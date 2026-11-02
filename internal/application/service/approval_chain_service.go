package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// 审批链引擎的业务哨兵错误。
var (
	// ErrChainNodeNotFound 节点不存在（或不属于该任务）。
	ErrChainNodeNotFound = errors.New("approval chain: node not found")
	// ErrChainNodeNotActive 节点尚未激活或已终结，不能签。
	ErrChainNodeNotActive = errors.New("approval chain: node is not accepting votes")
	// ErrChainNotAssignee 投票人不在该节点的审批人集合内（且无有效委托）。
	ErrChainNotAssignee = errors.New("approval chain: the approver is not assigned to this node")
	// ErrChainAlreadyVoted 同一人已在该节点投过票。
	ErrChainAlreadyVoted = errors.New("approval chain: this approver already voted on the node")
	// ErrChainInvalidDelegation 委托参数非法（自委托/非活跃成员等）。
	ErrChainInvalidDelegation = errors.New("approval chain: invalid delegation")
	// ErrChainDuplicateApprover 加签的目标已在节点内。
	ErrChainDuplicateApprover = errors.New("approval chain: the approver is already on this node")
)

// approvalChainService implements interfaces.ApprovalChainService.
//
// Design notes that the spec pins down and that are easy to get wrong:
//
//   - 或签 (or) 通过 = 集合内任一票通过；会签 (and) 通过 = 集合内每一票都
//     通过，所以 RequiredCount 参与判定，加签会把它 +1。
//   - 逐级 (sequential) 不单独建模：它就是"节点按 seq 推进"，前一节点通过
//     后下一节点才从 pending 变 active。
//   - 四眼原则在链上依然成立：同一人不能在自己的提交上投票（由调用方
//     校验），且同一节点内同一人只有一票。
//   - 委托不等于换人：票记在代理人名下，但 DelegateOf 保留被代理人，
//     审计能回答"这一票是谁的授权"。
type approvalChainService struct {
	repo  interfaces.ApprovalChainRepository
	nodes interfaces.ApprovalFlowRepository
	// members resolves role-based assignees at instantiation time.
	members interfaces.TenantMemberRepository
	now     func() time.Time
}

// NewApprovalChainService constructs the chain engine.
func NewApprovalChainService(
	repo interfaces.ApprovalChainRepository,
	nodes interfaces.ApprovalFlowRepository,
	members interfaces.TenantMemberRepository,
) interfaces.ApprovalChainService {
	return &approvalChainService{repo: repo, nodes: nodes, members: members, now: time.Now}
}

// Instantiate builds the in-flight chain for a submitted task.
//
// A space with no node definitions still gets a chain: one implicit node whose
// required count is the legacy quorum. That keeps single/dual approval working
// for every tenant that never configures a flow, and makes the vote path uniform
// (there is always exactly one active node to reason about).
func (s *approvalChainService) Instantiate(
	ctx context.Context, task *types.ApprovalTask, resolution *interfaces.ApprovalResolution,
) ([]*types.ApprovalTaskNode, error) {
	if task == nil || task.ID == "" {
		return nil, apperrors.NewBadRequestError("task is required")
	}
	quorum := 1
	if resolution != nil && resolution.Quorum > 0 {
		quorum = resolution.Quorum
	}

	defs, err := s.flowDefinitions(ctx, task)
	if err != nil {
		return nil, err
	}
	nodes := make([]*types.ApprovalTaskNode, 0, len(defs))
	if len(defs) == 0 {
		// 隐式单节点：等价于旧的"攒够 N 票"语义。
		approvers, err := s.roleApprovers(ctx, task.TenantID, string(types.TenantRoleAdmin))
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, &types.ApprovalTaskNode{
			ID:            uuid.NewString(),
			TenantID:      task.TenantID,
			TaskID:        task.ID,
			Seq:           1,
			Name:          "审批",
			Mode:          string(types.ApprovalNodeModeOr),
			ApproverIDs:   marshalStringList(approvers),
			Status:        string(types.ApprovalNodeStatusActive),
			RequiredCount: quorum,
			CreatedAt:     s.now(),
		})
		if err := s.repo.CreateTaskNodes(ctx, task.TenantID, nodes); err != nil {
			return nil, err
		}
		return nodes, nil
	}

	for _, def := range defs {
		if def == nil {
			continue
		}
		approvers, err := s.resolveAssignees(ctx, task.TenantID, def)
		if err != nil {
			return nil, err
		}
		mode := types.ApprovalNodeMode(def.Mode)
		if !mode.Valid() {
			mode = types.ApprovalNodeModeOr
		}
		required := 1
		if mode == types.ApprovalNodeModeAnd {
			required = len(approvers)
		}
		status := types.ApprovalNodeStatusPending
		if len(nodes) == 0 {
			status = types.ApprovalNodeStatusActive
		}
		nodes = append(nodes, &types.ApprovalTaskNode{
			ID:            uuid.NewString(),
			TenantID:      task.TenantID,
			TaskID:        task.ID,
			Seq:           def.Seq,
			Name:          def.Name,
			Mode:          string(mode),
			ApproverIDs:   marshalStringList(approvers),
			Status:        string(status),
			RequiredCount: required,
			CreatedAt:     s.now(),
		})
	}
	// 首个节点没有任何审批人时，跳过它而不是让任务永远卡住。
	if first := nodes[0]; first != nil && len(first.ApproverIDsList()) == 0 {
		first.Status = string(types.ApprovalNodeStatusSkipped)
		first.DecidedAt = chainTimePtr(s.now())
	}
	if err := s.repo.CreateTaskNodes(ctx, task.TenantID, nodes); err != nil {
		return nil, err
	}
	return nodes, nil
}

// flowDefinitions loads a task's flow node definitions.
func (s *approvalChainService) flowDefinitions(
	ctx context.Context, task *types.ApprovalTask,
) ([]*types.ApprovalFlowNode, error) {
	if s.nodes == nil || task.KBID == "" {
		return nil, nil
	}
	flow, err := s.nodes.GetByKB(ctx, task.TenantID, task.KBID)
	if err != nil {
		// 无配置是合法状态：回落到隐式节点。
		return nil, nil
	}
	if flow == nil || !flow.Enabled {
		return nil, nil
	}
	return s.repo.ListFlowNodes(ctx, task.TenantID, flow.ID)
}

// resolveAssignees turns a node definition's assignee config into user IDs.
func (s *approvalChainService) resolveAssignees(
	ctx context.Context, tenantID uint64, def *types.ApprovalFlowNode,
) ([]string, error) {
	switch def.AssigneeKind {
	case "users":
		var ids []string
		if len(def.AssigneeIDs) > 0 {
			_ = json.Unmarshal(def.AssigneeIDs, &ids)
		}
		return chainDedupeStrings(ids), nil
	default:
		return s.roleApprovers(ctx, tenantID, def.AssigneeRole)
	}
}

// roleApprovers lists the tenant members holding a role.
func (s *approvalChainService) roleApprovers(
	ctx context.Context, tenantID uint64, role string,
) ([]string, error) {
	if s.members == nil || role == "" {
		return nil, nil
	}
	members, err := s.members.ListByTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	want := types.TenantRole(role)
	out := make([]string, 0, len(members))
	for _, member := range members {
		if member == nil || member.UserID == "" {
			continue
		}
		if member.Role.HasPermission(want) {
			out = append(out, member.UserID)
		}
	}
	return chainDedupeStrings(out), nil
}

// State returns the current chain state.
func (s *approvalChainService) State(
	ctx context.Context, tenantID uint64, taskID string,
) (*types.ApprovalChainState, error) {
	if taskID == "" {
		return nil, apperrors.NewBadRequestError("task_id is required")
	}
	nodes, err := s.repo.ListTaskNodes(ctx, tenantID, taskID)
	if err != nil {
		return nil, err
	}
	votes, err := s.repo.ListVotes(ctx, tenantID, taskID)
	if err != nil {
		return nil, err
	}
	return buildChainState(taskID, nodes, votes), nil
}

// Vote records one decision and advances the chain.
func (s *approvalChainService) Vote(
	ctx context.Context, task *types.ApprovalTask, approverID, decision, comment string,
) (*types.ApprovalChainState, error) {
	if task == nil || task.ID == "" {
		return nil, apperrors.NewBadRequestError("task is required")
	}
	if approverID == "" {
		return nil, apperrors.NewUnauthorizedError("approver identity is required")
	}
	if decision != string(types.ApprovalDecisionApproved) &&
		decision != string(types.ApprovalDecisionRejected) {
		return nil, apperrors.NewBadRequestError("decision must be approved or rejected")
	}

	nodes, err := s.repo.ListTaskNodes(ctx, task.TenantID, task.ID)
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		// 历史任务（多级链上线前提交）没有节点行：交给调用方的旧攒票路径。
		node, err := s.ensureLegacyNode(ctx, task)
		if err != nil {
			return nil, err
		}
		nodes = []*types.ApprovalTaskNode{node}
	}

	active := firstActiveNode(nodes)
	if active == nil {
		return nil, ErrChainNodeNotActive
	}
	votes, err := s.repo.ListVotes(ctx, task.TenantID, task.ID)
	if err != nil {
		return nil, err
	}

	// 委托解析：投票人本身不在名单内，但持有被代理人的有效委托时，按被
	// 代理人身份记票。票仍记在代理人名下，DelegateOf 保留授权来源。
	delegateOf := ""
	if !chainContains(active.ApproverIDsList(), approverID) {
		delegator, derr := s.delegatorFor(ctx, task.TenantID, active, approverID)
		if derr != nil {
			return nil, derr
		}
		if delegator == "" {
			return nil, ErrChainNotAssignee
		}
		delegateOf = delegator
	}
	for _, vote := range votes {
		if vote == nil || vote.NodeID != active.ID {
			continue
		}
		if vote.ApproverID == approverID || (delegateOf != "" && vote.DelegateOf == delegateOf) {
			return nil, ErrChainAlreadyVoted
		}
	}

	if err := s.repo.AppendVote(ctx, &types.ApprovalVote{
		TenantID:   task.TenantID,
		TaskID:     task.ID,
		NodeID:     active.ID,
		ApproverID: approverID,
		DelegateOf: delegateOf,
		Decision:   decision,
		Comment:    comment,
		CreatedAt:  s.now(),
	}); err != nil {
		return nil, err
	}

	votes = append(votes, &types.ApprovalVote{
		NodeID: active.ID, ApproverID: approverID, DelegateOf: delegateOf, Decision: decision,
	})
	state := s.advance(ctx, task, nodes, votes, active, decision)
	return state, nil
}

// advance applies the vote outcome to the node and promotes the next one.
func (s *approvalChainService) advance(
	ctx context.Context, task *types.ApprovalTask, nodes []*types.ApprovalTaskNode,
	votes []*types.ApprovalVote, active *types.ApprovalTaskNode, decision string,
) *types.ApprovalChainState {
	if decision == string(types.ApprovalDecisionRejected) {
		// 任一节点驳回即整链驳回（§6.3 驳回必须记录原因码，由调用方处理）。
		active.Status = string(types.ApprovalNodeStatusRejected)
		active.DecidedAt = chainTimePtr(s.now())
		if err := s.repo.UpdateTaskNode(ctx, active); err != nil {
			// 状态写失败时不谎报：后续 State 会重新算出真实状态。
			return buildChainState(task.ID, nodes, votes)
		}
		return buildChainState(task.ID, nodes, votes)
	}

	approvedAtNode := countNodeApprovals(votes, active.ID)
	if approvedAtNode >= active.RequiredCount {
		active.Status = string(types.ApprovalNodeStatusApproved)
		active.DecidedAt = chainTimePtr(s.now())
		if err := s.repo.UpdateTaskNode(ctx, active); err != nil {
			return buildChainState(task.ID, nodes, votes)
		}
		s.promoteNext(ctx, nodes, active.Seq)
	}
	return buildChainState(task.ID, nodes, votes)
}

// promoteNext activates the next pending node, skipping nodes with no approvers
// so an unstaffed stage cannot block the chain forever.
func (s *approvalChainService) promoteNext(
	ctx context.Context, nodes []*types.ApprovalTaskNode, afterSeq int,
) {
	for _, node := range nodes {
		if node == nil || node.Seq <= afterSeq ||
			node.Status != string(types.ApprovalNodeStatusPending) {
			continue
		}
		if len(node.ApproverIDsList()) == 0 {
			node.Status = string(types.ApprovalNodeStatusSkipped)
			node.DecidedAt = chainTimePtr(s.now())
			if err := s.repo.UpdateTaskNode(ctx, node); err != nil {
				return
			}
			continue
		}
		node.Status = string(types.ApprovalNodeStatusActive)
		_ = s.repo.UpdateTaskNode(ctx, node)
		return
	}
}

// delegatorFor finds a delegator on the node whose authority approverID holds.
func (s *approvalChainService) delegatorFor(
	ctx context.Context, tenantID uint64, node *types.ApprovalTaskNode, approverID string,
) (string, error) {
	live, err := s.repo.ListActiveDelegationsFor(ctx, tenantID, approverID, s.now())
	if err != nil {
		return "", err
	}
	assigned := node.ApproverIDsList()
	for _, delegation := range live {
		if delegation == nil || !delegation.Active(s.now()) {
			continue
		}
		if chainContains(assigned, delegation.DelegatorID) {
			return delegation.DelegatorID, nil
		}
	}
	return "", nil
}

// ensureLegacyNode gives a pre-chain task a single implicit node.
func (s *approvalChainService) ensureLegacyNode(
	ctx context.Context, task *types.ApprovalTask,
) (*types.ApprovalTaskNode, error) {
	quorum := 1
	if task.Mode == string(types.ApprovalModeDual) || task.RiskLevel == string(types.ApprovalRiskHigh) {
		quorum = 2
	}
	node := &types.ApprovalTaskNode{
		ID:            uuid.NewString(),
		TenantID:      task.TenantID,
		TaskID:        task.ID,
		Seq:           1,
		Name:          "审批",
		Mode:          string(types.ApprovalNodeModeOr),
		ApproverIDs:   types.JSON("[]"),
		Status:        string(types.ApprovalNodeStatusActive),
		RequiredCount: quorum,
		CreatedAt:     s.now(),
	}
	if err := s.repo.CreateTaskNodes(ctx, task.TenantID, []*types.ApprovalTaskNode{node}); err != nil {
		return nil, err
	}
	return node, nil
}

// AddApprover appends an approver to one node (加签).
//
// 加签对会签节点同时抬高 RequiredCount：否则"需要 2 人全签"的节点加了一个
// 人之后仍然只要 2 票，新加的人实际可以被忽略。
func (s *approvalChainService) AddApprover(
	ctx context.Context, tenantID uint64, taskID, nodeID, approverID, actorID string,
) (*types.ApprovalChainState, error) {
	if approverID == "" {
		return nil, apperrors.NewBadRequestError("approver_id is required")
	}
	node, err := s.loadMutableNode(ctx, tenantID, taskID, nodeID)
	if err != nil {
		return nil, err
	}
	approvers := node.ApproverIDsList()
	if chainContains(approvers, approverID) {
		return nil, ErrChainDuplicateApprover
	}
	approvers = append(approvers, approverID)
	node.ApproverIDs = marshalStringList(approvers)
	if node.Mode == string(types.ApprovalNodeModeAnd) {
		node.RequiredCount = len(approvers)
	}
	if err := s.repo.UpdateTaskNode(ctx, node); err != nil {
		return nil, err
	}
	return s.State(ctx, tenantID, taskID)
}

// TransferApprover replaces one approver on a node (转签).
//
// 转签只在节点仍可签时允许：已终结节点上的转签改不了任何历史票，只会让
// 审批记录与实际责任人脱节。
func (s *approvalChainService) TransferApprover(
	ctx context.Context, tenantID uint64, taskID, nodeID, fromID, toID, actorID string,
) (*types.ApprovalChainState, error) {
	if fromID == "" || toID == "" {
		return nil, apperrors.NewBadRequestError("from_id and to_id are required")
	}
	if fromID == toID {
		return nil, apperrors.NewBadRequestError("cannot transfer to the same approver")
	}
	node, err := s.loadMutableNode(ctx, tenantID, taskID, nodeID)
	if err != nil {
		return nil, err
	}
	approvers := node.ApproverIDsList()
	replaced := false
	for i, id := range approvers {
		if id == fromID {
			approvers[i] = toID
			replaced = true
			break
		}
	}
	if !replaced {
		return nil, ErrChainNotAssignee
	}
	node.ApproverIDs = marshalStringList(chainDedupeStrings(approvers))
	if err := s.repo.UpdateTaskNode(ctx, node); err != nil {
		return nil, err
	}
	return s.State(ctx, tenantID, taskID)
}

// loadMutableNode loads a node and rejects terminal states.
func (s *approvalChainService) loadMutableNode(
	ctx context.Context, tenantID uint64, taskID, nodeID string,
) (*types.ApprovalTaskNode, error) {
	if taskID == "" || nodeID == "" {
		return nil, apperrors.NewBadRequestError("task_id and node_id are required")
	}
	nodes, err := s.repo.ListTaskNodes(ctx, tenantID, taskID)
	if err != nil {
		return nil, err
	}
	for _, node := range nodes {
		if node == nil || node.ID != nodeID {
			continue
		}
		switch types.ApprovalNodeStatus(node.Status) {
		case types.ApprovalNodeStatusApproved, types.ApprovalNodeStatusRejected,
			types.ApprovalNodeStatusSkipped:
			return nil, ErrChainNodeNotActive
		}
		return node, nil
	}
	return nil, ErrChainNodeNotFound
}

// Delegate creates a delegation (休假代理).
func (s *approvalChainService) Delegate(
	ctx context.Context, tenantID uint64, delegatorID, delegateID, reason string, expiresAt *time.Time,
) (*types.ApprovalDelegation, error) {
	if delegatorID == "" || delegateID == "" {
		return nil, apperrors.NewBadRequestError("delegator_id and delegate_id are required")
	}
	if delegatorID == delegateID {
		return nil, ErrChainInvalidDelegation
	}
	now := s.now()
	if expiresAt != nil && !expiresAt.After(now) {
		return nil, apperrors.NewBadRequestError("expires_at must be in the future")
	}
	delegation := &types.ApprovalDelegation{
		ID:          uuid.NewString(),
		TenantID:    tenantID,
		DelegatorID: delegatorID,
		DelegateID:  delegateID,
		StartsAt:    now,
		ExpiresAt:   expiresAt,
		Reason:      reason,
		CreatedAt:   now,
	}
	if err := s.repo.CreateDelegation(ctx, delegation); err != nil {
		return nil, err
	}
	return delegation, nil
}

// RevokeDelegation ends a delegation early.
func (s *approvalChainService) RevokeDelegation(
	ctx context.Context, tenantID uint64, delegationID, actorID string,
) error {
	if delegationID == "" {
		return apperrors.NewBadRequestError("delegation_id is required")
	}
	return s.repo.RevokeDelegation(ctx, tenantID, delegationID, s.now())
}

// ListDelegations returns the live delegations a delegate holds.
func (s *approvalChainService) ListDelegations(
	ctx context.Context, tenantID uint64, delegateID string,
) ([]*types.ApprovalDelegation, error) {
	return s.repo.ListActiveDelegationsFor(ctx, tenantID, delegateID, s.now())
}

// ============================================================================
// 纯函数助手（无 IO，便于单测覆盖判定逻辑）
// ============================================================================

// buildChainState folds nodes + votes into the API view.
func buildChainState(
	taskID string, nodes []*types.ApprovalTaskNode, votes []*types.ApprovalVote,
) *types.ApprovalChainState {
	state := &types.ApprovalChainState{TaskID: taskID, Nodes: nodes, Votes: votes}
	rejected := false
	allDone := len(nodes) > 0
	for _, node := range nodes {
		if node == nil {
			continue
		}
		switch types.ApprovalNodeStatus(node.Status) {
		case types.ApprovalNodeStatusRejected:
			rejected = true
		case types.ApprovalNodeStatusApproved, types.ApprovalNodeStatusSkipped:
			// terminal-ok
		default:
			allDone = false
			if state.ActiveNodeSeq == 0 {
				state.ActiveNodeSeq = node.Seq
			}
		}
	}
	if rejected {
		state.Finished = true
		state.Approved = false
		state.ActiveNodeSeq = 0
		return state
	}
	if allDone {
		state.Finished = true
		state.Approved = true
		state.ActiveNodeSeq = 0
	}
	return state
}

// firstActiveNode returns the lowest-seq active node.
func firstActiveNode(nodes []*types.ApprovalTaskNode) *types.ApprovalTaskNode {
	var active *types.ApprovalTaskNode
	for _, node := range nodes {
		if node == nil || node.Status != string(types.ApprovalNodeStatusActive) {
			continue
		}
		if active == nil || node.Seq < active.Seq {
			active = node
		}
	}
	return active
}

// countNodeApprovals counts distinct approvers who approved on one node.
// 委托票按被代理人去重：同一份授权不能被代理人投两次。
func countNodeApprovals(votes []*types.ApprovalVote, nodeID string) int {
	seen := make(map[string]bool, len(votes))
	for _, vote := range votes {
		if vote == nil || vote.NodeID != nodeID ||
			vote.Decision != string(types.ApprovalDecisionApproved) {
			continue
		}
		identity := vote.ApproverID
		if vote.DelegateOf != "" {
			identity = vote.DelegateOf
		}
		seen[identity] = true
	}
	return len(seen)
}

// marshalStringList renders a user-ID list as JSON without failing the caller.
func marshalStringList(ids []string) types.JSON {
	if len(ids) == 0 {
		return types.JSON("[]")
	}
	raw, err := json.Marshal(chainDedupeStrings(ids))
	if err != nil {
		return types.JSON("[]")
	}
	return types.JSON(raw)
}

// dedupeStrings removes empties and duplicates, preserving order.
func chainDedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, value := range in {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

// chainContains reports membership.
func chainContains(list []string, want string) bool {
	for _, value := range list {
		if value == want {
			return true
		}
	}
	return false
}

// timePtr returns a pointer to t.
func chainTimePtr(t time.Time) *time.Time { return &t }
