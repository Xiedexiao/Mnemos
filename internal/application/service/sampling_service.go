package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// 抽检（§6.6）的审计动作。
const (
	auditActionSamplingDrawn  = "sampling.inspections_drawn"
	auditActionSamplingDecide = "sampling.inspection_decided"
)

// samplingService implements interfaces.SamplingService
// (KnowAxis V3.0 §6.6 抽检与质量).
type samplingService struct {
	repo    interfaces.SamplingRepository
	quality interfaces.QualityTaskRepository
	audit   interfaces.AuditLogService
	now     func() time.Time
}

// NewSamplingService constructs the service.
func NewSamplingService(
	repo interfaces.SamplingRepository,
	quality interfaces.QualityTaskRepository,
	audit interfaces.AuditLogService,
) interfaces.SamplingService {
	return &samplingService{repo: repo, quality: quality, audit: audit, now: time.Now}
}

// Draw performs one sampling pass.
//
// 候选按"最近更新"取，因为最新发布的内容出错代价最高（还没经过读者检验）。
// 是否抽中由 (文档, 周期) 的哈希决定，不随机：同一期重跑得到同一批样本，
// 复盘时可重现。
func (s *samplingService) Draw(
	ctx context.Context, tenantID uint64, period string, limit int,
) (*types.SamplingRunResult, error) {
	if tenantID == 0 {
		return nil, apperrors.NewUnauthorizedError("tenant context is required")
	}
	now := s.now().UTC()
	if period == "" {
		period = types.SamplingPeriod(now)
	}
	candidates, err := s.repo.DrawCandidates(ctx, tenantID, time.Time{}, limit)
	if err != nil {
		return nil, err
	}
	result := &types.SamplingRunResult{
		Period: period, RanAt: now, ByRatio: map[int]int{},
	}
	for _, candidate := range candidates {
		if candidate == nil {
			continue
		}
		result.Candidates++
		ratio := types.SamplingRatioForRisk(candidate.RiskLevel)
		if !types.ShouldSample(candidate.KnowledgeID, period, ratio) {
			continue
		}
		inspection := &types.SamplingInspection{
			TenantID:        tenantID,
			KnowledgeID:     candidate.KnowledgeID,
			KnowledgeBaseID: candidate.KnowledgeBaseID,
			Period:          period,
			RiskLevel:       candidate.RiskLevel,
			Ratio:           int(ratio),
			Title:           truncateRunes(candidate.Title, 255),
			Status:          string(types.SamplingPending),
			CreatedAt:       now,
		}
		created, insertErr := s.repo.Insert(ctx, inspection)
		if insertErr != nil {
			logger.Warnf(ctx, "[sampling] insert failed for %s: %v", candidate.KnowledgeID, insertErr)
			continue
		}
		if !created {
			// 同一期已经抽过这一篇：重跑是常态（定时任务每天跑一次），
			// 不应产生重复样本。
			result.SkippedExisting++
			continue
		}
		result.Sampled++
		result.ByRatio[int(ratio)]++
	}
	if result.Sampled > 0 {
		s.logAudit(ctx, tenantID, "", auditActionSamplingDrawn, "", map[string]interface{}{
			"period":     period,
			"sampled":    result.Sampled,
			"by_ratio":   result.ByRatio,
			"candidates": result.Candidates,
		})
	}
	return result, nil
}

// List returns the inspection queue.
func (s *samplingService) List(
	ctx context.Context, tenantID uint64, period string,
	status types.SamplingDecisionStatus, limit int,
) ([]*types.SamplingInspection, error) {
	if status != "" && !status.Valid() {
		return nil, apperrors.NewBadRequestError("unknown inspection status")
	}
	return s.repo.List(ctx, tenantID, period, status, limit)
}

// Decide records a verdict.
//
// 抽检不通过时开一张纠错任务：抽检的价值不在"发现问题"，而在"问题被修掉"。
// 任务失败不阻断结论落库（结论本身是审计事实），但要记日志。
func (s *samplingService) Decide(
	ctx context.Context, tenantID uint64, actorID string,
	input types.SamplingDecisionInput,
) (*types.SamplingInspection, error) {
	if err := types.ValidateSamplingDecision(input); err != nil {
		return nil, apperrors.NewBadRequestError(err.Error())
	}
	status := types.SamplingDecisionStatus(input.Status)
	inspection, err := s.repo.Get(ctx, tenantID, input.InspectionID)
	if err != nil {
		return nil, mapSamplingError(err)
	}
	followUpTaskID := ""
	if status == types.SamplingFailed {
		followUpTaskID = s.openCorrectiveTask(ctx, tenantID, actorID, inspection, input.Note)
	}
	decided, err := s.repo.Decide(ctx, tenantID, input.InspectionID, status,
		actorID, truncateRunes(input.Note, 1024), followUpTaskID, s.now().UTC())
	if err != nil {
		return nil, mapSamplingError(err)
	}
	s.logAudit(ctx, tenantID, actorID, auditActionSamplingDecide, inspection.ID,
		map[string]interface{}{
			"status":         string(status),
			"knowledge_id":   inspection.KnowledgeID,
			"risk_level":     inspection.RiskLevel,
			"follow_up_task": followUpTaskID,
		})
	return decided, nil
}

// openCorrectiveTask files a quality task for a failed inspection.
func (s *samplingService) openCorrectiveTask(
	ctx context.Context, tenantID uint64, actorID string,
	inspection *types.SamplingInspection, note string,
) string {
	if s.quality == nil || inspection == nil {
		return ""
	}
	// 同一篇已有未关闭的纠错任务时复用：抽检与用户反馈指向同一处问题时，
	// 开两张任务只会让负责人收到两条互不相干的待办。
	if existing, err := s.quality.HasOpenByKnowledgeAndType(
		ctx, tenantID, types.QualityTaskTypeSamplingFailed, inspection.KnowledgeID); err == nil && existing {
		return ""
	}
	task := &types.QualityTask{
		TenantID:    tenantID,
		TaskType:    types.QualityTaskTypeSamplingFailed,
		SourceType:  types.QualityTaskSourceKnowledge,
		SourceID:    inspection.ID,
		KnowledgeID: inspection.KnowledgeID,
		Detail: samplingDetailJSON(map[string]interface{}{
			"inspection_id": inspection.ID,
			"period":        inspection.Period,
			"risk_level":    inspection.RiskLevel,
			"note":          note,
			"raised_by":     actorID,
		}),
		Status:    types.QualityTaskStatusOpen,
		CreatedAt: s.now().UTC(),
	}
	if err := s.quality.Create(ctx, task); err != nil {
		logger.Warnf(ctx, "[sampling] open corrective task failed for %s: %v", inspection.KnowledgeID, err)
		return ""
	}
	return task.ID
}

// Stats aggregates the inspection outcomes.
func (s *samplingService) Stats(
	ctx context.Context, tenantID uint64, period string,
) (*types.SamplingStats, error) {
	return s.repo.Stats(ctx, tenantID, period)
}

// samplingDetailJSON encodes the task detail, falling back to an empty object.
func samplingDetailJSON(payload map[string]interface{}) types.JSON {
	raw, err := json.Marshal(payload)
	if err != nil {
		return types.JSON("{}")
	}
	return types.JSON(raw)
}

// logAudit writes one sampling audit event.
func (s *samplingService) logAudit(
	ctx context.Context, tenantID uint64, actorID, action, targetID string,
	details map[string]interface{},
) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Log(ctx, &types.AuditLog{
		TenantID:    tenantID,
		ActorUserID: actorID,
		Action:      types.AuditAction(action),
		ScopeType:   "sampling",
		TargetType:  "sampling_inspection",
		TargetID:    targetID,
		Outcome:     "success",
		Details:     samplingDetailJSON(details),
	})
}

// mapSamplingError maps repository misses onto the API 404.
func mapSamplingError(err error) error {
	if errors.Is(err, repository.ErrSamplingInspectionNotFound) {
		return apperrors.NewNotFoundError("sampling inspection not found or already decided")
	}
	return err
}

// compile-time guard.
var _ interfaces.SamplingService = (*samplingService)(nil)
