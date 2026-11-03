package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// 官方答案的业务哨兵错误。
var (
	// ErrOfficialAnswerNotFound 官方答案不存在（或不在本租户）。
	ErrOfficialAnswerNotFound = errors.New("official answer: not found")
	// ErrOfficialAnswerInvalidState 状态机不允许该动作。
	ErrOfficialAnswerInvalidState = errors.New("official answer: status does not allow this action")
	// ErrOfficialAnswerReceiptRequired 高风险官方答案缺审批回执。
	ErrOfficialAnswerReceiptRequired = errors.New("official answer: a high-risk answer requires an approval receipt to publish")
	// ErrOfficialAnswerDuplicate 同一问题已有生效答案。
	ErrOfficialAnswerDuplicate = errors.New("official answer: an active answer already exists for this question")
	// ErrOfficialAnswerEvidenceRequired 官方答案必须绑定证据（§13.7/§6.7）。
	ErrOfficialAnswerEvidenceRequired = errors.New("official answer: at least one evidence knowledge entry is required")
)

// officialAnswerService implements interfaces.OfficialAnswerService (§13.7).
//
// The design follows the review flow deliberately: an official answer is the
// highest-trust artifact the platform produces, so it goes through the same
// approval-receipt gate as a secret document, and it degrades the same way —
// by an explicit state change with an audit trail, never a silent edit.
type officialAnswerService struct {
	repo       interfaces.OfficialAnswerRepository
	receipts   interfaces.ApprovalReceiptRepository
	knowledges interfaces.KnowledgeRepository
	audit      interfaces.AuditLogService
	now        func() time.Time
}

// officialAnswerAuditAction is the audit action prefix for this vertical.
const officialAnswerAuditAction = "official_answer"

// NewOfficialAnswerService constructs the service.
func NewOfficialAnswerService(
	repo interfaces.OfficialAnswerRepository,
	receipts interfaces.ApprovalReceiptRepository,
	knowledges interfaces.KnowledgeRepository,
	audit interfaces.AuditLogService,
) interfaces.OfficialAnswerService {
	return &officialAnswerService{
		repo: repo, receipts: receipts, knowledges: knowledges, audit: audit, now: time.Now,
	}
}

// Save creates or edits a draft.
//
// Editing a published/approved answer moves it back to draft: silently changing
// an approved standard answer would invalidate the receipt that authorised it.
func (s *officialAnswerService) Save(
	ctx context.Context, tenantID uint64, answer *types.OfficialAnswer, actorID string,
) (*types.OfficialAnswer, error) {
	if answer == nil {
		return nil, apperrors.NewBadRequestError("answer is required")
	}
	answer.Question = strings.TrimSpace(answer.Question)
	answer.QuestionNormalized = types.NormalizeOfficialAnswerQuestion(answer.Question)
	if answer.QuestionNormalized == "" {
		return nil, apperrors.NewBadRequestError("question is required")
	}
	if strings.TrimSpace(answer.Answer) == "" {
		return nil, apperrors.NewBadRequestError("answer body is required")
	}
	if answer.RiskLevel == "" {
		answer.RiskLevel = string(types.ApprovalRiskMid)
	}
	answer.TenantID = tenantID
	answer.UpdatedAt = s.now()

	if answer.ID == "" {
		answer.Status = string(types.OfficialAnswerDraft)
		answer.CreatedBy = actorID
		answer.CreatedAt = s.now()
		if err := s.repo.Create(ctx, answer); err != nil {
			return nil, err
		}
		s.auditEvent(ctx, tenantID, answer, actorID, "created", nil)
		return answer, nil
	}

	existing, err := s.repo.GetByID(ctx, tenantID, answer.ID)
	if err != nil {
		return nil, s.mapRepoError(err)
	}
	// 已批准的答案被改动 → 必须重新走审批，回执对应的是旧正文。
	switch types.OfficialAnswerStatus(existing.Status) {
	case types.OfficialAnswerApproved, types.OfficialAnswerPublished:
		answer.Status = string(types.OfficialAnswerDraft)
		answer.ReceiptID = ""
		answer.Version = existing.Version
	default:
		answer.Status = existing.Status
		answer.Version = existing.Version
		answer.ReceiptID = existing.ReceiptID
	}
	answer.CreatedAt = existing.CreatedAt
	answer.CreatedBy = existing.CreatedBy
	if err := s.repo.Update(ctx, answer); err != nil {
		return nil, err
	}
	s.auditEvent(ctx, tenantID, answer, actorID, "updated", map[string]interface{}{
		"previous_status": existing.Status,
		"reset_to_draft":  answer.Status == string(types.OfficialAnswerDraft) && existing.Status != answer.Status,
	})
	return answer, nil
}

// SubmitReview moves a draft into review.
func (s *officialAnswerService) SubmitReview(
	ctx context.Context, tenantID uint64, answerID, actorID string,
) (*types.OfficialAnswer, error) {
	answer, err := s.repo.GetByID(ctx, tenantID, answerID)
	if err != nil {
		return nil, s.mapRepoError(err)
	}
	if !types.OfficialAnswerStatus(answer.Status).CanTransitionTo(types.OfficialAnswerInReview) {
		return nil, ErrOfficialAnswerInvalidState
	}
	// 证据是硬约束（§13.7 必须绑定证据）：没有来源的标准答案无法被审计或撤回。
	if len(answer.EvidenceIDs()) == 0 {
		return nil, ErrOfficialAnswerEvidenceRequired
	}
	answer.Status = string(types.OfficialAnswerInReview)
	answer.UpdatedAt = s.now()
	if err := s.repo.Update(ctx, answer); err != nil {
		return nil, err
	}
	s.auditEvent(ctx, tenantID, answer, actorID, "submitted", nil)
	return answer, nil
}

// Approve records the approval and stamps the receipt.
func (s *officialAnswerService) Approve(
	ctx context.Context, tenantID uint64, answerID, approverID, receiptID string,
) (*types.OfficialAnswer, error) {
	answer, err := s.repo.GetByID(ctx, tenantID, answerID)
	if err != nil {
		return nil, s.mapRepoError(err)
	}
	if !types.OfficialAnswerStatus(answer.Status).CanTransitionTo(types.OfficialAnswerApproved) {
		return nil, ErrOfficialAnswerInvalidState
	}
	// 四眼原则：作者不能批准自己写的官方答案。
	if approverID == answer.CreatedBy {
		return nil, ErrReviewSelfDecision
	}
	// 高风险官方答案必须绑定回执（§13.7 高风险内容禁止绕过审批）。
	if answer.RiskLevel == string(types.ApprovalRiskHigh) {
		if receiptID == "" {
			return nil, ErrOfficialAnswerReceiptRequired
		}
		if s.receipts == nil {
			return nil, apperrors.NewInternalServerError("approval receipt repository is not configured")
		}
		receipt, rerr := s.receipts.Get(ctx, tenantID, receiptID)
		if rerr != nil || receipt == nil ||
			receipt.Decision != string(types.ApprovalDecisionApproved) {
			return nil, ErrOfficialAnswerReceiptRequired
		}
	}
	answer.Status = string(types.OfficialAnswerApproved)
	answer.ReviewedBy = approverID
	answer.ReceiptID = receiptID
	answer.UpdatedAt = s.now()
	if err := s.repo.Update(ctx, answer); err != nil {
		return nil, err
	}
	s.auditEvent(ctx, tenantID, answer, approverID, "approved", map[string]interface{}{
		"receipt_id": receiptID,
	})
	return answer, nil
}

// Publish makes an approved answer live.
func (s *officialAnswerService) Publish(
	ctx context.Context, tenantID uint64, answerID, actorID string,
) (*types.OfficialAnswer, error) {
	answer, err := s.repo.GetByID(ctx, tenantID, answerID)
	if err != nil {
		return nil, s.mapRepoError(err)
	}
	if !types.OfficialAnswerStatus(answer.Status).CanTransitionTo(types.OfficialAnswerPublished) {
		return nil, ErrOfficialAnswerInvalidState
	}
	// 同一问题只能有一份生效答案，否则同一句话会得到两个"标准"回答。
	if active, aerr := s.repo.FindActiveByQuestion(
		ctx, tenantID, answer.QuestionNormalized, answer.ID); aerr == nil &&
		active != nil && active.ID != answer.ID {
		return nil, ErrOfficialAnswerDuplicate
	}
	answer.Status = string(types.OfficialAnswerPublished)
	answer.Version++
	answer.UpdatedAt = s.now()
	if err := s.repo.Update(ctx, answer); err != nil {
		return nil, err
	}
	s.auditEvent(ctx, tenantID, answer, actorID, "published", map[string]interface{}{
		"version": answer.Version,
	})
	return answer, nil
}

// Retire withdraws an answer, optionally pointing at its replacement.
func (s *officialAnswerService) Retire(
	ctx context.Context, tenantID uint64, answerID, actorID, supersededByID string,
) (*types.OfficialAnswer, error) {
	answer, err := s.repo.GetByID(ctx, tenantID, answerID)
	if err != nil {
		return nil, s.mapRepoError(err)
	}
	next := types.OfficialAnswerRetired
	if supersededByID != "" {
		next = types.OfficialAnswerSuperseded
		answer.SupersededByID = supersededByID
	}
	if !types.OfficialAnswerStatus(answer.Status).CanTransitionTo(next) {
		return nil, ErrOfficialAnswerInvalidState
	}
	answer.Status = string(next)
	answer.UpdatedAt = s.now()
	if err := s.repo.Update(ctx, answer); err != nil {
		return nil, err
	}
	s.auditEvent(ctx, tenantID, answer, actorID, "retired", map[string]interface{}{
		"superseded_by": supersededByID,
	})
	return answer, nil
}

// Get returns one answer.
func (s *officialAnswerService) Get(
	ctx context.Context, tenantID uint64, answerID string,
) (*types.OfficialAnswer, error) {
	if answerID == "" {
		return nil, apperrors.NewBadRequestError("answer_id is required")
	}
	answer, err := s.repo.GetByID(ctx, tenantID, answerID)
	if err != nil {
		return nil, s.mapRepoError(err)
	}
	return answer, nil
}

// List returns answers of one tenant.
func (s *officialAnswerService) List(
	ctx context.Context, tenantID uint64, status types.OfficialAnswerStatus, limit int,
) ([]*types.OfficialAnswer, error) {
	if status != "" && !status.Valid() {
		return nil, apperrors.NewBadRequestError("unknown status")
	}
	return s.repo.List(ctx, tenantID, status, limit)
}

// Lookup answers a question from the official-answer set.
//
// A nil result means "no official answer"; an expired answer is returned with
// its resolved state so the answer path can say "this standard answer expired"
// rather than pretending nothing exists (§13.7).
func (s *officialAnswerService) Lookup(
	ctx context.Context, tenantID uint64, question, kbID string,
) (*types.OfficialAnswerMatch, error) {
	normalized := types.NormalizeOfficialAnswerQuestion(question)
	if normalized == "" {
		return nil, nil
	}
	answer, err := s.repo.FindAnswerable(ctx, tenantID, normalized, kbID)
	if err != nil {
		if errors.Is(err, repository.ErrOfficialAnswerNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &types.OfficialAnswerMatch{
		Answer:          answer,
		EffectiveStatus: types.ResolveOfficialAnswerState(answer, s.now()),
		Exact:           true,
	}, nil
}

// DemoteDue expires answers whose validity window ended.
func (s *officialAnswerService) DemoteDue(ctx context.Context, tenantID uint64) (int, error) {
	rows, err := s.repo.ListExpiredDue(ctx, tenantID, s.now(), 100)
	if err != nil {
		return 0, err
	}
	demoted := 0
	for _, answer := range rows {
		if answer == nil {
			continue
		}
		answer.Status = string(types.OfficialAnswerExpired)
		answer.UpdatedAt = s.now()
		if err := s.repo.Update(ctx, answer); err != nil {
			logger.Warnf(ctx, "official answer: expire %s failed: %v", answer.ID, err)
			continue
		}
		demoted++
		s.auditEvent(ctx, tenantID, answer, "system", "expired", map[string]interface{}{
			"expires_at": answer.ExpiresAt,
		})
	}
	return demoted, nil
}

// CheckEvidence demotes answers that cite a knowledge entry which is no longer
// available (§13.7 证据或版本失效时自动降级并通知责任人).
func (s *officialAnswerService) CheckEvidence(
	ctx context.Context, tenantID uint64, knowledgeID string,
) (int, error) {
	if knowledgeID == "" {
		return 0, nil
	}
	// 调用方可能是"删除正在回滚"的路径：先确认这条知识确实已经不可用，否则
	// 会把仍然有效的标准答案误降级（降级后需要人工重新审批才能恢复）。
	if s.knowledges != nil {
		if knowledge, kerr := s.knowledges.GetKnowledgeByID(ctx, tenantID, knowledgeID); kerr == nil && knowledge != nil {
			return 0, nil
		}
	}
	rows, err := s.repo.List(ctx, tenantID, "", 200)
	if err != nil {
		return 0, err
	}
	demoted := 0
	for _, answer := range rows {
		if answer == nil || !types.OfficialAnswerStatus(answer.Status).Answerable() {
			continue
		}
		if !answerCitesKnowledge(answer, knowledgeID) {
			continue
		}
		answer.Status = string(types.OfficialAnswerExpired)
		answer.UpdatedAt = s.now()
		if err := s.repo.Update(ctx, answer); err != nil {
			logger.Warnf(ctx, "official answer: demote %s failed: %v", answer.ID, err)
			continue
		}
		demoted++
		s.auditEvent(ctx, tenantID, answer, "system", "evidence_withdrawn", map[string]interface{}{
			"knowledge_id": knowledgeID,
			"owner_id":     answer.OwnerID,
		})
	}
	return demoted, nil
}

// answerCitesKnowledge reports whether the answer's evidence includes the id.
func answerCitesKnowledge(answer *types.OfficialAnswer, knowledgeID string) bool {
	for _, id := range answer.EvidenceIDs() {
		if id == knowledgeID {
			return true
		}
	}
	return false
}

// mapRepoError normalises repository misses into a business error.
func (s *officialAnswerService) mapRepoError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, repository.ErrOfficialAnswerNotFound) {
		return ErrOfficialAnswerNotFound
	}
	return err
}

// auditEvent writes the official-answer audit trail (best-effort).
func (s *officialAnswerService) auditEvent(
	ctx context.Context, tenantID uint64, answer *types.OfficialAnswer,
	actorID, action string, extra map[string]interface{},
) {
	if s.audit == nil || answer == nil {
		return
	}
	details := map[string]interface{}{
		"status":     answer.Status,
		"risk_level": answer.RiskLevel,
		"version":    answer.Version,
	}
	for key, value := range extra {
		details[key] = value
	}
	_ = s.audit.Log(ctx, &types.AuditLog{
		TenantID:    tenantID,
		ActorUserID: actorID,
		Action:      types.AuditAction(officialAnswerAuditAction + "." + action),
		ScopeType:   "official_answer",
		ScopeID:     answer.ID,
		Outcome:     types.AuditOutcomeSuccess,
		Details:     lifecycleJSON(details),
	})
}
