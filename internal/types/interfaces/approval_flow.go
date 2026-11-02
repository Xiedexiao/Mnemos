package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// ApprovalFlowService manages per-space approval routing configuration and
// resolves the approval chain for a submission (KnowAxis V3.0 §6.3).
type ApprovalFlowService interface {
	// Get returns the configuration of one space, or the single-mode default
	// when none is configured.
	Get(ctx context.Context, tenantID uint64, kbID string) (*types.ApprovalFlow, error)
	// List returns every configured space in the tenant.
	List(ctx context.Context, tenantID uint64, limit int) ([]*types.ApprovalFlow, error)
	// Save creates or updates one space's configuration and advances its
	// version, so in-flight tasks keep their frozen definition.
	Save(ctx context.Context, flow *types.ApprovalFlow) (*types.ApprovalFlow, error)
	// Delete removes a space's configuration.
	Delete(ctx context.Context, tenantID uint64, kbID string) error
	// Resolve decides the approval chain for one submission.
	Resolve(
		ctx context.Context, tenantID uint64, kbID string, contentRisk types.ApprovalRiskLevel,
	) (*ApprovalResolution, error)
}

// ApprovalResolution is the outcome of evaluating a submission against a space's
// routing rules.
type ApprovalResolution struct {
	// RiskLevel is the final risk level (content risk raised by any floor).
	RiskLevel types.ApprovalRiskLevel `json:"risk_level"`
	// Mode is the approval mode the chain runs in.
	Mode types.ApprovalFlowMode `json:"mode"`
	// Quorum is how many distinct approvers must approve.
	Quorum int `json:"quorum"`
	// FlowVersion is the definition version the chain was resolved from.
	FlowVersion int `json:"flow_version"`
	// RequireDistinctReviewerRoles asks the review service to reject a second
	// vote from an approver whose role matches the first (§6.3 四眼).
	RequireDistinctReviewerRoles bool `json:"require_distinct_reviewer_roles"`
	// Reasons names every rule that influenced the outcome, so an operator can
	// answer "why is this dual".
	Reasons []string `json:"reasons"`
}

// ApprovalSLAService sweeps pending tasks for reminders and escalations
// (§6.3 到期催办 / 超时升级 / 积压预警).
type ApprovalSLAService interface {
	// SweepOnce runs one reminder/escalation pass and returns how many tasks it
	// acted on. It is idempotent per task per kind.
	SweepOnce(ctx context.Context) (reminded, escalated int, err error)
	// Events returns the SLA ledger of one task.
	Events(ctx context.Context, tenantID uint64, taskID string) ([]*types.ApprovalSLAEvent, error)
}
