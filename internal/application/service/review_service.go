package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
)

// 审核流业务哨兵错误。handler 层据此映射 HTTP 语义（404/403/409/400），
// 与 tenant_member 等服务的错误风格一致。
var (
	// ErrReviewTenantMissing 请求上下文缺少租户（中间件层未注入）。
	ErrReviewTenantMissing = errors.New("review: tenant context is missing")
	// ErrReviewActorMissing 调用者缺少用户身份（审批人/提交人）。
	ErrReviewActorMissing = errors.New("review: caller identity is missing")
	// ErrReviewKnowledgeNotFound 目标知识不存在（或不在本租户）。
	ErrReviewKnowledgeNotFound = errors.New("review: knowledge not found")
	// ErrReviewKnowledgeNotReady 知识当前生命周期状态不允许提交审核
	// （例：已在 pending_review / 已被替代）。
	ErrReviewKnowledgeNotReady = errors.New("review: knowledge is not in a state that can be submitted for review")
	// ErrReviewTaskAlreadyPending 同一知识已有待审任务（提交幂等防重）。
	ErrReviewTaskAlreadyPending = errors.New("review: an approval task is already pending for this knowledge")
	// ErrReviewTaskNotFound 审批任务不存在（或不在本租户）。
	ErrReviewTaskNotFound = errors.New("review: approval task not found")
	// ErrReviewTaskNotPending 任务已不在待审状态（终态不可再表决）。
	ErrReviewTaskNotPending = errors.New("review: approval task is no longer pending")
	// ErrReviewSelfDecision 四眼校验失败：审批人就是提交人。
	ErrReviewSelfDecision = errors.New("review: the requester cannot decide their own submission")
	// ErrReviewInvalidDecision decision 不是 approved/rejected。
	ErrReviewInvalidDecision = errors.New("review: decision must be approved or rejected")
	// ErrReviewReasonRequired 驳回必须给出 reason_code。
	ErrReviewReasonRequired = errors.New("review: a reason code is required when rejecting")
	// ErrReviewInvalidReasonCode reason_code 不在白名单内。
	ErrReviewInvalidReasonCode = errors.New("review: unknown reason code")
	// ErrReviewReceiptNotFound 回执不存在（或不在本租户）。
	ErrReviewReceiptNotFound = errors.New("review: approval receipt not found")
	// ErrReviewBaselineChanged 基线已变：审批期间内容被改动，审批人看到的
	// 快照与当前内容不一致（KnowAxis V3.0 §7.5 差异再审）。调用方应选择
	// 继续/重审/撤回，而不是让"批 A 发 B"。
	ErrReviewBaselineChanged = errors.New("review: the review baseline changed since submission")
)

const (
	// reviewDefaultSLA 是未配置 sla_hours 时的默认审批时限（7 天）。
	reviewDefaultSLA = 7 * 24 * time.Hour
	// reviewTaskListLimit 是待办列表的默认条数（仓储侧还有硬上限）。
	reviewTaskListLimit = 100
	// reviewAuditCommentLimit 限制进入审计详情的评语长度，避免审计行膨胀。
	reviewAuditCommentLimit = 200
)

// reviewService implements interfaces.ReviewService（KnowAxis V3.0 §6 v1 子集）。
type reviewService struct {
	tasks      interfaces.ApprovalTaskRepository
	receipts   interfaces.ApprovalReceiptRepository
	flows      interfaces.ApprovalFlowRepository
	knowledges interfaces.KnowledgeRepository
	chunks     interfaces.ChunkRepository
	lifecycle  interfaces.KnowledgeLifecycleService
	audit      interfaces.AuditLogService
	now        func() time.Time
}

// NewReviewService constructs the production review service.
func NewReviewService(
	tasks interfaces.ApprovalTaskRepository,
	receipts interfaces.ApprovalReceiptRepository,
	flows interfaces.ApprovalFlowRepository,
	knowledges interfaces.KnowledgeRepository,
	chunks interfaces.ChunkRepository,
	lifecycle interfaces.KnowledgeLifecycleService,
	audit interfaces.AuditLogService,
) interfaces.ReviewService {
	return &reviewService{
		tasks:      tasks,
		receipts:   receipts,
		flows:      flows,
		knowledges: knowledges,
		chunks:     chunks,
		lifecycle:  lifecycle,
		audit:      audit,
		now:        time.Now,
	}
}

// SubmitReview 提交知识进入审核：
//  1. 校验知识生命周期允许 submit（CanTransitionTo pending_review）；
//  2. 幂等：同知识已有 pending 任务直接拒绝；
//  3. 冻结内容基线（content hash + chunk 数 + 标题 + custom_metadata 摘要）
//     到任务的 baseline JSONB 与 content_hash 列；
//  4. 风险路由 v1：密级 confidential→high、secret→mid、其余 low；KB flow
//     配置决定 single/dual（无配置或 enabled=false 默认 single）；
//  5. 创建任务、写审计、调用 lifecycle Transition(submit_review)。
func (s *reviewService) SubmitReview(ctx context.Context, knowledgeID, requesterID string) (*types.ApprovalTask, error) {
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		return nil, ErrReviewTenantMissing
	}
	if requesterID == "" {
		return nil, ErrReviewActorMissing
	}

	knowledge, err := s.knowledges.GetKnowledgeByID(ctx, tenantID, knowledgeID)
	if err != nil {
		if errors.Is(err, repository.ErrKnowledgeNotFound) {
			return nil, ErrReviewKnowledgeNotFound
		}
		return nil, err
	}

	// 状态机前置校验：只在"能合法转入 pending_review"时才允许提交。
	// 真正的落库仍由 lifecycle.Transition 复核（防并发窗口）。
	if !types.ParseKnowledgeLifecycleStatus(knowledge.LifecycleStatus).
		CanTransitionTo(types.KnowledgeLifecyclePendingReview) {
		return nil, ErrReviewKnowledgeNotReady
	}

	// 幂等：同一知识同时只允许一个待审任务。
	duplicate, err := s.tasks.HasActiveByKnowledgeID(ctx, tenantID, knowledgeID)
	if err != nil {
		return nil, err
	}
	if duplicate {
		return nil, ErrReviewTaskAlreadyPending
	}

	// KB 审批流配置。无配置 / enabled=false / 模式非法 → single 兜底。
	mode := types.ApprovalModeSingle
	var slaDue time.Time
	flow, err := s.flows.GetByKB(ctx, tenantID, knowledge.KnowledgeBaseID)
	switch {
	case err == nil:
		if flow.Enabled && flow.Mode == string(types.ApprovalModeDual) {
			mode = types.ApprovalModeDual
		}
		slaDue = s.now().Add(reviewSLAFromRules(flow.RiskRules))
	case errors.Is(err, repository.ErrApprovalFlowNotFound):
		slaDue = s.now().Add(reviewDefaultSLA)
	default:
		return nil, err
	}

	// 内容基线：text/faq chunk 内容摘要 + 行级字段一起算 content hash。
	chunks, err := s.chunks.ListChunksByKnowledgeIDAndTypes(ctx, tenantID, knowledgeID,
		[]types.ChunkType{types.ChunkTypeText, types.ChunkTypeFAQ})
	if err != nil {
		return nil, err
	}
	contentHash := reviewContentHash(knowledge, len(chunks), chunkContentDigest(chunks))
	baseline := types.ApprovalBaseline{
		ContentHash:           contentHash,
		ChunkCount:            len(chunks),
		Title:                 knowledge.Title,
		CustomMetadataSummary: knowledge.CustomMetadataText(),
		Classification:        knowledge.Classification,
		ParseStatus:           knowledge.ParseStatus,
	}
	baselineRaw, err := json.Marshal(baseline)
	if err != nil {
		return nil, fmt.Errorf("review: marshal baseline: %w", err)
	}

	risk := reviewRiskLevelFor(types.ParseKnowledgeClassification(knowledge.Classification))
	now := s.now()
	task := &types.ApprovalTask{
		ID:             uuid.NewString(),
		TenantID:       tenantID,
		KnowledgeID:    knowledge.ID,
		KBID:           knowledge.KnowledgeBaseID,
		Mode:           string(mode),
		Baseline:       types.JSON(baselineRaw),
		ContentHash:    contentHash,
		RiskLevel:      string(risk),
		Status:         string(types.ApprovalTaskStatusPending),
		AssigneeIDs:    types.JSON(`[]`), // v1 不做指派路由，保留空数组语义
		RequestedBy:    requesterID,
		DecidedHistory: types.JSON(`[]`),
		SLADue:         &slaDue,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.tasks.Create(ctx, task); err != nil {
		return nil, err
	}

	// 状态机推进失败 → 撤销刚建的任务，避免留下没有状态机支撑的孤儿待办。
	if _, err := s.lifecycle.Transition(ctx, &types.KnowledgeLifecycleTransitionInput{
		KnowledgeID: knowledgeID,
		Action:      types.KnowledgeLifecycleActionSubmitReview,
		ActorID:     requesterID,
	}); err != nil {
		task.Status = string(types.ApprovalTaskStatusCanceled)
		task.UpdatedAt = s.now()
		if updateErr := s.tasks.Update(ctx, task); updateErr != nil {
			// 撤销失败只留一条 pending 脏数据（可人工清理），不吞原始错误。
			_ = updateErr
		}
		return nil, fmt.Errorf("review: submit_review transition failed: %w", err)
	}

	// 审计：任务已受理但审批未决，outcome 用 accepted 而非 success。
	s.reviewAuditEvent(ctx, task, types.AuditActionReviewSubmitted, types.AuditOutcomeAccepted, map[string]any{
		"risk_level":   string(risk),
		"mode":         string(mode),
		"requested_by": requesterID,
		"content_hash": contentHash,
	})
	return task, nil
}

// Decide 审批人投一票。四眼校验后：
//   - rejected：任务立即终态，lifecycle reject_review 退回草稿；
//   - approved：票记入 decided_history；达到法定人数（dual 模式或 high 风险
//     需 2 个不同审批人；其余 1 票）才出回执（哈希链追加）并 lifecycle
//     publish（传 ReceiptID + ChangeNote）。未达人数则任务保持 pending。
func (s *reviewService) Decide(
	ctx context.Context,
	taskID, approverID string,
	decision types.ApprovalDecision,
	reasonCode types.ApprovalReasonCode,
	comment string,
) (*types.ApprovalTask, error) {
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		return nil, ErrReviewTenantMissing
	}
	if approverID == "" {
		return nil, ErrReviewActorMissing
	}
	if !decision.Valid() {
		return nil, ErrReviewInvalidDecision
	}
	if !reasonCode.Valid() {
		return nil, ErrReviewInvalidReasonCode
	}
	if decision == types.ApprovalDecisionRejected && reasonCode == "" {
		return nil, ErrReviewReasonRequired
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
	// 四眼原则：提交人不能审批自己（dual 模式的"2 个不同人"由去重计数保证）。
	if approverID == task.RequestedBy {
		return nil, ErrReviewSelfDecision
	}

	history, err := decodeApprovalDecisions(task.DecidedHistory)
	if err != nil {
		return nil, err
	}
	now := s.now()
	history = append(history, types.ApprovalDecisionRecord{
		By:       approverID,
		Decision: string(decision),
		At:       now,
	})
	historyRaw, err := json.Marshal(history)
	if err != nil {
		return nil, fmt.Errorf("review: marshal decided_history: %w", err)
	}
	task.DecidedHistory = types.JSON(historyRaw)
	task.ReasonCode = string(reasonCode)
	task.Comment = comment
	task.UpdatedAt = now

	if decision == types.ApprovalDecisionRejected {
		return s.rejectTask(ctx, task, approverID, now)
	}
	return s.approveTask(ctx, task, history, approverID, now)
}

// rejectTask 落 rejected 终态并推进 lifecycle reject_review（pending_review → draft）。
func (s *reviewService) rejectTask(
	ctx context.Context, task *types.ApprovalTask, approverID string, now time.Time,
) (*types.ApprovalTask, error) {
	task.Status = string(types.ApprovalTaskStatusRejected)
	task.DecidedBy = approverID
	task.DecisionAt = &now
	if err := s.tasks.Update(ctx, task); err != nil {
		return nil, err
	}

	s.reviewAuditEvent(ctx, task, types.AuditActionReviewRejected, types.AuditOutcomeSuccess, map[string]any{
		"approver_id": approverID,
		"reason_code": task.ReasonCode,
		"comment":     truncateReviewComment(task.Comment),
	})

	// 状态机推进放在任务落库之后：驳回是事实，退回草稿失败时任务与审计
	// 已留痕，可人工补推；反过来（先退回）失败则知识卡死在 pending_review。
	if _, err := s.lifecycle.Transition(ctx, &types.KnowledgeLifecycleTransitionInput{
		KnowledgeID: task.KnowledgeID,
		Action:      types.KnowledgeLifecycleActionRejectReview,
		ActorID:     approverID,
	}); err != nil {
		return nil, fmt.Errorf("review: reject_review transition failed: %w", err)
	}
	return task, nil
}

// approveTask 处理一张通过票：未达法定人数则记票保 pending；达标则落
// approved 终态、追加回执（哈希链）、推进 lifecycle publish。
func (s *reviewService) approveTask(
	ctx context.Context, task *types.ApprovalTask, history []types.ApprovalDecisionRecord, approverID string, now time.Time,
) (*types.ApprovalTask, error) {
	required := approvalQuorum(task)
	approvers := distinctApprovalApprovers(history)

	// 基线复核（§7.5 差异再审）：审批人批的是提交时冻结的那一版。若内容
	// 在审批期间被改动，先阻断——否则回执绑定的是旧 hash、发布的是新内容，
	// 四眼与凭证同时失效。中间票不做检查，避免把"内容还没改完"的正常在途
	// 审批卡住；只在即将发布的那一票上强制。
	if len(approvers) >= required {
		drifted, err := s.baselineDrifted(ctx, task)
		if err != nil {
			return nil, err
		}
		if drifted {
			s.reviewAuditEvent(ctx, task, types.AuditActionReviewRejected, types.AuditOutcomeFailed, map[string]any{
				"approver_id":      approverID,
				"baseline_changed": true,
				"expected_hash":    task.ContentHash,
			})
			return nil, ErrReviewBaselineChanged
		}
	}

	if len(approvers) < required {
		// 中间票：任务保持 pending，等待下一位审批人。
		if err := s.tasks.Update(ctx, task); err != nil {
			return nil, err
		}
		s.reviewAuditEvent(ctx, task, types.AuditActionReviewApproved, types.AuditOutcomeAccepted, map[string]any{
			"vote_recorded": true,
			"approvals":     len(approvers),
			"required":      required,
			"approver_id":   approverID,
		})
		return task, nil
	}

	// 达到法定人数：终态 approved。
	task.Status = string(types.ApprovalTaskStatusApproved)
	task.DecidedBy = approverID
	task.DecisionAt = &now

	// 回执先行：lifecycle publish 会把 ReceiptID 写进版本快照，必须先有
	// 回执可指向。Append 在仓储事务内读链尾并计算 row_hash。
	receipt := &types.ApprovalReceipt{
		ReceiptID:        newApprovalReceiptID(),
		TenantID:         task.TenantID,
		TaskID:           task.ID,
		KnowledgeID:      task.KnowledgeID,
		ContentHash:      task.ContentHash,
		BaselineSnapshot: task.Baseline,
		ApproverID:       approverID,
		Decision:         string(types.ApprovalDecisionApproved),
		ReasonCode:       task.ReasonCode,
		CreatedAt:        now,
	}
	if err := s.receipts.Append(ctx, receipt); err != nil {
		return nil, fmt.Errorf("review: append receipt: %w", err)
	}
	task.ReceiptID = receipt.ReceiptID
	if err := s.tasks.Update(ctx, task); err != nil {
		return nil, err
	}

	s.reviewAuditEvent(ctx, task, types.AuditActionReviewApproved, types.AuditOutcomeSuccess, map[string]any{
		"approvals":   len(approvers),
		"required":    required,
		"approver_id": approverID,
		"receipt_id":  receipt.ReceiptID,
		"risk_level":  task.RiskLevel,
		"mode":        task.Mode,
	})
	s.reviewAuditEvent(ctx, task, types.AuditActionReviewReceiptIssued, types.AuditOutcomeSuccess, map[string]any{
		"receipt_id": receipt.ReceiptID,
		"row_hash":   receipt.RowHash,
		"prev_hash":  receipt.PrevHash,
	})

	// 发布。失败时回执与任务已落库（链完整、审计留痕），知识仍在
	// pending_review，可排查后重试 publish；不回滚回执。
	if _, err := s.lifecycle.Transition(ctx, &types.KnowledgeLifecycleTransitionInput{
		KnowledgeID: task.KnowledgeID,
		Action:      types.KnowledgeLifecycleActionPublish,
		ActorID:     approverID,
		ChangeNote:  task.Comment,
		ReceiptID:   receipt.ReceiptID,
	}); err != nil {
		return nil, fmt.Errorf("review: publish transition failed: %w", err)
	}
	return task, nil
}

// ListPendingTasks v1：路由层已要求 Admin+，返回本租户全部 pending 任务。
// assigneeID 预留给按 assignee_ids 过滤的指派语义（v1 的 assignee_ids 恒空）。
func (s *reviewService) ListPendingTasks(ctx context.Context, assigneeID string) ([]*types.ApprovalTask, error) {
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		return nil, ErrReviewTenantMissing
	}
	_ = assigneeID
	return s.tasks.ListPending(ctx, tenantID, reviewTaskListLimit)
}

// GetTask 按任务 ID 取单条（租户隔离）。
func (s *reviewService) GetTask(ctx context.Context, taskID string) (*types.ApprovalTask, error) {
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		return nil, ErrReviewTenantMissing
	}
	task, err := s.tasks.GetByID(ctx, tenantID, taskID)
	if err != nil {
		if errors.Is(err, repository.ErrApprovalTaskNotFound) {
			return nil, ErrReviewTaskNotFound
		}
		return nil, err
	}
	return task, nil
}

// GetReceipt 按回执 ID 取单条（租户隔离）。
func (s *reviewService) GetReceipt(ctx context.Context, receiptID string) (*types.ApprovalReceipt, error) {
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		return nil, ErrReviewTenantMissing
	}
	receipt, err := s.receipts.Get(ctx, tenantID, receiptID)
	if err != nil {
		if errors.Is(err, repository.ErrApprovalReceiptNotFound) {
			return nil, ErrReviewReceiptNotFound
		}
		return nil, err
	}
	return receipt, nil
}

// GetReceiptChain 审计回放：按创建时间倒序返回回执链（从链尾向链首逐行
// 验证 prev_hash）。tenantID 为 0 时回退到 ctx 租户。
func (s *reviewService) GetReceiptChain(ctx context.Context, tenantID uint64, limit int) ([]*types.ApprovalReceipt, error) {
	if tenantID == 0 {
		fromCtx, ok := types.TenantIDFromContext(ctx)
		if !ok || fromCtx == 0 {
			return nil, ErrReviewTenantMissing
		}
		tenantID = fromCtx
	}
	receipts, err := s.receipts.List(ctx, tenantID, limit)
	if err != nil {
		return nil, err
	}
	return receipts, nil
}

// ============================================================================
// 内部助手
// ============================================================================

// baselineDrifted 重新计算当前内容指纹并与任务冻结的 ContentHash 比较
// （KnowAxis V3.0 §7.5 基线规则：发布前比对当前内容 hash 与审核基线）。
// 与提交时的计算口径（reviewContentHash）保持完全一致：同样的 chunk 类型
// 集合、同样的行级字段，否则会产生假阳性。
func (s *reviewService) baselineDrifted(ctx context.Context, task *types.ApprovalTask) (bool, error) {
	if task.ContentHash == "" {
		// 历史任务没有冻结 hash：无法比对，放行（不改变旧行为）。
		return false, nil
	}
	knowledge, err := s.knowledges.GetKnowledgeByID(ctx, task.TenantID, task.KnowledgeID)
	if err != nil {
		return false, err
	}
	chunks, err := s.chunks.ListChunksByKnowledgeIDAndTypes(ctx, task.TenantID, task.KnowledgeID,
		[]types.ChunkType{types.ChunkTypeText, types.ChunkTypeFAQ})
	if err != nil {
		return false, err
	}
	current := reviewContentHash(knowledge, len(chunks), chunkContentDigest(chunks))
	return current != task.ContentHash, nil
}

// reviewRiskLevelFor 是 v1 的风险路由：密级 → 审批风险级。confidential 一票
// 不放行（走 dual），secret 居中，其余 low。（委员会多角色会签为后续版本预留。）
func reviewRiskLevelFor(classification types.KnowledgeClassification) types.ApprovalRiskLevel {
	switch classification {
	case types.KnowledgeClassificationConfidential:
		return types.ApprovalRiskHigh
	case types.KnowledgeClassificationSecret:
		return types.ApprovalRiskMid
	default:
		return types.ApprovalRiskLow
	}
}

// reviewSLAFromRules 从 flow.risk_rules 读 {"sla_hours": n}（1..720 钳制），
// 未配置或非法时回退默认 7 天。
func reviewSLAFromRules(rules types.JSON) time.Duration {
	if len(rules) == 0 {
		return reviewDefaultSLA
	}
	var parsed map[string]any
	if err := json.Unmarshal(rules, &parsed); err != nil {
		return reviewDefaultSLA
	}
	hours, ok := parsed["sla_hours"].(float64)
	if !ok || hours < 1 || hours > 720 {
		return reviewDefaultSLA
	}
	return time.Duration(hours * float64(time.Hour))
}

// reviewContentHash 计算知识当前内容指纹：行级字段 + chunk 内容摘要一起进
// sha256。提交时冻结进任务，决策时可对照"内容是否中途变过"。
func reviewContentHash(knowledge *types.Knowledge, chunkCount int, chunkDigest string) string {
	canonical := strings.Join([]string{
		"v1",
		knowledge.ID,
		knowledge.Title,
		knowledge.FileHash,
		strconv.Itoa(chunkCount),
		chunkDigest,
	}, "\n")
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

// chunkContentDigest 对 chunk 内容序列做 sha256：长度前缀分帧，避免相邻
// 内容拼接产生歧义（"ab"+"c" 与 "a"+"bc"）。
func chunkContentDigest(chunks []*types.Chunk) string {
	h := sha256.New()
	for _, chunk := range chunks {
		content := ""
		if chunk != nil {
			content = chunk.Content
		}
		fmt.Fprintf(h, "%d:", len(content))
		h.Write([]byte(content))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// approvalQuorum 计算任务法定审批人数：dual 模式需要 2 人；high 风险在 v1
// 一律按 dual 处理（委员会多角色会签为后续版本预留）；其余（single 流的
// low/mid）1 票即通过。
func approvalQuorum(task *types.ApprovalTask) int {
	if task.Mode == string(types.ApprovalModeDual) ||
		task.RiskLevel == string(types.ApprovalRiskHigh) {
		return 2
	}
	return 1
}

// distinctApprovalApprovers 返回历史中投过 approved 票的去重审批人列表。
// 同一人重复投票不重复计数，这就是 dual 模式"2 个不同人"的保证。
func distinctApprovalApprovers(history []types.ApprovalDecisionRecord) []string {
	seen := make(map[string]struct{}, len(history))
	approvals := make([]string, 0, len(history))
	for _, record := range history {
		if record.Decision != string(types.ApprovalDecisionApproved) || record.By == "" {
			continue
		}
		if _, voted := seen[record.By]; voted {
			continue
		}
		seen[record.By] = struct{}{}
		approvals = append(approvals, record.By)
	}
	return approvals
}

// decodeApprovalDecisions 解析任务上的 decided_history；空值按空历史处理。
func decodeApprovalDecisions(raw types.JSON) ([]types.ApprovalDecisionRecord, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var history []types.ApprovalDecisionRecord
	if err := json.Unmarshal(raw, &history); err != nil {
		return nil, fmt.Errorf("review: decode decided_history: %w", err)
	}
	return history, nil
}

// newApprovalReceiptID 生成回执 ID：rcpt_ + 去连字符 uuid（5+32=37 字符，
// 列宽 varchar(64) 内）。
func newApprovalReceiptID() string {
	return "rcpt_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}

// truncateReviewComment 限制进入审计详情的评语长度。
func truncateReviewComment(comment string) string {
	runes := []rune(comment)
	if len(runes) <= reviewAuditCommentLimit {
		return comment
	}
	return string(runes[:reviewAuditCommentLimit]) + "…"
}

// reviewAuditEvent 把审核事件写进租户/KB 活动审计流（best-effort：审计
// 故障不回滚业务，与 recordKBActivity 的失败语义一致）。
func (s *reviewService) reviewAuditEvent(
	ctx context.Context,
	task *types.ApprovalTask,
	action types.AuditAction,
	outcome types.AuditOutcome,
	details map[string]any,
) {
	recordKBActivity(ctx, s.audit, task.TenantID, task.KBID, action,
		"approval_task", task.ID, outcome, details)
}
