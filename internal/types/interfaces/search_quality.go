package interfaces

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// SearchLogRepository stores query telemetry (§12.5 查询日志).
type SearchLogRepository interface {
	// Create appends one query log row.
	Create(ctx context.Context, entry *types.SearchLog) error
	// MarkClicked flags a logged query as leading to a click.
	MarkClicked(ctx context.Context, tenantID uint64, logID string) error
	// Overview aggregates the dashboard numbers for a window.
	Overview(ctx context.Context, tenantID uint64, since time.Time) (*types.SearchQualityOverview, error)
	// FrequentQueries returns the most frequent queries; zeroOnly filters to
	// queries that returned nothing.
	FrequentQueries(
		ctx context.Context, tenantID uint64, since time.Time, zeroOnly bool, limit int,
	) ([]types.FrequentQuery, error)
}

// KnowledgeGapRepository clusters missing-knowledge signals (§14.4).
type KnowledgeGapRepository interface {
	// UpsertOccurrence increments the counter for a normalized query.
	UpsertOccurrence(ctx context.Context, gap *types.KnowledgeGap) (*types.KnowledgeGap, error)
	// List returns gaps of one tenant, most frequent first.
	List(
		ctx context.Context, tenantID uint64, status types.KnowledgeGapStatus, limit int,
	) ([]*types.KnowledgeGap, error)
	// Update applies operator changes.
	Update(
		ctx context.Context, tenantID uint64, gapID string, updates map[string]interface{},
	) (*types.KnowledgeGap, error)
	// Get returns one gap.
	Get(ctx context.Context, tenantID uint64, gapID string) (*types.KnowledgeGap, error)
}

// SearchQualityService is the write+read face of search telemetry: the retrieval
// path records queries, and the operations console reads aggregates and works
// the gap list.
type SearchQualityService interface {
	// Record logs one executed query. Best-effort: logging must never fail a
	// search the caller already got.
	Record(ctx context.Context, entry *types.SearchLog)
	// RecordResult is a convenience wrapper for the retrieval path.
	RecordResult(
		ctx context.Context, channel types.SearchChannel, query string,
		kbIDs []string, resultCount, permissionFiltered int, topScore float64,
		duration time.Duration,
	)
	// MarkClicked flags a query as producing a click.
	MarkClicked(ctx context.Context, tenantID uint64, logID string) error
	// Overview returns the §12.5 dashboard numbers.
	Overview(ctx context.Context, tenantID uint64, windowDays int) (*types.SearchQualityOverview, error)
	// FrequentQueries returns the hot queries; zeroOnly restricts to queries
	// that returned nothing.
	FrequentQueries(
		ctx context.Context, tenantID uint64, windowDays int, zeroOnly bool, limit int,
	) ([]types.FrequentQuery, error)
}

// KnowledgeGapService turns repeated zero-result queries into an actionable gap
// list (§14.4).
type KnowledgeGapService interface {
	// Observe records one missing-knowledge signal. The caller decides the
	// threshold; this only clusters.
	Observe(
		ctx context.Context, tenantID uint64, query string,
		source types.KnowledgeGapSource,
	) (*types.KnowledgeGap, error)
	// List returns the gap list.
	List(ctx context.Context, tenantID uint64, status types.KnowledgeGapStatus, limit int) ([]*types.KnowledgeGap, error)
	// Assign hands a gap to an owner.
	Assign(ctx context.Context, tenantID uint64, gapID, assigneeID string) (*types.KnowledgeGap, error)
	// UpdateStatus moves a gap through its lifecycle, optionally linking the
	// knowledge that resolved it.
	UpdateStatus(
		ctx context.Context, tenantID uint64, gapID string,
		status types.KnowledgeGapStatus, resolvedKnowledgeID string,
	) (*types.KnowledgeGap, error)
}
