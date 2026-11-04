package interfaces

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// SamplingRepository persists §6.6 sampling inspections.
type SamplingRepository interface {
	// DrawCandidates returns published knowledge in the tenant that could be
	// sampled in the period.
	DrawCandidates(
		ctx context.Context, tenantID uint64, since time.Time, limit int,
	) ([]*types.SamplingCandidate, error)
	// Insert draws one inspection, ignoring an existing (period, document) row.
	Insert(ctx context.Context, inspection *types.SamplingInspection) (bool, error)
	// Get returns one inspection in the tenant.
	Get(ctx context.Context, tenantID uint64, inspectionID string) (*types.SamplingInspection, error)
	// List returns inspections, newest first.
	List(
		ctx context.Context, tenantID uint64, period string,
		status types.SamplingDecisionStatus, limit int,
	) ([]*types.SamplingInspection, error)
	// Decide records one verdict.
	Decide(
		ctx context.Context, tenantID uint64, inspectionID string,
		status types.SamplingDecisionStatus, reviewerID, note, followUpTaskID string,
		at time.Time,
	) (*types.SamplingInspection, error)
	// Stats aggregates the outcomes.
	Stats(ctx context.Context, tenantID uint64, period string) (*types.SamplingStats, error)
}

// SamplingService is the §6.6 inspection surface.
type SamplingService interface {
	// Draw performs one sampling pass for the period.
	Draw(ctx context.Context, tenantID uint64, period string, limit int) (*types.SamplingRunResult, error)
	// List returns the inspection queue.
	List(
		ctx context.Context, tenantID uint64, period string,
		status types.SamplingDecisionStatus, limit int,
	) ([]*types.SamplingInspection, error)
	// Decide records a verdict; a failed inspection opens a corrective task.
	Decide(
		ctx context.Context, tenantID uint64, actorID string,
		input types.SamplingDecisionInput,
	) (*types.SamplingInspection, error)
	// Stats aggregates the inspection outcomes.
	Stats(ctx context.Context, tenantID uint64, period string) (*types.SamplingStats, error)
}
