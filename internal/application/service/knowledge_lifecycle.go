package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
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
	receipts     interfaces.ApprovalReceiptRepository
	// versions advances the authorization epoch on reclassification so
	// snapshot caches miss (§2.4 升密即时生效). Optional: nil skips it.
	versions interfaces.PermissionVersionService
	// goldenSets gates publish on the golden-set evaluation (§14.8). Optional:
	// nil means the deployment has no quality gate configured, which is the
	// correct reading for installs that never created a set.
	goldenSets interfaces.GoldenSetService
	now        func() time.Time
}

// NewKnowledgeLifecycleService constructs the lifecycle service.
func NewKnowledgeLifecycleService(
	kgRepo interfaces.KnowledgeRepository,
	versionRepo interfaces.KnowledgeVersionRepository,
	audit interfaces.AuditLogService,
	gamification interfaces.GamificationService,
	receipts interfaces.ApprovalReceiptRepository,
	versions interfaces.PermissionVersionService,
	goldenSets interfaces.GoldenSetService,
) interfaces.KnowledgeLifecycleService {
	return &knowledgeLifecycleService{
		kgRepo:       kgRepo,
		versionRepo:  versionRepo,
		audit:        audit,
		gamification: gamification,
		receipts:     receipts,
		goldenSets:   goldenSets,
		versions:     versions,
		now:          time.Now,
	}
}

// requireReceiptForPublish 判定义务性审批凭证（§6.2「知识更新」+§6.7）。
//
// 规则：secret / confidential 只有绑定本知识的 approved 回执才能发布；
// public / internal 保持自助发布。历史数据与上传即 valid 的路径不经过
// publish 动作，因此本门槛不会拦住存量内容。
func (s *knowledgeLifecycleService) requireReceiptForPublish(
	ctx context.Context, knowledge *types.Knowledge, receiptID string,
) error {
	if knowledge == nil {
		return nil
	}
	classification := types.ParseKnowledgeClassification(knowledge.Classification)
	if classification.Rank() < types.KnowledgeClassificationSecret.Rank() {
		return nil
	}
	if receiptID == "" {
		return apperrors.NewForbiddenError(
			"publishing " + string(classification) + " knowledge requires an approval receipt")
	}
	if s.receipts == nil {
		// 依赖缺失时放行会静默降级安全语义，宁可失败。
		return apperrors.NewInternalServerError("approval receipt repository is not configured")
	}
	receipt, err := s.receipts.Get(ctx, knowledge.TenantID, receiptID)
	if err != nil {
		return apperrors.NewForbiddenError(
			"approval receipt " + receiptID + " was not found for this tenant")
	}
	if receipt.KnowledgeID != knowledge.ID {
		// 回执必须绑定"这一条"知识，否则可以用别的回执偷渡发布。
		return apperrors.NewForbiddenError(
			"approval receipt does not belong to this knowledge")
	}
	if receipt.Decision != string(types.ApprovalDecisionApproved) {
		return apperrors.NewForbiddenError(
			"approval receipt decision is " + receipt.Decision + ", not approved")
	}
	return nil
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
		// 审批凭证门槛（§6.2/§6.7）：密级为 secret/confidential 的知识属于
		// "高风险不得单审"的范围，发布必须绑定一份属于本知识的、结论为通过
		// 的回执，否则任何人都能绕过审批直接发布。low/internal 维持自助发布
		// （上传即 valid 的存量流程不受影响）。
		if err := s.requireReceiptForPublish(ctx, knowledge, input.ReceiptID); err != nil {
			return nil, err
		}
		// 评测门禁（§14.8）：租户配了黄金问题集时，命中率不达标或出现越权
		// 召回就不得发布。没配集合的部署不受影响（nil 即未配置），这与
		// "没跑过评测"不同——后者由 CheckGate 自己失败闭合。
		if err := s.requireGoldenGate(ctx, knowledge); err != nil {
			return nil, err
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

	// 密级变更与撤权同样推进权限版本（§2.4 升密"即时生效"、§2.6 撤权传播）：
	// 升密之后，旧缓存里的答案摘要与派生内容都必须立刻不可用。
	if input.Action == types.KnowledgeLifecycleActionUpdateSettings &&
		input.Classification != nil && input.Classification.Valid() &&
		string(*input.Classification) != knowledge.Classification && s.versions != nil {
		if _, err := s.versions.BumpForKnowledge(ctx, updated.TenantID, updated.ID, updated.KnowledgeBaseID,
			permissionEpochReason(string(types.RevocationTriggerClassification), string(*input.Classification))); err != nil {
			logger.Warnf(ctx, "failed to bump permission version after reclassification of %s: %v", updated.ID, err)
		}
		if _, err := s.versions.RecordRevocation(ctx, updated.TenantID, updated.ID, "",
			types.RevocationTriggerClassification); err != nil {
			logger.Warnf(ctx, "failed to record reclassification propagation for %s: %v", updated.ID, err)
		}
	}
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
		"title": knowledge.Title,
		// description 是快照里唯一可比的正文文本：没有它，§7.4 的段落差异
		// 只能比标题，等于比不出来。
		"description":    knowledge.Description,
		"file_hash":      knowledge.FileHash,
		"file_size":      knowledge.StorageSize,
		"parse_status":   knowledge.ParseStatus,
		"classification": knowledge.Classification,
	}
	snapshotJSON := lifecycleJSON(snapshot)

	// 语义化版本（§7.4）：由作者选择的 bump 级别推进；没选时按变更类型
	// 推断（推断只降低填表负担，作者的选择优先）。
	bump := types.VersionBump(strings.TrimSpace(input.VersionBump))
	if !bump.Valid() {
		bump = types.ClassifyVersionBump(input.ChangeType)
	}
	previous := types.SemanticVersion{Major: 1}
	if latest != nil {
		if parsed, parseErr := types.ParseSemanticVersion(latest.SemanticVersion); parseErr == nil &&
			latest.SemanticVersion != "" {
			previous = parsed
		} else {
			// 旧行没有语义化版本：以递增号作为主版本起点，保证单调。
			previous = types.SemanticVersion{Major: latest.Version}
		}
	}
	semantic := previous.Bump(bump)

	record := &types.KnowledgeVersion{
		TenantID:        knowledge.TenantID,
		KnowledgeID:     knowledge.ID,
		Version:         version,
		SemanticVersion: semantic.String(),
		BumpSize:        string(bump),
		ContentHash:     knowledge.FileHash,
		Title:           knowledge.Title,
		ChangeNote:      input.ChangeNote,
		Snapshot:        snapshotJSON,
		PublishedBy:     input.ActorID,
		ReceiptID:       input.ReceiptID,
		EffectiveAt:     knowledge.EffectiveAt,
		RolledBackFrom:  input.RolledBackFrom,
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

// requireGoldenGate blocks publish when the space's golden-set evaluation is
// failing (KnowAxis V3.0 §14.8 上线前跑黄金问题集，不达标不发布).
//
// 门禁绑定在**空间**上而不是每篇知识：黄金集评测的是检索质量，质量是
// 空间级属性。同一空间里发布第二篇文档时跑的是同一份集合，不会因为
// "这篇没被评测过"而放行。
func (s *knowledgeLifecycleService) requireGoldenGate(
	ctx context.Context, knowledge *types.Knowledge,
) error {
	if s.goldenSets == nil || knowledge == nil || knowledge.KnowledgeBaseID == "" {
		return nil
	}
	tenantID := knowledge.TenantID
	if tenantID == 0 {
		tenantID, _ = types.TenantIDFromContext(ctx)
	}
	if tenantID == 0 {
		return nil
	}
	sets, err := s.goldenSets.ListSets(ctx, tenantID, "")
	if err != nil {
		// 查询失败时**不**阻断发布：黄金集是质量门禁，不是安全门禁，用它
		// 打掉全部发布会让一次数据库抖动变成全站停更。错误进日志即可。
		logger.Warnf(ctx, "[lifecycle] golden set lookup failed for publish gate: %v", err)
		return nil
	}
	for _, set := range sets {
		if set == nil || set.KBID != knowledge.KnowledgeBaseID {
			continue
		}
		pass, run, gateErr := s.goldenSets.CheckGate(ctx, tenantID, set.ID)
		if gateErr != nil {
			logger.Warnf(ctx, "[lifecycle] golden gate check failed for set %s: %v", set.ID, gateErr)
			continue
		}
		if !pass {
			reason := "golden set evaluation did not pass"
			if run == nil {
				reason = "golden set has no completed evaluation run"
			}
			s.logGoldenGateBlocked(ctx, tenantID, knowledge, set, run, reason)
			return apperrors.NewForbiddenError(
				"发布被评测门禁阻断：" + reason + "（黄金问题集 " + set.Name + "）")
		}
	}
	return nil
}

// logGoldenGateBlocked records a refused publish; a blocked release is a
// governance event and must be traceable.
func (s *knowledgeLifecycleService) logGoldenGateBlocked(
	ctx context.Context, tenantID uint64, knowledge *types.Knowledge,
	set *types.GoldenSet, run *types.GoldenSetRun, reason string,
) {
	if s.audit == nil {
		return
	}
	runIDValue := ""
	if run != nil {
		runIDValue = run.ID
	}
	_ = s.audit.Log(ctx, &types.AuditLog{
		TenantID:    tenantID,
		ActorUserID: "system",
		Action:      types.AuditAction(auditActionLifecycleTransition),
		ScopeType:   "knowledge",
		ScopeID:     knowledge.KnowledgeBaseID,
		TargetType:  "knowledge",
		TargetID:    knowledge.ID,
		Outcome:     "denied",
		Details: lifecycleJSON(map[string]interface{}{
			"action": "publish",
			"gate":   "golden_set",
			"set_id": set.ID,
			"run_id": runIDValue,
			"reason": reason,
		}),
	})
}
