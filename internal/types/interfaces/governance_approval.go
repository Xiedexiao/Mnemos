package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// GovernanceApprovalService routes the fourteen governance actions of §6.2
// through the same approval, receipt and four-eyes machinery as knowledge
// publishing.
type GovernanceApprovalService interface {
	// Submit creates the approval task for one governance action.
	Submit(
		ctx context.Context, tenantID uint64, requesterID string,
		req *types.GovernanceApprovalRequest,
	) (*types.ApprovalTask, error)
	// Decide records one approver's verdict.
	Decide(
		ctx context.Context, tenantID uint64, taskID, approverID string,
		decision types.ApprovalDecision, comment string,
	) (*types.ApprovalTask, error)
	// Status reports the latest approval state of one subject.
	Status(
		ctx context.Context, tenantID uint64, subjectType types.ApprovalSubjectType, subjectID string,
	) (*types.GovernanceApprovalStatus, error)
	// RequireApproval is the pre-action gate: nil error means the action may
	// proceed.
	RequireApproval(
		ctx context.Context, tenantID uint64, subjectType types.ApprovalSubjectType, subjectID string,
	) (*types.GovernanceApprovalDecision, error)
	// ListBySubjectType returns pending tasks of one subject type.
	ListBySubjectType(
		ctx context.Context, tenantID uint64, subjectType types.ApprovalSubjectType, limit int,
	) ([]*types.ApprovalTask, error)
}
