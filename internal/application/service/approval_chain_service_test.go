package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// stubChainRepo is an in-memory chain store.
type stubChainRepo struct {
	interfaces.ApprovalChainRepository
	nodeDefs    []*types.ApprovalFlowNode
	taskNodes   []*types.ApprovalTaskNode
	votes       []*types.ApprovalVote
	delegations []*types.ApprovalDelegation
}

func (s *stubChainRepo) ReplaceFlowNodes(
	_ context.Context, _ uint64, _ string, nodes []*types.ApprovalFlowNode,
) error {
	s.nodeDefs = nodes
	return nil
}

func (s *stubChainRepo) ListFlowNodes(
	_ context.Context, _ uint64, _ string,
) ([]*types.ApprovalFlowNode, error) {
	return s.nodeDefs, nil
}

func (s *stubChainRepo) CreateTaskNodes(
	_ context.Context, _ uint64, nodes []*types.ApprovalTaskNode,
) error {
	s.taskNodes = append(s.taskNodes, nodes...)
	return nil
}

func (s *stubChainRepo) ListTaskNodes(
	_ context.Context, _ uint64, taskID string,
) ([]*types.ApprovalTaskNode, error) {
	out := make([]*types.ApprovalTaskNode, 0, len(s.taskNodes))
	for _, node := range s.taskNodes {
		if node.TaskID == taskID {
			out = append(out, node)
		}
	}
	return out, nil
}

func (s *stubChainRepo) UpdateTaskNode(_ context.Context, node *types.ApprovalTaskNode) error {
	for i, existing := range s.taskNodes {
		if existing.ID == node.ID {
			s.taskNodes[i] = node
			return nil
		}
	}
	return ErrChainNodeNotFound
}

func (s *stubChainRepo) AppendVote(_ context.Context, vote *types.ApprovalVote) error {
	s.votes = append(s.votes, vote)
	return nil
}

func (s *stubChainRepo) ListVotes(
	_ context.Context, _ uint64, taskID string,
) ([]*types.ApprovalVote, error) {
	out := make([]*types.ApprovalVote, 0, len(s.votes))
	for _, vote := range s.votes {
		if vote.TaskID == taskID {
			out = append(out, vote)
		}
	}
	return out, nil
}

func (s *stubChainRepo) CreateDelegation(
	_ context.Context, delegation *types.ApprovalDelegation,
) error {
	s.delegations = append(s.delegations, delegation)
	return nil
}

func (s *stubChainRepo) RevokeDelegation(
	_ context.Context, _ uint64, delegationID string, at time.Time,
) error {
	for _, delegation := range s.delegations {
		if delegation.ID == delegationID {
			delegation.RevokedAt = &at
		}
	}
	return nil
}

func (s *stubChainRepo) ListActiveDelegationsFor(
	_ context.Context, _ uint64, delegateID string, now time.Time,
) ([]*types.ApprovalDelegation, error) {
	out := make([]*types.ApprovalDelegation, 0, len(s.delegations))
	for _, delegation := range s.delegations {
		if delegation.DelegateID == delegateID && delegation.Active(now) {
			out = append(out, delegation)
		}
	}
	return out, nil
}

func (s *stubChainRepo) DelegationFor(
	_ context.Context, _ uint64, delegatorID, delegateID string, now time.Time,
) (*types.ApprovalDelegation, error) {
	for _, delegation := range s.delegations {
		if delegation.DelegatorID == delegatorID && delegation.DelegateID == delegateID &&
			delegation.Active(now) {
			return delegation, nil
		}
	}
	return nil, nil
}

// stubFlowRepo drives flowDefinitions.
type stubChainFlowRepo struct {
	interfaces.ApprovalFlowRepository
	flow *types.ApprovalFlow
}

func (s *stubChainFlowRepo) GetByKB(
	_ context.Context, _ uint64, _ string,
) (*types.ApprovalFlow, error) {
	if s.flow == nil {
		return nil, ErrChainNodeNotFound
	}
	return s.flow, nil
}

// stubMembers returns a fixed membership list.
type stubChainMembers struct {
	interfaces.TenantMemberRepository
	members []*types.TenantMember
}

func (s *stubChainMembers) ListByTenant(
	_ context.Context, _ uint64,
) ([]*types.TenantMember, error) {
	return s.members, nil
}

func newChainService(repo *stubChainRepo) *approvalChainService {
	return &approvalChainService{
		repo: repo,
		now:  func() time.Time { return time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC) },
	}
}

func chainTask() *types.ApprovalTask {
	return &types.ApprovalTask{ID: "task-1", TenantID: 7, KBID: "kb-1", RiskLevel: "low"}
}

func approverList(t *testing.T, node *types.ApprovalTaskNode) []string {
	t.Helper()
	var ids []string
	require.NoError(t, json.Unmarshal(node.ApproverIDs, &ids))
	return ids
}

// TestInstantiateImplicitNodeKeepsLegacyQuorum covers the fallback: a space with
// no configured chain still gets one node whose required count is the old
// quorum, so 单审/双审 behaviour is unchanged.
func TestInstantiateImplicitNodeKeepsLegacyQuorum(t *testing.T) {
	repo := &stubChainRepo{}
	svc := newChainService(repo)

	nodes, err := svc.Instantiate(context.Background(), chainTask(),
		&interfaces.ApprovalResolution{Quorum: 2})
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	assert.Equal(t, 2, nodes[0].RequiredCount, "dual approval keeps needing two votes")
	assert.Equal(t, string(types.ApprovalNodeStatusActive), nodes[0].Status)
}

// TestInstantiateSequentialChainActivatesOnlyFirstNode is 逐级审批: later stages
// must not accept votes before earlier ones clear.
func TestInstantiateSequentialChainActivatesOnlyFirstNode(t *testing.T) {
	repo := &stubChainRepo{nodeDefs: []*types.ApprovalFlowNode{
		{Seq: 1, Name: "领域 Owner", Mode: string(types.ApprovalNodeModeOr), AssigneeKind: "users",
			AssigneeIDs: types.JSON(`["owner-1"]`)},
		{Seq: 2, Name: "质量", Mode: string(types.ApprovalNodeModeAnd), AssigneeKind: "users",
			AssigneeIDs: types.JSON(`["qa-1","qa-2"]`)},
		{Seq: 3, Name: "法务", Mode: string(types.ApprovalNodeModeOr), AssigneeKind: "users",
			AssigneeIDs: types.JSON(`["legal-1"]`)},
	}}
	svc := newChainService(repo)
	svc.nodes = &stubChainFlowRepo{flow: &types.ApprovalFlow{ID: "flow-1", Enabled: true}}

	nodes, err := svc.Instantiate(context.Background(), chainTask(), &interfaces.ApprovalResolution{})
	require.NoError(t, err)
	require.Len(t, nodes, 3)

	assert.Equal(t, string(types.ApprovalNodeStatusActive), nodes[0].Status)
	assert.Equal(t, string(types.ApprovalNodeStatusPending), nodes[1].Status)
	assert.Equal(t, string(types.ApprovalNodeStatusPending), nodes[2].Status)
	// 会签节点需要全员签名。
	assert.Equal(t, 2, nodes[1].RequiredCount)
}

// TestOrSignRequiresOneVote covers 或签.
func TestOrSignRequiresOneVote(t *testing.T) {
	repo := &stubChainRepo{nodeDefs: []*types.ApprovalFlowNode{
		{Seq: 1, Mode: string(types.ApprovalNodeModeOr), AssigneeKind: "users",
			AssigneeIDs: types.JSON(`["a","b","c"]`)},
	}}
	svc := newChainService(repo)
	svc.nodes = &stubChainFlowRepo{flow: &types.ApprovalFlow{ID: "flow-1", Enabled: true}}
	ctx := context.Background()
	task := chainTask()
	_, err := svc.Instantiate(ctx, task, &interfaces.ApprovalResolution{})
	require.NoError(t, err)

	state, err := svc.Vote(ctx, task, "a", string(types.ApprovalDecisionApproved), "ok")
	require.NoError(t, err)
	assert.True(t, state.Finished)
	assert.True(t, state.Approved)
}

// TestAndSignRequiresEveryApprover covers 会签, including the 加签 interaction:
// adding an approver raises the bar instead of being ignorable.
func TestAndSignRequiresEveryApprover(t *testing.T) {
	repo := &stubChainRepo{nodeDefs: []*types.ApprovalFlowNode{
		{Seq: 1, Mode: string(types.ApprovalNodeModeAnd), AssigneeKind: "users",
			AssigneeIDs: types.JSON(`["a","b"]`)},
	}}
	svc := newChainService(repo)
	svc.nodes = &stubChainFlowRepo{flow: &types.ApprovalFlow{ID: "flow-1", Enabled: true}}
	ctx := context.Background()
	task := chainTask()
	nodes, err := svc.Instantiate(ctx, task, &interfaces.ApprovalResolution{})
	require.NoError(t, err)
	require.Equal(t, 2, nodes[0].RequiredCount)

	state, err := svc.Vote(ctx, task, "a", string(types.ApprovalDecisionApproved), "")
	require.NoError(t, err)
	assert.False(t, state.Finished, "one of two co-signers is not enough")

	// 加签：第三人加入后，会签仍然需要三人全过。
	state, err = svc.AddApprover(ctx, 7, task.ID, nodes[0].ID, "c", "admin")
	require.NoError(t, err)
	node := state.NodeByID(nodes[0].ID)
	require.NotNil(t, node)
	assert.Equal(t, 3, node.RequiredCount)
	assert.Contains(t, approverList(t, node), "c")

	state, err = svc.Vote(ctx, task, "b", string(types.ApprovalDecisionApproved), "")
	require.NoError(t, err)
	assert.False(t, state.Finished, "the newly added approver must still sign")

	state, err = svc.Vote(ctx, task, "c", string(types.ApprovalDecisionApproved), "")
	require.NoError(t, err)
	assert.True(t, state.Finished)
	assert.True(t, state.Approved)
}

// TestSequentialChainAdvancesStageByStage is the 逐级 core: stage 2 cannot be
// voted before stage 1 clears, and the chain finishes only at the last stage.
func TestSequentialChainAdvancesStageByStage(t *testing.T) {
	repo := &stubChainRepo{nodeDefs: []*types.ApprovalFlowNode{
		{Seq: 1, Mode: string(types.ApprovalNodeModeOr), AssigneeKind: "users",
			AssigneeIDs: types.JSON(`["owner-1"]`)},
		{Seq: 2, Mode: string(types.ApprovalNodeModeOr), AssigneeKind: "users",
			AssigneeIDs: types.JSON(`["legal-1"]`)},
	}}
	svc := newChainService(repo)
	svc.nodes = &stubChainFlowRepo{flow: &types.ApprovalFlow{ID: "flow-1", Enabled: true}}
	ctx := context.Background()
	task := chainTask()
	_, err := svc.Instantiate(ctx, task, &interfaces.ApprovalResolution{})
	require.NoError(t, err)

	// 第二级的人现在投票应被拒绝。
	_, err = svc.Vote(ctx, task, "legal-1", string(types.ApprovalDecisionApproved), "")
	require.ErrorIs(t, err, ErrChainNotAssignee)

	state, err := svc.Vote(ctx, task, "owner-1", string(types.ApprovalDecisionApproved), "")
	require.NoError(t, err)
	assert.False(t, state.Finished, "stage 2 has not run yet")
	assert.Equal(t, 2, state.ActiveNodeSeq)

	state, err = svc.Vote(ctx, task, "legal-1", string(types.ApprovalDecisionApproved), "")
	require.NoError(t, err)
	assert.True(t, state.Finished)
	assert.True(t, state.Approved)
}

// TestRejectAtAnyStageRejectsWholeChain covers 驳回: one rejection is terminal.
func TestRejectAtAnyStageRejectsWholeChain(t *testing.T) {
	repo := &stubChainRepo{nodeDefs: []*types.ApprovalFlowNode{
		{Seq: 1, Mode: string(types.ApprovalNodeModeOr), AssigneeKind: "users",
			AssigneeIDs: types.JSON(`["a"]`)},
		{Seq: 2, Mode: string(types.ApprovalNodeModeOr), AssigneeKind: "users",
			AssigneeIDs: types.JSON(`["b"]`)},
	}}
	svc := newChainService(repo)
	svc.nodes = &stubChainFlowRepo{flow: &types.ApprovalFlow{ID: "flow-1", Enabled: true}}
	ctx := context.Background()
	task := chainTask()
	_, err := svc.Instantiate(ctx, task, &interfaces.ApprovalResolution{})
	require.NoError(t, err)

	state, err := svc.Vote(ctx, task, "a", string(types.ApprovalDecisionRejected), "依据不足")
	require.NoError(t, err)
	assert.True(t, state.Finished)
	assert.False(t, state.Approved)
}

// TestDelegatedVoteIsAcceptedAndAttributed covers 委托: the delegate may vote
// for an absent approver, and the vote records whose authority it used.
func TestDelegatedVoteIsAcceptedAndAttributed(t *testing.T) {
	repo := &stubChainRepo{nodeDefs: []*types.ApprovalFlowNode{
		{Seq: 1, Mode: string(types.ApprovalNodeModeOr), AssigneeKind: "users",
			AssigneeIDs: types.JSON(`["absent"]`)},
	}}
	svc := newChainService(repo)
	svc.nodes = &stubChainFlowRepo{flow: &types.ApprovalFlow{ID: "flow-1", Enabled: true}}
	ctx := context.Background()
	task := chainTask()
	_, err := svc.Instantiate(ctx, task, &interfaces.ApprovalResolution{})
	require.NoError(t, err)

	// 没有委托时，外部人投票被拒。
	_, err = svc.Vote(ctx, task, "stand-in", string(types.ApprovalDecisionApproved), "")
	require.ErrorIs(t, err, ErrChainNotAssignee)

	_, err = svc.Delegate(ctx, 7, "absent", "stand-in", "休假", nil)
	require.NoError(t, err)

	state, err := svc.Vote(ctx, task, "stand-in", string(types.ApprovalDecisionApproved), "")
	require.NoError(t, err)
	assert.True(t, state.Finished)
	require.Len(t, state.Votes, 1)
	assert.Equal(t, "stand-in", state.Votes[0].ApproverID)
	assert.Equal(t, "absent", state.Votes[0].DelegateOf,
		"the vote must keep the delegator for the four-eyes trail")
}

// TestDuplicateVoteRejected covers the one-vote-per-approver rule.
func TestDuplicateVoteRejected(t *testing.T) {
	repo := &stubChainRepo{nodeDefs: []*types.ApprovalFlowNode{
		{Seq: 1, Mode: string(types.ApprovalNodeModeAnd), AssigneeKind: "users",
			AssigneeIDs: types.JSON(`["a","b"]`)},
	}}
	svc := newChainService(repo)
	svc.nodes = &stubChainFlowRepo{flow: &types.ApprovalFlow{ID: "flow-1", Enabled: true}}
	ctx := context.Background()
	task := chainTask()
	_, err := svc.Instantiate(ctx, task, &interfaces.ApprovalResolution{})
	require.NoError(t, err)

	_, err = svc.Vote(ctx, task, "a", string(types.ApprovalDecisionApproved), "")
	require.NoError(t, err)
	_, err = svc.Vote(ctx, task, "a", string(types.ApprovalDecisionApproved), "")
	require.ErrorIs(t, err, ErrChainAlreadyVoted)
}

// TestTransferApproverReplacesTheSlot covers 转签.
func TestTransferApproverReplacesTheSlot(t *testing.T) {
	repo := &stubChainRepo{nodeDefs: []*types.ApprovalFlowNode{
		{Seq: 1, Mode: string(types.ApprovalNodeModeOr), AssigneeKind: "users",
			AssigneeIDs: types.JSON(`["a"]`)},
	}}
	svc := newChainService(repo)
	svc.nodes = &stubChainFlowRepo{flow: &types.ApprovalFlow{ID: "flow-1", Enabled: true}}
	ctx := context.Background()
	task := chainTask()
	nodes, err := svc.Instantiate(ctx, task, &interfaces.ApprovalResolution{})
	require.NoError(t, err)

	state, err := svc.TransferApprover(ctx, 7, task.ID, nodes[0].ID, "a", "b", "admin")
	require.NoError(t, err)
	node := state.NodeByID(nodes[0].ID)
	require.NotNil(t, node)
	assert.Equal(t, []string{"b"}, approverList(t, node))

	// 被转签的人不再能签。
	_, err = svc.Vote(ctx, task, "a", string(types.ApprovalDecisionApproved), "")
	require.ErrorIs(t, err, ErrChainNotAssignee)
}

// TestTerminalNodeRejectsMutations keeps 加签/转签 from rewriting history.
func TestTerminalNodeRejectsMutations(t *testing.T) {
	repo := &stubChainRepo{nodeDefs: []*types.ApprovalFlowNode{
		{Seq: 1, Mode: string(types.ApprovalNodeModeOr), AssigneeKind: "users",
			AssigneeIDs: types.JSON(`["a"]`)},
	}}
	svc := newChainService(repo)
	svc.nodes = &stubChainFlowRepo{flow: &types.ApprovalFlow{ID: "flow-1", Enabled: true}}
	ctx := context.Background()
	task := chainTask()
	nodes, err := svc.Instantiate(ctx, task, &interfaces.ApprovalResolution{})
	require.NoError(t, err)
	_, err = svc.Vote(ctx, task, "a", string(types.ApprovalDecisionApproved), "")
	require.NoError(t, err)

	_, err = svc.AddApprover(ctx, 7, task.ID, nodes[0].ID, "late", "admin")
	require.ErrorIs(t, err, ErrChainNodeNotActive)
}

// TestEmptyNodeIsSkipped keeps an unstaffed stage from deadlocking the chain.
func TestEmptyNodeIsSkipped(t *testing.T) {
	repo := &stubChainRepo{nodeDefs: []*types.ApprovalFlowNode{
		{Seq: 1, Mode: string(types.ApprovalNodeModeOr), AssigneeKind: "users",
			AssigneeIDs: types.JSON(`[]`)},
		{Seq: 2, Mode: string(types.ApprovalNodeModeOr), AssigneeKind: "users",
			AssigneeIDs: types.JSON(`["a"]`)},
	}}
	svc := newChainService(repo)
	svc.nodes = &stubChainFlowRepo{flow: &types.ApprovalFlow{ID: "flow-1", Enabled: true}}
	nodes, err := svc.Instantiate(context.Background(), chainTask(), &interfaces.ApprovalResolution{})
	require.NoError(t, err)
	require.Len(t, nodes, 2)
	assert.Equal(t, string(types.ApprovalNodeStatusSkipped), nodes[0].Status)
	// 第二层直接激活，不需要第一层有人签。
	assert.Equal(t, string(types.ApprovalNodeStatusPending), nodes[1].Status)
}

// TestDelegationValidityWindow covers the expiry and revocation rules.
func TestDelegationValidityWindow(t *testing.T) {
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	assert.True(t, (&types.ApprovalDelegation{StartsAt: past}).Active(now))
	assert.False(t, (&types.ApprovalDelegation{StartsAt: future}).Active(now))
	assert.False(t, (&types.ApprovalDelegation{StartsAt: past, ExpiresAt: &past}).Active(now))
	assert.False(t, (&types.ApprovalDelegation{StartsAt: past, RevokedAt: &past}).Active(now))
}
