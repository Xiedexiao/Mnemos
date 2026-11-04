package interfaces

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// QualityMetricsService computes the §14.7 timeliness / accuracy / value groups.
type QualityMetricsService interface {
	// Get computes the metrics for one tenant over the window.
	Get(ctx context.Context, tenantID uint64, days int) (*types.QualityMetrics, error)
}

// QualityMetricsRepository answers the aggregate queries behind §14.7.
type QualityMetricsRepository interface {
	// CountPublishedKnowledge counts usable published entries.
	CountPublishedKnowledge(ctx context.Context, tenantID uint64) (int64, error)
	// CountUsedKnowledge counts published entries read or cited in the window.
	CountUsedKnowledge(ctx context.Context, tenantID uint64, since time.Time) (int64, error)
	// SignalToUpdatePairs returns (signal collected, change published) pairs.
	SignalToUpdatePairs(
		ctx context.Context, tenantID uint64, since time.Time, limit int,
	) ([]types.QualityLatencySample, error)
	// ReviewOnTime counts completed reviews and how many finished before due.
	ReviewOnTime(ctx context.Context, tenantID uint64, since time.Time) (onTime, completed int64, err error)
	// CorrectionCloseDurations returns (created, closed) pairs for corrections.
	CorrectionCloseDurations(
		ctx context.Context, tenantID uint64, since time.Time, limit int,
	) ([]types.QualityLatencySample, error)
	// SamplingStats reads passed/failed inspection counts.
	SamplingStats(ctx context.Context, tenantID uint64, since time.Time) (passed, failed int64, err error)
	// CountErrorPublished counts entries confirmed wrong after release.
	CountErrorPublished(ctx context.Context, tenantID uint64, since time.Time) (int64, error)
	// AnswerCounts counts answered turns and how many carried citations.
	AnswerCounts(ctx context.Context, tenantID uint64, since time.Time) (withCitations, total int64, err error)
	// FeedbackResolution counts rated answers and the useful ones.
	FeedbackResolution(ctx context.Context, tenantID uint64, since time.Time) (useful, rated int64, err error)
	// RepeatedZeroResultQueries counts recurring zero-result queries.
	RepeatedZeroResultQueries(ctx context.Context, tenantID uint64, since, until time.Time) (int64, error)
	// GapCounts returns gaps created and closed in the window.
	GapCounts(ctx context.Context, tenantID uint64, since, until time.Time) (created, closed int64, err error)
}
