package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// OperationsDashboardService composes the §16.1 operations view from the
// existing verticals; it defines no new business rules of its own.
type OperationsDashboardService interface {
	// Get composes the dashboard for one tenant over the given window.
	Get(ctx context.Context, tenantID uint64, windowDays int) (*types.OperationsDashboard, error)
}
