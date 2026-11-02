package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// approvalChainRepository implements interfaces.ApprovalChainRepository: the
// flow-node definitions, the instantiated task nodes and the vote log
// (KnowAxis V3.0 §6.3).
type approvalChainRepository struct {
	db *gorm.DB
}

// NewApprovalChainRepository constructs the chain repository.
func NewApprovalChainRepository(db *gorm.DB) interfaces.ApprovalChainRepository {
	return &approvalChainRepository{db: db}
}

// ReplaceFlowNodes replaces the node definitions of one flow in a single
// transaction, so a partially written definition can never be instantiated.
func (r *approvalChainRepository) ReplaceFlowNodes(
	ctx context.Context, tenantID uint64, flowID string, nodes []*types.ApprovalFlowNode,
) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("tenant_id = ? AND flow_id = ?", tenantID, flowID).
			Delete(&types.ApprovalFlowNode{}).Error; err != nil {
			return err
		}
		for i, node := range nodes {
			if node == nil {
				continue
			}
			node.TenantID = tenantID
			node.FlowID = flowID
			if node.Seq <= 0 {
				node.Seq = i + 1
			}
			if len(node.AssigneeIDs) == 0 {
				node.AssigneeIDs = types.JSON("[]")
			}
			if err := tx.Create(node).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// ListFlowNodes returns a flow's node definitions in approval order.
func (r *approvalChainRepository) ListFlowNodes(
	ctx context.Context, tenantID uint64, flowID string,
) ([]*types.ApprovalFlowNode, error) {
	var nodes []*types.ApprovalFlowNode
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND flow_id = ?", tenantID, flowID).
		Order("seq ASC").
		Find(&nodes).Error
	return nodes, err
}

// CreateTaskNodes instantiates the in-flight nodes for one task.
func (r *approvalChainRepository) CreateTaskNodes(
	ctx context.Context, tenantID uint64, nodes []*types.ApprovalTaskNode,
) error {
	if len(nodes) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, node := range nodes {
			if node == nil {
				continue
			}
			node.TenantID = tenantID
			if len(node.ApproverIDs) == 0 {
				node.ApproverIDs = types.JSON("[]")
			}
			if node.CreatedAt.IsZero() {
				node.CreatedAt = time.Now()
			}
			node.UpdatedAt = node.CreatedAt
			if err := tx.Create(node).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// ListTaskNodes returns a task's nodes in approval order.
func (r *approvalChainRepository) ListTaskNodes(
	ctx context.Context, tenantID uint64, taskID string,
) ([]*types.ApprovalTaskNode, error) {
	var nodes []*types.ApprovalTaskNode
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND task_id = ?", tenantID, taskID).
		Order("seq ASC").
		Find(&nodes).Error
	return nodes, err
}

// UpdateTaskNode persists one node's mutable state.
func (r *approvalChainRepository) UpdateTaskNode(
	ctx context.Context, node *types.ApprovalTaskNode,
) error {
	if node == nil {
		return errors.New("approval chain: node is required")
	}
	node.UpdatedAt = time.Now()
	return r.db.WithContext(ctx).Save(node).Error
}

// AppendVote records one vote.
func (r *approvalChainRepository) AppendVote(
	ctx context.Context, vote *types.ApprovalVote,
) error {
	if vote == nil {
		return errors.New("approval chain: vote is required")
	}
	if vote.CreatedAt.IsZero() {
		vote.CreatedAt = time.Now()
	}
	return r.db.WithContext(ctx).Create(vote).Error
}

// ListVotes returns a task's votes, oldest first.
func (r *approvalChainRepository) ListVotes(
	ctx context.Context, tenantID uint64, taskID string,
) ([]*types.ApprovalVote, error) {
	var votes []*types.ApprovalVote
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND task_id = ?", tenantID, taskID).
		Order("created_at ASC").
		Find(&votes).Error
	return votes, err
}

// CreateDelegation records a delegation grant.
func (r *approvalChainRepository) CreateDelegation(
	ctx context.Context, delegation *types.ApprovalDelegation,
) error {
	if delegation == nil {
		return errors.New("approval chain: delegation is required")
	}
	if delegation.StartsAt.IsZero() {
		delegation.StartsAt = time.Now()
	}
	if delegation.CreatedAt.IsZero() {
		delegation.CreatedAt = delegation.StartsAt
	}
	return r.db.WithContext(ctx).Create(delegation).Error
}

// RevokeDelegation ends a delegation early.
func (r *approvalChainRepository) RevokeDelegation(
	ctx context.Context, tenantID uint64, delegationID string, at time.Time,
) error {
	return r.db.WithContext(ctx).
		Model(&types.ApprovalDelegation{}).
		Where("tenant_id = ? AND id = ? AND revoked_at IS NULL", tenantID, delegationID).
		Update("revoked_at", at).Error
}

// ListActiveDelegationsFor returns live delegations where delegateID acts for
// someone, so the vote path can decide whether a cast is authorised.
func (r *approvalChainRepository) ListActiveDelegationsFor(
	ctx context.Context, tenantID uint64, delegateID string, now time.Time,
) ([]*types.ApprovalDelegation, error) {
	var delegations []*types.ApprovalDelegation
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND delegate_id = ? AND revoked_at IS NULL "+
			"AND starts_at <= ? AND (expires_at IS NULL OR expires_at > ?)",
			tenantID, delegateID, now, now).
		Find(&delegations).Error
	return delegations, err
}

// DelegationFor reports the live delegation letting delegateID vote for
// delegatorID, or nil when there is none.
func (r *approvalChainRepository) DelegationFor(
	ctx context.Context, tenantID uint64, delegatorID, delegateID string, now time.Time,
) (*types.ApprovalDelegation, error) {
	var delegation types.ApprovalDelegation
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND delegator_id = ? AND delegate_id = ? AND revoked_at IS NULL "+
			"AND starts_at <= ? AND (expires_at IS NULL OR expires_at > ?)",
			tenantID, delegatorID, delegateID, now, now).
		First(&delegation).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &delegation, nil
}
