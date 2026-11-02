package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/Tencent/WeKnora/internal/application/repository"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// 治理审批的业务哨兵错误。
var (
	// ErrGovernancePending 该主体已有待审任务（幂等防重）。
	ErrGovernancePending = errors.New("governance approval: a task is already pending for this subject")
	// ErrGovernanceNotApproved 动作尚未获批（§6.2 高风险动作不得先行生效）。
	ErrGovernanceNotApproved = errors.New("governance approval: the action is not approved")
)

// governanceApprovalService implements interfaces.GovernanceApprovalService.
//
// §6.2 lists fourteen governance actions that must go through the same approval
// and evidence machinery. Before this, only knowledge publishing did: an
// external share was auto-approved, a bulk export only flipped a status, and a
// deletion had no approval at all.
//
// The service deliberately reuses approval_tasks / approval_receipts /
// approval_task_nodes rather than introducing a parallel model, so the receipt
// hash chain, the four-eyes rule and the multi-stage chain all apply unchanged.
type governanceApprovalService struct {
	tasks     interfaces.ApprovalTaskRepository
	receipts  interfaces.ApprovalReceiptRepository
	flows     interfaces.ApprovalFlowRepository
	chain     interfaces.ApprovalChainService
	lifecycle interfaces.KnowledgeLifecycleService
	audit     interfaces.AuditLogService
	now       func() time.Time
}

// NewGovernanceApprovalService constructs the service.
func NewGovernanceApprovalService(
	tasks interfaces.ApprovalTaskRepository,
	receipts interfaces.ApprovalReceiptRepository,
	flows interfaces.ApprovalFlowRepository,
	chain interfaces.ApprovalChainService,
	lifecycle interfaces.KnowledgeLifecycleService,
	audit interfaces.AuditLogService,
) interfaces.GovernanceApprovalService {
	return &governanceApprovalService{
		tasks: tasks, receipts: receipts, flows: flows, chain: chain,
		lifecycle: lifecycle, audit: audit, now: time.Now,
	}
}

// Submit creates the approval task for one governance action.
//
// Knowledge subjects keep their existing lifecycle-driven path (SubmitReview);
// this entry point is for the other thirteen, which have no content state
// machine to advance — the receipt *is* the effect.
func (s *governanceApprovalService) Submit(
	ctx context.Context, tenantID uint64, requesterID string,
	req *types.GovernanceApprovalRequest,
) (*types.ApprovalTask, error) {
	if tenantID == 0 {
		return nil, apperrors.NewUnauthorizedError("tenant context is required")
	}
	if requesterID == "" {
		return nil, apperrors.NewUnauthorizedError("requester identity is required")
	}
	if err := req.Validate(); err != nil {
		return nil, apperrors.NewBadRequestError(err.Error())
	}
	req.Normalize()
	if req.SubjectType.IsKnowledgeSubject() {
		// 知识类主体必须走审核流：那里会冻结内容基线并推进生命周期。
		return nil, apperrors.NewBadRequestError(
			"knowledge subjects are submitted through the review flow, not here")
	}

	// 幂等：同一主体已有待审任务时不再新建，避免重复审批同一动作。
	if existing, err := s.tasks.FindLatestBySubject(ctx, tenantID,
		string(req.SubjectType), req.SubjectID); err == nil && existing != nil &&
		existing.Status == string(types.ApprovalTaskStatusPending) {
		return nil, ErrGovernancePending
	}

	resolution := ResolveApprovalChain(s.flowFor(ctx, tenantID, req.KBID), req.RiskLevel)
	now := s.now()
	slaDue := now.Add(s.slaDuration(ctx, tenantID, req.KBID))

	baseline, err := json.Marshal(map[string]interface{}{
		"subject_type": req.SubjectType,
		"subject_id":   req.SubjectID,
		"title":        req.Title,
		"reason":       req.Reason,
		"snapshot":     json.RawMessage(orEmptyObject(req.Snapshot)),
	})
	if err != nil {
		return nil, fmt.Errorf("governance approval: marshal baseline: %w", err)
	}

	task := &types.ApprovalTask{
		ID:          uuid.NewString(),
		TenantID:    tenantID,
		KnowledgeID: "", // 非知识主体没有 knowledge_id
		KBID:        req.KBID,
		SubjectType: string(req.SubjectType),
		SubjectID:   req.SubjectID,
		Title:       req.Title,
		Mode:        string(resolution.Mode),
		Baseline:    types.JSON(baseline),
		ContentHash: governanceContentHash(req),
		RiskLevel:   string(resolution.RiskLevel),
		Status:      string(types.ApprovalTaskStatusPending),
		AssigneeIDs: types.JSON("[]"),
		RequestedBy: requesterID,
		// decided_history 与 votes 双写：前者服务旧读取方与审计，后者驱动
		// 多级链。
		DecidedHistory: types.JSON("[]"),
		SLADue:         &slaDue,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.tasks.Create(ctx, task); err != nil {
		return nil, err
	}

	// 实例化审批链：或签/会签/逐级与知识审核完全一致。
	if s.chain != nil {
		if _, chainErr := s.chain.Instantiate(ctx, task, resolution); chainErr != nil {
			task.Status = string(types.ApprovalTaskStatusCanceled)
			task.UpdatedAt = s.now()
			_ = s.tasks.Update(ctx, task)
			return nil, fmt.Errorf("governance approval: instantiate chain: %w", chainErr)
		}
	}
	s.auditEvent(ctx, task, requesterID, "submitted", map[string]interface{}{
		"subject_type": req.SubjectType,
		"subject_id":   req.SubjectID,
		"risk_level":   task.RiskLevel,
	})
	return task, nil
}

// Decide records one approver's verdict on a governance task.
func (s *governanceApprovalService) Decide(
	ctx context.Context, tenantID uint64, taskID, approverID string,
	decision types.ApprovalDecision, comment string,
) (*types.ApprovalTask, error) {
	if approverID == "" {
		return nil, apperrors.NewUnauthorizedError("approver identity is required")
	}
	if !decision.Valid() {
		return nil, ErrReviewInvalidDecision
	}
	task, err := s.tasks.GetByID(ctx, tenantID, taskID)
	if err != nil {
		if errors.Is(err, repository.ErrApprovalTaskNotFound) {
			return nil, ErrReviewTaskNotFound
		}
		return nil, err
	}
	if task.Status != string(types.ApprovalTaskStatusPending) {
		return nil, ErrReviewTaskNotPending
	}
	// 四眼：提交人不能批自己的动作。
	if approverID == task.RequestedBy {
		return nil, ErrReviewSelfDecision
	}
	// 知识主体的审批由 ReviewService 负责（它要推进生命周期）。
	if group := types.ApprovalSubjectType(task.SubjectType); group.IsKnowledgeSubject() {
		return nil, apperrors.NewBadRequestError(
			"knowledge subjects are decided through the review flow")
	}

	now := s.now()
	var chainState *types.ApprovalChainState
	if s.chain != nil {
		state, chainErr := s.chain.Vote(ctx, task, approverID, string(decision), comment)
		if chainErr != nil {
			return nil, mapChainError(chainErr)
		}
		chainState = state
	}

	history, err := decodeApprovalDecisions(task.DecidedHistory)
	if err != nil {
		return nil, err
	}
	history = append(history, types.ApprovalDecisionRecord{
		By: approverID, Decision: string(decision), At: now,
	})
	if raw, marshalErr := json.Marshal(history); marshalErr == nil {
		task.DecidedHistory = types.JSON(raw)
	}
	task.Comment = comment
	task.UpdatedAt = now

	if decision == types.ApprovalDecisionRejected {
		task.Status = string(types.ApprovalTaskStatusRejected)
		task.DecidedBy = approverID
		task.DecisionAt = &now
		if err := s.tasks.Update(ctx, task); err != nil {
			return nil, err
		}
		s.auditEvent(ctx, task, approverID, "rejected", nil)
		return task, nil
	}

	// 链未走完：记票保持 pending。
	if chainState != nil && !chainState.Finished {
		if err := s.tasks.Update(ctx, task); err != nil {
			return nil, err
		}
		return task, nil
	}

	// 链走完：出回执。回执就是治理动作的生效凭据。
	task.Status = string(types.ApprovalTaskStatusApproved)
	task.DecidedBy = approverID
	task.DecisionAt = &now
	if s.receipts != nil {
		receipt := &types.ApprovalReceipt{
			ReceiptID:        newApprovalReceiptID(),
			TenantID:         task.TenantID,
			TaskID:           task.ID,
			KnowledgeID:      task.SubjectID, // 回执的知识列兼作治理主体 ID
			ContentHash:      task.ContentHash,
			BaselineSnapshot: task.Baseline,
			ApproverID:       approverID,
			Decision:         string(types.ApprovalDecisionApproved),
			CreatedAt:        now,
		}
		if err := s.receipts.Append(ctx, receipt); err != nil {
			return nil, fmt.Errorf("governance approval: append receipt: %w", err)
		}
		task.ReceiptID = receipt.ReceiptID
	}
	if err := s.tasks.Update(ctx, task); err != nil {
		return nil, err
	}
	s.auditEvent(ctx, task, approverID, "approved", map[string]interface{}{
		"receipt_id": task.ReceiptID,
	})
	return task, nil
}

// Status reports whether a governance action may take effect.
func (s *governanceApprovalService) Status(
	ctx context.Context, tenantID uint64, subjectType types.ApprovalSubjectType, subjectID string,
) (*types.GovernanceApprovalStatus, error) {
	if !subjectType.Valid() {
		return nil, apperrors.NewBadRequestError("unknown subject type")
	}
	status := &types.GovernanceApprovalStatus{
		SubjectType: subjectType, SubjectID: subjectID,
	}
	task, err := s.tasks.FindLatestBySubject(ctx, tenantID, string(subjectType), subjectID)
	if err != nil {
		// 没有任务：动作尚未提交审批。
		return status, nil
	}
	created := task.CreatedAt
	status.TaskID = task.ID
	status.Status = task.Status
	status.RiskLevel = task.RiskLevel
	status.ReceiptID = task.ReceiptID
	status.RequestedBy = task.RequestedBy
	status.CreatedAt = &created
	status.DecidedAt = task.DecisionAt
	return status, nil
}

// RequireApproval is the gate callers use before performing a gated action.
//
// Returns nil when the action may proceed: either the subject type is not gated,
// or an approved receipt exists. High-risk subjects are gated even when no task
// was ever submitted — "nobody asked" must not read as "nobody objected".
func (s *governanceApprovalService) RequireApproval(
	ctx context.Context, tenantID uint64, subjectType types.ApprovalSubjectType, subjectID string,
) (*types.GovernanceApprovalDecision, error) {
	decision := &types.GovernanceApprovalDecision{
		SubjectType:      subjectType,
		SubjectID:        subjectID,
		RequiresApproval: subjectType.RequiresReceiptBeforeEffect(),
	}
	if !decision.RequiresApproval {
		return decision, nil
	}
	task, err := s.tasks.FindLatestBySubject(ctx, tenantID, string(subjectType), subjectID)
	if err != nil {
		decision.Reason = "no approval task has been submitted for this action"
		return decision, ErrGovernanceNotApproved
	}
	if task.Status != string(types.ApprovalTaskStatusApproved) || task.ReceiptID == "" {
		decision.Reason = "the approval task is " + task.Status
		decision.TaskID = task.ID
		return decision, ErrGovernanceNotApproved
	}
	decision.Approved = true
	decision.ReceiptID = task.ReceiptID
	decision.TaskID = task.ID
	return decision, nil
}

// ListBySubjectType returns pending tasks of one subject type.
func (s *governanceApprovalService) ListBySubjectType(
	ctx context.Context, tenantID uint64, subjectType types.ApprovalSubjectType, limit int,
) ([]*types.ApprovalTask, error) {
	if subjectType != "" && !subjectType.Valid() {
		return nil, apperrors.NewBadRequestError("unknown subject type")
	}
	return s.tasks.ListPendingBySubject(ctx, tenantID, string(subjectType), limit)
}

// flowFor loads the approval flow of the task's space, or nil for the default.
func (s *governanceApprovalService) flowFor(
	ctx context.Context, tenantID uint64, kbID string,
) *types.ApprovalFlow {
	if s.flows == nil || kbID == "" {
		return nil
	}
	flow, err := s.flows.GetByKB(ctx, tenantID, kbID)
	if err != nil || flow == nil || !flow.Enabled {
		return nil
	}
	return flow
}

// slaDuration resolves the space's SLA for a governance task.
func (s *governanceApprovalService) slaDuration(
	ctx context.Context, tenantID uint64, kbID string,
) time.Duration {
	flow := s.flowFor(ctx, tenantID, kbID)
	if flow != nil && flow.SLAHours > 0 {
		return time.Duration(flow.SLAHours) * time.Hour
	}
	return reviewDefaultSLA
}

// auditEvent writes the governance audit trail.
func (s *governanceApprovalService) auditEvent(
	ctx context.Context, task *types.ApprovalTask, actorID, action string,
	extra map[string]interface{},
) {
	if s.audit == nil || task == nil {
		return
	}
	details := map[string]interface{}{
		"subject_type": task.SubjectType,
		"subject_id":   task.SubjectID,
		"title":        task.Title,
		"status":       task.Status,
	}
	for key, value := range extra {
		details[key] = value
	}
	_ = s.audit.Log(ctx, &types.AuditLog{
		TenantID:    task.TenantID,
		ActorUserID: actorID,
		Action:      types.AuditAction("governance." + action),
		ScopeType:   task.SubjectType,
		ScopeID:     task.SubjectID,
		TargetType:  "approval_task",
		TargetID:    task.ID,
		Outcome:     types.AuditOutcomeSuccess,
		Details:     lifecycleJSON(details),
	})
}

// governanceContentHash fingerprints the frozen baseline so a receipt certifies
// exactly the snapshot the approver saw.
func governanceContentHash(req *types.GovernanceApprovalRequest) string {
	payload := fmt.Sprintf("v1\n%s\n%s\n%s\n%s",
		req.SubjectType, req.SubjectID, req.Title, orEmptyObject(req.Snapshot))
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

// orEmptyObject returns the JSON blob or "{}".
func orEmptyObject(raw types.JSON) []byte {
	if len(bytesTrimSpace(raw)) == 0 {
		return []byte("{}")
	}
	return raw
}

// bytesTrimSpace trims ASCII whitespace.
func bytesTrimSpace(raw []byte) []byte {
	start := 0
	for start < len(raw) && isASCIIWhitespace(raw[start]) {
		start++
	}
	end := len(raw)
	for end > start && isASCIIWhitespace(raw[end-1]) {
		end--
	}
	return raw[start:end]
}

// isASCIIWhitespace reports whether b is an ASCII whitespace byte.
func isASCIIWhitespace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}
