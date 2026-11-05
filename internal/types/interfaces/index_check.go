package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// IndexCheckRepository runs query plans for the performance self-check.
type IndexCheckRepository interface {
	// Dialect returns the database dialect name ("sqlite" / "postgres").
	Dialect() string
	// Explain returns the plan text for one check.
	Explain(ctx context.Context, check types.IndexCheckQuery) (string, error)
}

// IndexCheckService is the §17.1 index-coverage self-check.
type IndexCheckService interface {
	// Run executes every configured check and reports the plans.
	Run(ctx context.Context) (*types.IndexCheckReport, error)
}
