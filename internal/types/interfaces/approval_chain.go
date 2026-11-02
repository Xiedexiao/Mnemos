package interfaces

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// ApprovalChainRepository persists the multi-stage approval chain: node
// definitions, instantiated task nodes, votes and delegations
// (KnowAxis V3.0 §6.3).
type ApprovalChainRepository interface {
	// ReplaceFlowNodes replaces one flow's node definitions atomically.
	ReplaceFlowNodes(
		ctx context.Context, tenantID uint64, flowID string, nodes []*types.ApprovalFlowNode,
	) error
	// ListFlowNodes returns a flow's definitions in approval order.
	ListFlowNodes(ctx context.Context, tenantID uint64, flowID string) ([]*types.ApprovalFlowNode, error)
	// CreateTaskNodes instantiates a task's in-flight nodes.
	CreateTaskNodes(ctx context.Context, tenantID uint64, nodes []*types.ApprovalTaskNode) error
	// ListTaskNodes returns a task's nodes in approval order.
	ListTaskNodes(ctx context.Context, tenantID uint64, taskID string) ([]*types.ApprovalTaskNode, error)
	// UpdateTaskNode persists one node's mutable state.
	UpdateTaskNode(ctx context.Context, node *types.ApprovalTaskNode) error
	// AppendVote records one vote.
	AppendVote(ctx context.Context, vote *types.ApprovalVote) error
	// ListVotes returns a task's votes, oldest first.
	ListVotes(ctx context.Context, tenantID uint64, taskID string) ([]*types.ApprovalVote, error)
	// CreateDelegation records a delegation grant.
	CreateDelegation(ctx context.Context, delegation *types.ApprovalDelegation) error
	// RevokeDelegation ends a delegation early.
	RevokeDelegation(ctx context.Context, tenantID uint64, delegationID string, at time.Time) error
	// ListActiveDelegationsFor returns the live delegations a delegate holds.
	ListActiveDelegationsFor(
		ctx context.Context, tenantID uint64, delegateID string, now time.Time,
	) ([]*types.ApprovalDelegation, error)
	// DelegationFor reports whether delegateID may vote for delegatorID.
	DelegationFor(
		ctx context.Context, tenantID uint64, delegatorID, delegateID string, now time.Time,
	) (*types.ApprovalDelegation, error)
}

// ApprovalChainService is the §6.3 approval-engine surface: instantiating a
// chain, advancing it with votes, and the in-flight mutations (加签/转签/委托).
type ApprovalChainService interface {
	// Instantiate builds the in-flight chain for a submitted task from its
	// space's flow definition. A space without definitions gets one implicit
	// node covering the legacy quorum, so single/dual approval keeps working.
	Instantiate(
		ctx context.Context, task *types.ApprovalTask, resolution *ApprovalResolution,
	) ([]*types.ApprovalTaskNode, error)
	// Vote records one approver's decision and advances the chain.
	Vote(
		ctx context.Context, task *types.ApprovalTask, approverID, decision, comment string,
	) (*types.ApprovalChainState, error)
	// AddApprover appends an approver to one node (加签).
	AddApprover(
		ctx context.Context, tenantID uint64, taskID, nodeID, approverID, actorID string,
	) (*types.ApprovalChainState, error)
	// TransferApprover replaces one approver on a node (转签).
	TransferApprover(
		ctx context.Context, tenantID uint64, taskID, nodeID, fromID, toID, actorID string,
	) (*types.ApprovalChainState, error)
	// State returns the current chain state of a task.
	State(ctx context.Context, tenantID uint64, taskID string) (*types.ApprovalChainState, error)
	// Delegate creates a delegation (休假代理).
	Delegate(
		ctx context.Context, tenantID uint64, delegatorID, delegateID, reason string, expiresAt *time.Time,
	) (*types.ApprovalDelegation, error)
	// RevokeDelegation ends a delegation.
	RevokeDelegation(ctx context.Context, tenantID uint64, delegationID, actorID string) error
	// ListDelegations returns the live delegations a delegate holds.
	ListDelegations(
		ctx context.Context, tenantID uint64, delegateID string,
	) ([]*types.ApprovalDelegation, error)
}
