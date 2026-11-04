package service

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// watermarkService implements interfaces.WatermarkService
// (KnowAxis V3.0 §16.3 服务端水印与下载留痕).
type watermarkService struct {
	repo interfaces.WatermarkRepository
	// audit records the download as an audit event as well; optional.
	audit interfaces.AuditLogService
	now   func() time.Time
}

// NewWatermarkService constructs the service.
func NewWatermarkService(
	repo interfaces.WatermarkRepository, audit interfaces.AuditLogService,
) interfaces.WatermarkService {
	return &watermarkService{repo: repo, audit: audit, now: time.Now}
}

// Now exposes the clock so a caller can render the same timestamp it records.
func (s *watermarkService) Now() time.Time { return s.now().UTC() }

// Plan resolves the mark for one download.
func (s *watermarkService) Plan(
	ctx context.Context, tenantID uint64, userID string,
	knowledge *types.Knowledge, fileName string, source string, requestID string,
) types.WatermarkApplied {
	if knowledge == nil {
		return types.WatermarkApplied{}
	}
	title := knowledge.Title
	userLabel := watermarkUserLabel(ctx, userID)
	applied := types.PlanWatermark(
		fileName, knowledge.ID, title, userLabel,
		types.ParseKnowledgeClassification(knowledge.Classification),
		s.Now(),
	)
	return applied
}

// Record writes the ledger row for a delivered download.
func (s *watermarkService) Record(
	ctx context.Context, tenantID uint64, userID string,
	knowledge *types.Knowledge, applied types.WatermarkApplied, source, requestID string,
) error {
	if knowledge == nil || !types.WatermarkRequired(
		types.ParseKnowledgeClassification(knowledge.Classification)) {
		return nil
	}
	record := &types.WatermarkRecord{
		TenantID:        tenantID,
		UserID:          userID,
		KnowledgeID:     knowledge.ID,
		KnowledgeBaseID: knowledge.KnowledgeBaseID,
		FileName:        truncateRunes(applied.FileName, 512),
		Classification:  string(types.ParseKnowledgeClassification(knowledge.Classification)),
		Strength:        string(applied.Strength),
		Mode:            string(applied.Mode),
		WatermarkText:   truncateRunes(applied.Text, 255),
		Source:          source,
		RequestID:       requestID,
		CreatedAt:       s.Now(),
	}
	if err := s.repo.Create(ctx, record); err != nil {
		// 台账写失败不阻断下载：文件已经在传了，此时报错只会让用户拿到一个
		// 不完整的响应。但必须记日志——台账缺失是需要人工补记的事件。
		logger.Warnf(ctx, "[watermark] record failed for knowledge %s user %s: %v",
			knowledge.ID, userID, err)
		return err
	}
	if s.audit != nil {
		_ = s.audit.Log(ctx, &types.AuditLog{
			TenantID:    tenantID,
			ActorUserID: userID,
			Action:      types.AuditAction("knowledge.watermarked_download"),
			ScopeType:   "knowledge",
			ScopeID:     knowledge.KnowledgeBaseID,
			TargetType:  "knowledge",
			TargetID:    knowledge.ID,
			Outcome:     "success",
			Details: gamificationAuditJSON(map[string]interface{}{
				"classification": record.Classification,
				"strength":       record.Strength,
				"mode":           record.Mode,
				"source":         source,
				"watermark":      record.WatermarkText,
			}),
		})
	}
	return nil
}

// List returns the tenant's watermark ledger.
func (s *watermarkService) List(
	ctx context.Context, tenantID uint64, knowledgeID, userID string, limit int,
) ([]*types.WatermarkRecord, error) {
	return s.repo.List(ctx, tenantID, knowledgeID, userID, limit)
}

// watermarkUserLabel renders the actor label embedded in the mark.
//
// 用 user id 而不是昵称：昵称可改、可重名，而水印要在文件流出之后仍然能
// 定位到唯一一个账号。
func watermarkUserLabel(ctx context.Context, userID string) string {
	if caller := types.CallerFromContext(ctx); caller.UserID != "" {
		return caller.UserID
	}
	return userID
}

// compile-time guard.
var _ interfaces.WatermarkService = (*watermarkService)(nil)
