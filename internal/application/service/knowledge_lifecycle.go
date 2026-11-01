package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
)

// lifecycleJSON marshals v, falling back to an empty object on failure so audit
// writes never block a lifecycle transition over a logging payload.
func lifecycleJSON(v interface{}) types.JSON {
	data, err := json.Marshal(v)
	if err != nil {
		return types.JSON([]byte("{}"))
	}
	return types.JSON(data)
}

// Lifecycle audit actions (V3.0 §7.1: every state change is audited).
const (
	auditActionLifecycleTransition = "knowledge.lifecycle_transition"
	auditActionLifecycleSettings   = "knowledge.lifecycle_settings"
	auditActionKnowledgeVersion    = "knowledge.version_published"
)

// knowledgeLifecycleService implements interfaces.KnowledgeLifecycleService.
// It is the single door through which lifecycle states change: transitions are
// validated against the state machine, published versions are snapshotted and
// every action writes an audit event (V3.0 §7.1 hard rules: no silent flips).
type knowledgeLifecycleService struct {
	kgRepo       interfaces.KnowledgeRepository
	versionRepo  interfaces.KnowledgeVersionRepository
	audit        interfaces.AuditLogService
	gamification interfaces.GamificationService
	now          func() time.Time
}

// NewKnowledgeLifecycleService constructs the lifecycle service.
func NewKnowledgeLifecycleService(
	kgRepo interfaces.KnowledgeRepository,
	versionRepo interfaces.KnowledgeVersionRepository,
	audit interfaces.AuditLogService,
	gamification interfaces.GamificationService,
) interfaces.KnowledgeLifecycleService {
	return &knowledgeLifecycleService{
		kgRepo:       kgRepo,
		versionRepo:  versionRepo,
		audit:        audit,
		gamification: gamification,
		now:          time.Now,
	}
}

// Transition validates and applies one lifecycle action.
func (s *knowledgeLifecycleService) Transition(
	ctx context.Context,
	input *types.KnowledgeLifecycleTransitionInput,
) (*types.Knowledge, error) {
	if input == nil || input.KnowledgeID == "" || input.Action == "" {
		return nil, apperrors.NewBadRequestError("lifecycle action and knowledge id are required")
	}

	knowledge, err := s.kgRepo.GetKnowledgeByIDOnly(ctx, input.KnowledgeID)
	if err != nil {
		return nil, err
	}

	current := types.ParseKnowledgeLifecycleStatus(knowledge.LifecycleStatus)
	updates := map[string]interface{}{"updated_at": s.now()}

	switch input.Action {
	case types.KnowledgeLifecycleActionSubmitReview:
		if next := types.KnowledgeLifecyclePendingReview; !current.CanTransitionTo(next) {
			return nil, s.illegalTransition(ctx, knowledge, current, next, input.ActorID)
		}
		updates["lifecycle_status"] = string(types.KnowledgeLifecyclePendingReview)

	case types.KnowledgeLifecycleActionPublish:
		if next := types.KnowledgeLifecycleValid; !current.CanTransitionTo(next) &&
			!current.CanTransitionTo(types.KnowledgeLifecyclePendingEffective) {
			return nil, s.illegalTransition(ctx, knowledge, current, types.KnowledgeLifecycleValid, input.ActorID)
		}
		// 生效日未来 → 待生效；否则立即有效。
		next := types.KnowledgeLifecycleValid
		if knowledge.EffectiveAt != nil && knowledge.EffectiveAt.After(s.now()) {
			next = types.KnowledgeLifecyclePendingEffective
		}
		updates["lifecycle_status"] = string(next)

		version, versionErr := s.writeVersionSnapshot(ctx, knowledge, input)
		if versionErr != nil {
			return nil, versionErr
		}
		updates["current_version"] = version

	case types.KnowledgeLifecycleActionRejectReview:
		if next := types.KnowledgeLifecycleDraft; !current.CanTransitionTo(next) {
			return nil, s.illegalTransition(ctx, knowledge, current, next, input.ActorID)
		}
		// 驳回即不可用：draft 不参与检索，修复后重新提交。
		updates["lifecycle_status"] = string(types.KnowledgeLifecycleDraft)

	case types.KnowledgeLifecycleActionConfirmReview:
		if next := types.KnowledgeLifecycleValid; !current.CanTransitionTo(next) {
			return nil, s.illegalTransition(ctx, knowledge, current, next, input.ActorID)
		}
		now := s.now()
		updates["lifecycle_status"] = string(types.KnowledgeLifecycleValid)
		updates["last_reviewed_at"] = now
		updates["last_reviewed_by"] = input.ActorID

	case types.KnowledgeLifecycleActionMarkStale:
		if next := types.KnowledgeLifecycleStale; !current.CanTransitionTo(next) {
			return nil, s.illegalTransition(ctx, knowledge, current, next, input.ActorID)
		}
		updates["lifecycle_status"] = string(types.KnowledgeLifecycleStale)

	case types.KnowledgeLifecycleActionMarkPendingReviewCycle:
		// 复审调度器批量到期（C4）：valid→pending_review_cycle，同时记录
		// 复审待办来源，行为上仍可检索但降权（§7.1 状态行为表）。
		if next := types.KnowledgeLifecyclePendingReviewCycle; !current.CanTransitionTo(next) {
			return nil, s.illegalTransition(ctx, knowledge, current, next, input.ActorID)
		}
		updates["lifecycle_status"] = string(types.KnowledgeLifecyclePendingReviewCycle)

	case types.KnowledgeLifecycleActionMarkExpired:
		if next := types.KnowledgeLifecycleExpired; !current.CanTransitionTo(next) {
			return nil, s.illegalTransition(ctx, knowledge, current, next, input.ActorID)
		}
		updates["lifecycle_status"] = string(types.KnowledgeLifecycleExpired)

	case types.KnowledgeLifecycleActionReplace:
		if next := types.KnowledgeLifecycleReplaced; !current.CanTransitionTo(next) {
			return nil, s.illegalTransition(ctx, knowledge, current, next, input.ActorID)
		}
		if input.ReplacedByID == "" {
			return nil, apperrors.NewBadRequestError("replace action requires replaced_by_id")
		}
		updates["lifecycle_status"] = string(types.KnowledgeLifecycleReplaced)
		updates["replaced_by_id"] = input.ReplacedByID

	case types.KnowledgeLifecycleActionMarkConflict:
		if next := types.KnowledgeLifecycleConflict; !current.CanTransitionTo(next) {
			return nil, s.illegalTransition(ctx, knowledge, current, next, input.ActorID)
		}
		updates["lifecycle_status"] = string(types.KnowledgeLifecycleConflict)

	case types.KnowledgeLifecycleActionDelist:
		if next := types.KnowledgeLifecycleDelisted; !current.CanTransitionTo(next) {
			return nil, s.illegalTransition(ctx, knowledge, current, next, input.ActorID)
		}
		updates["lifecycle_status"] = string(types.KnowledgeLifecycleDelisted)

	case types.KnowledgeLifecycleActionArchive:
		if next := types.KnowledgeLifecycleArchived; !current.CanTransitionTo(next) {
			return nil, s.illegalTransition(ctx, knowledge, current, next, input.ActorID)
		}
		updates["lifecycle_status"] = string(types.KnowledgeLifecycleArchived)

	case types.KnowledgeLifecycleActionRestore:
		if next := types.KnowledgeLifecycleValid; !current.CanTransitionTo(next) {
			return nil, s.illegalTransition(ctx, knowledge, current, next, input.ActorID)
		}
		// 恢复是提权动作，调用方必须带审批凭证（A2 闭环后强制；v1 记录审计）。
		updates["lifecycle_status"] = string(types.KnowledgeLifecycleValid)
		updates["replaced_by_id"] = ""

	case types.KnowledgeLifecycleActionUpdateSettings:
		s.applySettings(knowledge, input, updates)

	default:
		return nil, apperrors.NewBadRequestError(fmt.Sprintf("unknown lifecycle action: %s", input.Action))
	}

	if err := s.kgRepo.UpdateKnowledgeColumns(ctx, input.KnowledgeID, updates); err != nil {
		return nil, err
	}

	updated, err := s.kgRepo.GetKnowledgeByIDOnly(ctx, input.KnowledgeID)
	if err != nil {
		return nil, err
	}

	// 行为埋点（KnowAxis V3.0 §15.4）：发布即"知识创建"加分。幂等键为
	// (knowledge_created, knowledge, knowledge.ID)，重复发布/复审放行不会
	// 重复记分；加分失败只记日志，不阻断生命周期转换。
	if input.Action == types.KnowledgeLifecycleActionPublish && s.gamification != nil {
		if _, err := s.gamification.AwardPoints(ctx, updated.TenantID, input.ActorID,
			types.PointsActionKnowledgeCreated, "knowledge", updated.ID); err != nil {
			logger.Warnf(ctx, "failed to award knowledge_created points for knowledge %s: %v", updated.ID, err)
		}
	}

	s.logTransition(ctx, knowledge, updated, input)
	return updated, nil
}

// writeVersionSnapshot appends the next immutable published-version row and
// returns the new version number.
func (s *knowledgeLifecycleService) writeVersionSnapshot(
	ctx context.Context,
	knowledge *types.Knowledge,
	input *types.KnowledgeLifecycleTransitionInput,
) (int, error) {
	latest, err := s.versionRepo.GetLatestKnowledgeVersion(ctx, knowledge.ID)
	if err != nil {
		return 0, err
	}
	version := 1
	if latest != nil {
		version = latest.Version + 1
	}
	snapshot := map[string]interface{}{
		"title":         knowledge.Title,
		"file_hash":     knowledge.FileHash,
		"file_size":     knowledge.StorageSize,
		"parse_status":  knowledge.ParseStatus,
		"classification": knowledge.Classification,
	}
	snapshotJSON := lifecycleJSON(snapshot)
	record := &types.KnowledgeVersion{
		TenantID:    knowledge.TenantID,
		KnowledgeID: knowledge.ID,
		Version:     version,
		ContentHash: knowledge.FileHash,
		Title:       knowledge.Title,
		ChangeNote:  input.ChangeNote,
		Snapshot:    snapshotJSON,
		PublishedBy: input.ActorID,
		ReceiptID:   input.ReceiptID,
		EffectiveAt: knowledge.EffectiveAt,
	}
	if err := s.versionRepo.CreateKnowledgeVersion(ctx, record); err != nil {
		return 0, err
	}
	s.logVersionPublished(ctx, knowledge, version, input)
	return version, nil
}

// applySettings merges governance settings into the update map. Settings
// changes never move the lifecycle state (V3.0 §7.1: 状态变更必须显式动作).
func (s *knowledgeLifecycleService) applySettings(
	knowledge *types.Knowledge,
	input *types.KnowledgeLifecycleTransitionInput,
	updates map[string]interface{},
) {
	if input.Classification != nil && input.Classification.Valid() {
		updates["classification"] = string(*input.Classification)
	}
	if input.EffectiveAt != nil {
		updates["effective_at"] = *input.EffectiveAt
	}
	if input.ExpiresAt != nil {
		updates["expires_at"] = *input.ExpiresAt
	}
	if input.ReviewCycleDays != nil && *input.ReviewCycleDays >= 0 {
		updates["review_cycle_days"] = *input.ReviewCycleDays
	}
}

// logTransition writes the audit event for a lifecycle state change.
func (s *knowledgeLifecycleService) logTransition(
	ctx context.Context,
	before, after *types.Knowledge,
	input *types.KnowledgeLifecycleTransitionInput,
) {
	if s.audit == nil {
		return
	}
	action := types.AuditAction(auditActionLifecycleTransition)
	if input.Action == types.KnowledgeLifecycleActionUpdateSettings {
		action = types.AuditAction(auditActionLifecycleSettings)
	}
	_ = s.audit.Log(ctx, &types.AuditLog{
		TenantID:    after.TenantID,
		ActorUserID: input.ActorID,
		Action:      action,
		ScopeType:   "knowledge",
		ScopeID:     after.KnowledgeBaseID,
		TargetType:  "knowledge",
		TargetID:    after.ID,
		Outcome:     "success",
		Details: lifecycleJSON(map[string]interface{}{
			"action":      string(input.Action),
			"from_state":  before.LifecycleStatus,
			"to_state":    after.LifecycleStatus,
			"change_note": input.ChangeNote,
			"receipt_id":  input.ReceiptID,
		}),
	})
}

// logVersionPublished writes the version-published audit event.
func (s *knowledgeLifecycleService) logVersionPublished(
	ctx context.Context,
	knowledge *types.Knowledge,
	version int,
	input *types.KnowledgeLifecycleTransitionInput,
) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Log(ctx, &types.AuditLog{
		TenantID:    knowledge.TenantID,
		ActorUserID: input.ActorID,
		Action:      auditActionKnowledgeVersion,
		ScopeType:   "knowledge",
		ScopeID:     knowledge.KnowledgeBaseID,
		TargetType:  "knowledge",
		TargetID:    knowledge.ID,
		Outcome:     "success",
		Details: lifecycleJSON(map[string]interface{}{
			"version":     version,
			"receipt_id":  input.ReceiptID,
			"change_note": input.ChangeNote,
		}),
	})
}

// illegalTransition returns a 400 with the offending states and records a
// denied audit entry: an illegal transition attempt is a governance signal.
func (s *knowledgeLifecycleService) illegalTransition(
	ctx context.Context,
	knowledge *types.Knowledge,
	current, requested types.KnowledgeLifecycleStatus,
	actorID string,
) error {
	if s.audit != nil {
		_ = s.audit.Log(ctx, &types.AuditLog{
			TenantID:    knowledge.TenantID,
			ActorUserID: actorID,
			Action:      auditActionLifecycleTransition,
			ScopeType:   "knowledge",
			ScopeID:     knowledge.KnowledgeBaseID,
			TargetType:  "knowledge",
			TargetID:    knowledge.ID,
			Outcome:     "denied",
			Details: lifecycleJSON(map[string]interface{}{
				"action":     "illegal_transition",
				"from_state": string(current),
				"to_state":   string(requested),
			}),
		})
	}
	logger.Warnf(ctx, "Illegal lifecycle transition: knowledge=%s from=%s to=%s actor=%s",
		knowledge.ID, current, requested, actorID)
	return apperrors.NewBadRequestError(fmt.Sprintf(
		"illegal lifecycle transition: %s -> %s", current, requested))
}

// GetResolved loads one knowledge entry and computes its read-time behaviour.
func (s *knowledgeLifecycleService) GetResolved(
	ctx context.Context,
	knowledgeID string,
) (*types.Knowledge, types.KnowledgeLifecycleResolved, error) {
	knowledge, err := s.kgRepo.GetKnowledgeByIDOnly(ctx, knowledgeID)
	if err != nil {
		return nil, types.KnowledgeLifecycleResolved{}, err
	}
	return knowledge, s.ResolvedForDisplay(knowledge, s.now()), nil
}

// ListVersions returns the published-version history, newest first.
func (s *knowledgeLifecycleService) ListVersions(
	ctx context.Context,
	knowledgeID string,
	limit int,
) ([]*types.KnowledgeVersion, error) {
	return s.versionRepo.ListKnowledgeVersions(ctx, knowledgeID, limit)
}

// ResolvedForDisplay computes the read-time behaviour of one entry.
func (s *knowledgeLifecycleService) ResolvedForDisplay(
	k *types.Knowledge,
	now time.Time,
) types.KnowledgeLifecycleResolved {
	return types.ResolveLifecycle(
		types.ParseKnowledgeLifecycleStatus(k.LifecycleStatus),
		k.EffectiveAt,
		k.ExpiresAt,
		now,
	)
}
