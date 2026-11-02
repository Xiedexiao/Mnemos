package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
)

// 候选卡片业务哨兵错误。handler 层据此映射 HTTP 语义（404/403/409/400），
// 与 review / change_request 等服务的错误风格一致。
var (
	// ErrCandidateTenantMissing 请求上下文缺少租户（中间件层未注入）。
	ErrCandidateTenantMissing = errors.New("knowledge candidate: tenant context is missing")
	// ErrCandidateActorMissing 调用者缺少用户身份（候选卡片必须可归因）。
	ErrCandidateActorMissing = errors.New("knowledge candidate: caller identity is missing")
	// ErrCandidateNotFound 候选卡片不存在（或不在本租户）。
	ErrCandidateNotFound = errors.New("knowledge candidate: candidate not found")
	// ErrCandidateInvalidState 状态机不允许该动作（终态/顺序不符的一切迁移）。
	ErrCandidateInvalidState = errors.New("knowledge candidate: status does not allow this action")
	// ErrCandidateTitleRequired 标题必填。
	ErrCandidateTitleRequired = errors.New("knowledge candidate: title is required")
	// ErrCandidateTitleTooLong 标题超过 varchar(255) 容量。
	ErrCandidateTitleTooLong = errors.New("knowledge candidate: title exceeds 255 characters")
	// ErrCandidateInvalidSourceType source_type 不在九类来源白名单内。
	ErrCandidateInvalidSourceType = errors.New("knowledge candidate: unknown source type")
	// ErrCandidateInvalidLevel level 不在 L0-L3 白名单内。
	ErrCandidateInvalidLevel = errors.New("knowledge candidate: unknown level")
	// ErrCandidateInvalidConfidence confidence 不在白名单内。
	ErrCandidateInvalidConfidence = errors.New("knowledge candidate: unknown confidence")
	// ErrCandidateForbidden 调用者不是提交人本人且不具备 Admin 权限
	// （确认推进/撤回的身份门槛）。
	ErrCandidateForbidden = errors.New("knowledge candidate: caller is not allowed to perform this action")
	// ErrCandidateNotSubmitter 撤回只允许提交人本人（v1 不做代办撤回）。
	ErrCandidateNotSubmitter = errors.New("knowledge candidate: only the requester can withdraw this candidate")
	// ErrCandidateLevelGate §10.4 门禁：L3 发布需要 Admin 审核人且
	// confidence != AMBIGUOUS（否则 400）。
	ErrCandidateLevelGate = errors.New("knowledge candidate: L3 publication requires an Admin reviewer and non-AMBIGUOUS confidence")
	// ErrCandidateKBNotFound 发布目标 KB 不存在（或不在本租户）。
	ErrCandidateKBNotFound = errors.New("knowledge candidate: target knowledge base not found")
	// ErrCandidateSourceUnavailable 引用为知识来源的证据已不可解析（被删除或
	// 跨租户）。派生知识无法证明自己不比源证据更可见，因此不得发布（§2.4：
	// 派生权限取源证据交集，交集为空则不可发布）。
	ErrCandidateSourceUnavailable = errors.New("knowledge candidate: a cited source knowledge is unavailable, so the derived permission ceiling cannot be computed")
)

const (
	// candidateAuditScope 是候选卡片审计事件的 scope_type。
	candidateAuditScope = "knowledge_candidate"
	// candidateTitleMaxRunes 是标题的字符上限（与列宽 varchar(255) 对齐）。
	candidateTitleMaxRunes = 255
	// candidateAuditCommentLimit 限制进入审计详情的意见长度，避免审计行膨胀。
	candidateAuditCommentLimit = 200
)

// candidateService implements interfaces.KnowledgeCandidateService
// （KnowAxis V3.0 §10 v1：候选卡片 + 审核发布闭环）。
//
// 发布不走 KnowledgeLifecycleService —— 那是既有正式知识的状态机；候选
// 发布是在 repository.CreateKnowledge 直接建 manual 知识行（lifecycle 默认
// valid），层级与溯源写进 knowledge.CustomMetadata。
type candidateService struct {
	candidates interfaces.KnowledgeCandidateRepository
	knowledges interfaces.KnowledgeRepository
	kbs        interfaces.KnowledgeBaseRepository
	audit      interfaces.AuditLogService
	now        func() time.Time
}

// NewKnowledgeCandidateService constructs the production candidate service.
func NewKnowledgeCandidateService(
	candidates interfaces.KnowledgeCandidateRepository,
	knowledges interfaces.KnowledgeRepository,
	kbs interfaces.KnowledgeBaseRepository,
	audit interfaces.AuditLogService,
) interfaces.KnowledgeCandidateService {
	return &candidateService{
		candidates: candidates,
		knowledges: knowledges,
		kbs:        kbs,
		audit:      audit,
		now:        time.Now,
	}
}

// CreateCandidate 创建候选卡片（手工 / 未来连接器共用）：
//  1. 校验来源（九类白名单，空默认 manual）、层级（L0-L3，空默认 L1）、
//     置信（EXTRACTED|INFERRED|AMBIGUOUS，空默认 AMBIGUOUS）；
//  2. v1 查重：同租户同标题精确命中 → duplicate（duplicate_of 指向既有
//     候选）；标题 LIKE 相似命中 → conflict；否则 unique；
//  3. 落卡（status=candidate）并写审计 candidate.created。
func (s *candidateService) CreateCandidate(
	ctx context.Context, input types.KnowledgeCandidateCreateInput,
) (*types.KnowledgeCandidate, error) {
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		return nil, ErrCandidateTenantMissing
	}

	sourceType := input.SourceType
	if sourceType == "" {
		sourceType = types.KnowledgeCandidateSourceManual
	}
	if !sourceType.Valid() {
		return nil, ErrCandidateInvalidSourceType
	}
	level := input.Level
	if level == "" {
		level = types.KnowledgeCandidateLevelL1
	}
	if !level.Valid() {
		return nil, ErrCandidateInvalidLevel
	}
	confidence := input.Confidence
	if confidence == "" {
		confidence = types.KnowledgeCandidateConfidenceAmbiguous
	}
	if !confidence.Valid() {
		return nil, ErrCandidateInvalidConfidence
	}

	title := strings.TrimSpace(input.Title)
	if title == "" {
		return nil, ErrCandidateTitleRequired
	}
	if len([]rune(title)) > candidateTitleMaxRunes {
		return nil, ErrCandidateTitleTooLong
	}

	requestedBy := input.RequestedBy
	if requestedBy == "" {
		requestedBy = types.CallerFromContext(ctx).UserID
	}
	if requestedBy == "" {
		return nil, ErrCandidateActorMissing
	}

	card := types.KnowledgeCandidateCard{}
	if input.Card != nil {
		card = *input.Card
	}
	cardRaw, err := json.Marshal(card)
	if err != nil {
		return nil, fmt.Errorf("knowledge candidate: marshal card: %w", err)
	}
	evidence := input.Evidence
	if evidence == nil {
		evidence = []types.KnowledgeCandidateEvidence{}
	}
	evidenceRaw, err := json.Marshal(evidence)
	if err != nil {
		return nil, fmt.Errorf("knowledge candidate: marshal evidence: %w", err)
	}

	// v1 查重：精确命中 → duplicate；否则 LIKE 相似 → conflict；否则 unique。
	dedup := string(types.KnowledgeCandidateDedupUnique)
	duplicateOf := ""
	exact, err := s.candidates.FindExactTitle(ctx, tenantID, title, "")
	if err != nil {
		return nil, err
	}
	if len(exact) > 0 {
		dedup = string(types.KnowledgeCandidateDedupDuplicate)
		duplicateOf = exact[0].ID
	} else if similar, err := s.candidates.CountSimilarTitles(ctx, tenantID, title, ""); err != nil {
		return nil, err
	} else if similar > 0 {
		dedup = string(types.KnowledgeCandidateDedupConflict)
	}

	now := s.now()
	candidate := &types.KnowledgeCandidate{
		ID:          uuid.NewString(),
		TenantID:    tenantID,
		SourceType:  string(sourceType),
		SourceID:    input.SourceID,
		Title:       title,
		Level:       string(level),
		Card:        types.JSON(cardRaw),
		Evidence:    types.JSON(evidenceRaw),
		Confidence:  string(confidence),
		DedupStatus: dedup,
		DuplicateOf: duplicateOf,
		Status:      string(types.KnowledgeCandidateStatusCandidate),
		RequestedBy: requestedBy,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.candidates.Create(ctx, candidate); err != nil {
		return nil, err
	}

	s.candidateAuditEvent(ctx, candidate, types.AuditActionCandidateCreated, types.AuditOutcomeSuccess, map[string]any{
		"source_type":  candidate.SourceType,
		"source_id":    candidate.SourceID,
		"level":        candidate.Level,
		"confidence":   candidate.Confidence,
		"dedup_status": candidate.DedupStatus,
		"duplicate_of": candidate.DuplicateOf,
	})
	return candidate, nil
}

// ConfirmCandidate 确认推进。同一入口按当前状态分两段：
//   - candidate→in_review：提交人触发（提交人本人或 Admin 可调，他人 403）；
//   - in_review→confirmed：审核人触发，v1 审核人 = Admin+（403 兜底，路由
//     层已要求）；L1→L2 晋级（§10.4）。
func (s *candidateService) ConfirmCandidate(ctx context.Context, candidateID, actorID string) (*types.KnowledgeCandidate, error) {
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		return nil, ErrCandidateTenantMissing
	}
	if actorID == "" {
		return nil, ErrCandidateActorMissing
	}
	candidate, err := s.loadCandidate(ctx, tenantID, candidateID)
	if err != nil {
		return nil, err
	}

	from := types.KnowledgeCandidateStatus(candidate.Status)
	switch from {
	case types.KnowledgeCandidateStatusCandidate:
		// 提交人触发；他人（非 Admin）不得代为提交。
		caller := types.CallerFromContext(ctx)
		if actorID != candidate.RequestedBy && !caller.Role.HasPermission(types.TenantRoleAdmin) {
			return nil, ErrCandidateForbidden
		}
		if !from.CanTransitionTo(types.KnowledgeCandidateStatusInReview) {
			return nil, ErrCandidateInvalidState
		}
		candidate.Status = string(types.KnowledgeCandidateStatusInReview)
		candidate.UpdatedAt = s.now()
		if err := s.candidates.Update(ctx, candidate); err != nil {
			return nil, err
		}
		s.candidateAuditEvent(ctx, candidate, types.AuditActionCandidateConfirmed, types.AuditOutcomeAccepted, map[string]any{
			"from":   string(from),
			"to":     candidate.Status,
			"actor":  actorID,
			"level":  candidate.Level,
			"status": "submitted_for_review",
		})
		return candidate, nil

	case types.KnowledgeCandidateStatusInReview:
		// 审核人触发；v1 审核人 = Admin+（路由 Contributor 挂载下的服务层兜底）。
		caller := types.CallerFromContext(ctx)
		if !caller.Role.HasPermission(types.TenantRoleAdmin) {
			return nil, ErrCandidateForbidden
		}
		if !from.CanTransitionTo(types.KnowledgeCandidateStatusConfirmed) {
			return nil, ErrCandidateInvalidState
		}
		candidate.Status = string(types.KnowledgeCandidateStatusConfirmed)
		candidate.ReviewedBy = actorID
		if types.KnowledgeCandidateLevel(candidate.Level) == types.KnowledgeCandidateLevelL1 {
			// L1→L2 晋级（§10.4）：审核确认即"已确认"层级。
			candidate.Level = string(types.KnowledgeCandidateLevelL2)
		}
		candidate.UpdatedAt = s.now()
		if err := s.candidates.Update(ctx, candidate); err != nil {
			return nil, err
		}
		s.candidateAuditEvent(ctx, candidate, types.AuditActionCandidateConfirmed, types.AuditOutcomeSuccess, map[string]any{
			"from":     string(from),
			"to":       candidate.Status,
			"actor":    actorID,
			"level":    candidate.Level,
			"reviewer": candidate.ReviewedBy,
		})
		return candidate, nil

	default:
		return nil, ErrCandidateInvalidState
	}
}

// PublishCandidate 发布（confirmed→published；§10.4 门禁：level=L3 需
// Admin 审核人且 confidence != AMBIGUOUS，否则 400）。创建 manual 正式
// 知识：title=候选标题、content=卡片字段拼 Markdown、lifecycle 默认 valid、
// level/溯源写 CustomMetadata；knowledge_id 回填候选并写审计。
// kbID 可选：提供时校验存在并落入该 KB；为空生成未归档知识行。
func (s *candidateService) PublishCandidate(
	ctx context.Context, candidateID, publisherID, kbID string,
) (*types.KnowledgeCandidate, error) {
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		return nil, ErrCandidateTenantMissing
	}
	if publisherID == "" {
		return nil, ErrCandidateActorMissing
	}
	candidate, err := s.loadCandidate(ctx, tenantID, candidateID)
	if err != nil {
		return nil, err
	}
	from := types.KnowledgeCandidateStatus(candidate.Status)
	if !from.CanTransitionTo(types.KnowledgeCandidateStatusPublished) {
		return nil, ErrCandidateInvalidState
	}

	// §10.4 门禁：L3 权威知识不得来自含糊证据，且必须由 Admin 发布。
	if types.KnowledgeCandidateLevel(candidate.Level) == types.KnowledgeCandidateLevelL3 {
		caller := types.CallerFromContext(ctx)
		if !caller.Role.HasPermission(types.TenantRoleAdmin) ||
			candidate.Confidence == string(types.KnowledgeCandidateConfidenceAmbiguous) {
			return nil, ErrCandidateLevelGate
		}
	}

	// 发布目标 KB（可选）：提供时必须存在且属于本租户。
	kbID = strings.TrimSpace(kbID)
	if kbID != "" && s.kbs != nil {
		if _, err := s.kbs.GetKnowledgeBaseByIDAndTenant(ctx, kbID, tenantID); err != nil {
			if errors.Is(err, repository.ErrKnowledgeBaseNotFound) {
				return nil, ErrCandidateKBNotFound
			}
			return nil, err
		}
	}

	// 派生权限门禁（§2.4）：案例/FAQ 等派生知识的可见范围取全部源证据的
	// 交集，不得因为标成 L2 就比原始工单更可见。这里计算源证据的最严密级，
	// 让新知识继承它，而不是落到 KB 默认的 internal。
	derived, err := s.derivedPermission(ctx, tenantID, candidate)
	if err != nil {
		return nil, err
	}

	knowledge, err := s.createManualKnowledgeFromCandidate(ctx, tenantID, candidate, kbID, derived)
	if err != nil {
		return nil, err
	}

	candidate.Status = string(types.KnowledgeCandidateStatusPublished)
	candidate.KnowledgeID = knowledge.ID
	candidate.UpdatedAt = s.now()
	if err := s.candidates.Update(ctx, candidate); err != nil {
		return nil, err
	}

	s.candidateAuditEvent(ctx, candidate, types.AuditActionCandidatePublished, types.AuditOutcomeSuccess, map[string]any{
		"from":         string(from),
		"to":           candidate.Status,
		"publisher":    publisherID,
		"knowledge_id": candidate.KnowledgeID,
		"level":        candidate.Level,
		"kb_id":        kbID,
	})
	return candidate, nil
}

// createManualKnowledgeFromCandidate 把候选卡片物化为 manual 正式知识行：
//   - title = 候选标题；content = 卡片字段拼 Markdown（§10.3 固定小节）；
//   - lifecycle 默认 valid（不入 draft/pending_review，发布即生效）；
//   - level 与溯源写 CustomMetadata（knowaxis_level / knowaxis_candidate_id /
//     knowaxis_candidate_source_type）；
//   - 解析状态直接 completed：正文内联在 manual metadata，无解析管线可跑
//     （v1 不入 chunk/向量检索，检索接入为后续版本）。
//
// derivedPermission resolves the classification a published candidate must
// inherit from its source evidence (V3.0 §2.4 派生交集).
//
// Unresolvable evidence is a hard error rather than a silent default: if a cited
// source document no longer exists or lives in another tenant, the derived entry
// cannot prove its visible range does not exceed the source, so it must not
// publish.
func (s *candidateService) derivedPermission(
	ctx context.Context, tenantID uint64, candidate *types.KnowledgeCandidate,
) (types.KnowledgeClassification, error) {
	resolver := &derivedPermissionResolver{knowledges: s.knowledges}
	sourceIDs := candidateSourceKnowledgeIDs(candidate)
	strictest, missing, err := resolver.derivedClassification(ctx, tenantID, sourceIDs)
	if err != nil {
		return types.KnowledgeClassificationInternal, err
	}
	if missing > 0 {
		return types.KnowledgeClassificationInternal, ErrCandidateSourceUnavailable
	}
	return strictest, nil
}

func (s *candidateService) createManualKnowledgeFromCandidate(
	ctx context.Context, tenantID uint64, candidate *types.KnowledgeCandidate, kbID string,
	classification types.KnowledgeClassification,
) (*types.Knowledge, error) {
	card := candidate.CardStruct()
	content := types.KnowledgeCandidateMarkdown(card)

	customMeta, err := json.Marshal(map[string]string{
		types.KnowledgeCandidateMetaLevelKey:       candidate.Level,
		types.KnowledgeCandidateMetaCandidateIDKey: candidate.ID,
		types.KnowledgeCandidateMetaSourceTypeKey:  candidate.SourceType,
	})
	if err != nil {
		return nil, fmt.Errorf("knowledge candidate: marshal custom metadata: %w", err)
	}

	now := s.now()
	knowledge := &types.Knowledge{
		ID:              uuid.NewString(),
		TenantID:        tenantID,
		KnowledgeBaseID: kbID, // 空 = 未归档（kb 列 NOT NULL，空串合法）
		Type:            types.KnowledgeTypeManual,
		Title:           candidate.Title,
		Description:     card.What,
		Source:          types.KnowledgeTypeManual,
		ParseStatus:     types.ParseStatusCompleted,
		EnableStatus:    "enabled",
		LifecycleStatus: string(types.KnowledgeLifecycleValid),
		// 继承源证据的最严密级（§2.4）；没有可解析源证据时按 internal 处理，
		// 由调用方决定是否允许发布。
		Classification: string(classification),
		CustomMetadata: types.JSON(customMeta),
		FileName:       fmt.Sprintf("%s.md", candidate.Title),
		FileType:       types.KnowledgeTypeManual,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := knowledge.SetManualMetadata(
		types.NewManualKnowledgeMetadata(content, types.ManualKnowledgeStatusPublish, 1),
	); err != nil {
		return nil, fmt.Errorf("knowledge candidate: set manual metadata: %w", err)
	}
	knowledge.EnsureManualDefaults()

	if err := s.knowledges.CreateKnowledge(ctx, knowledge); err != nil {
		return nil, fmt.Errorf("knowledge candidate: create manual knowledge: %w", err)
	}
	return knowledge, nil
}

// RejectCandidate 审核驳回（in_review→rejected；终态）。记录审核人与意见，
// 写审计 candidate.rejected。
func (s *candidateService) RejectCandidate(
	ctx context.Context, candidateID, reviewerID, comment string,
) (*types.KnowledgeCandidate, error) {
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		return nil, ErrCandidateTenantMissing
	}
	if reviewerID == "" {
		return nil, ErrCandidateActorMissing
	}
	candidate, err := s.loadCandidate(ctx, tenantID, candidateID)
	if err != nil {
		return nil, err
	}
	from := types.KnowledgeCandidateStatus(candidate.Status)
	if !from.CanTransitionTo(types.KnowledgeCandidateStatusRejected) {
		return nil, ErrCandidateInvalidState
	}

	candidate.Status = string(types.KnowledgeCandidateStatusRejected)
	candidate.ReviewedBy = reviewerID
	candidate.ReviewComment = comment
	candidate.UpdatedAt = s.now()
	if err := s.candidates.Update(ctx, candidate); err != nil {
		return nil, err
	}

	s.candidateAuditEvent(ctx, candidate, types.AuditActionCandidateRejected, types.AuditOutcomeSuccess, map[string]any{
		"from":     string(from),
		"to":       candidate.Status,
		"reviewer": reviewerID,
		"comment":  truncateCandidateComment(comment),
	})
	return candidate, nil
}

// WithdrawCandidate 提交人撤回（candidate|in_review→withdrawn；终态）。
// v1 仅提交人本人可撤；Admin 想下架走 reject。
func (s *candidateService) WithdrawCandidate(ctx context.Context, candidateID, actorID string) (*types.KnowledgeCandidate, error) {
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		return nil, ErrCandidateTenantMissing
	}
	if actorID == "" {
		return nil, ErrCandidateActorMissing
	}
	candidate, err := s.loadCandidate(ctx, tenantID, candidateID)
	if err != nil {
		return nil, err
	}
	if actorID != candidate.RequestedBy {
		return nil, ErrCandidateNotSubmitter
	}
	from := types.KnowledgeCandidateStatus(candidate.Status)
	if !from.CanTransitionTo(types.KnowledgeCandidateStatusWithdrawn) {
		return nil, ErrCandidateInvalidState
	}

	candidate.Status = string(types.KnowledgeCandidateStatusWithdrawn)
	candidate.UpdatedAt = s.now()
	if err := s.candidates.Update(ctx, candidate); err != nil {
		return nil, err
	}

	s.candidateAuditEvent(ctx, candidate, types.AuditActionCandidateConfirmed, types.AuditOutcomeSuccess, map[string]any{
		"from":   string(from),
		"to":     candidate.Status,
		"actor":  actorID,
		"status": "withdrawn",
	})
	return candidate, nil
}

// ListCandidates 分页列出候选（status/level 过滤）。
func (s *candidateService) ListCandidates(
	ctx context.Context, filter types.KnowledgeCandidateListFilter,
) ([]*types.KnowledgeCandidate, int64, error) {
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		return nil, 0, ErrCandidateTenantMissing
	}
	return s.candidates.List(ctx, tenantID, filter)
}

// GetCandidate 按候选 ID 取单条（租户隔离）。
func (s *candidateService) GetCandidate(ctx context.Context, candidateID string) (*types.KnowledgeCandidate, error) {
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		return nil, ErrCandidateTenantMissing
	}
	return s.loadCandidate(ctx, tenantID, candidateID)
}

// ============================================================================
// 内部助手
// ============================================================================

// loadCandidate 按租户加载候选卡片，仓储哨兵映射为业务哨兵。
func (s *candidateService) loadCandidate(ctx context.Context, tenantID uint64, candidateID string) (*types.KnowledgeCandidate, error) {
	if candidateID == "" {
		return nil, ErrCandidateNotFound
	}
	candidate, err := s.candidates.GetByID(ctx, tenantID, candidateID)
	if err != nil {
		if errors.Is(err, repository.ErrKnowledgeCandidateNotFound) {
			return nil, ErrCandidateNotFound
		}
		return nil, err
	}
	return candidate, nil
}

// candidateAuditEvent 把候选事件写进租户审计流（best-effort：审计故障不
// 回滚业务）。候选卡片没有归属 KB，直接以 scope_type=knowledge_candidate
// 落审计（与变更单 crAuditEvent 同风格），不经 recordKBActivity。
func (s *candidateService) candidateAuditEvent(
	ctx context.Context,
	candidate *types.KnowledgeCandidate,
	action types.AuditAction,
	outcome types.AuditOutcome,
	details map[string]any,
) {
	if s.audit == nil {
		return
	}
	var detailJSON types.JSON
	if len(details) > 0 {
		if payload, err := json.Marshal(details); err == nil {
			detailJSON = types.JSON(payload)
		}
	}
	_ = s.audit.Log(ctx, &types.AuditLog{
		TenantID:    candidate.TenantID,
		ActorUserID: auditActor(ctx),
		ActorRole:   auditActorRole(ctx),
		Action:      action,
		ScopeType:   candidateAuditScope,
		ScopeID:     candidate.ID,
		TargetType:  candidateAuditScope,
		TargetID:    candidate.ID,
		Outcome:     outcome,
		Details:     detailJSON,
	})
}

// truncateCandidateComment 限制进入审计详情的意见长度。
func truncateCandidateComment(comment string) string {
	runes := []rune(comment)
	if len(runes) <= candidateAuditCommentLimit {
		return comment
	}
	return string(runes[:candidateAuditCommentLimit]) + "…"
}
