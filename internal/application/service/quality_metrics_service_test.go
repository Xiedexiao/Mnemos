package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// stubQualityMetricsRepo answers the §14.7 aggregates from fixed fixtures.
type stubQualityMetricsRepo struct {
	interfaces.QualityMetricsRepository

	published   int64
	used        int64
	pairs       []types.QualityLatencySample
	pairsErr    error
	onTime      int64
	completed   int64
	corrections []types.QualityLatencySample
	passed      int64
	failed      int64
	flagged     int64
	withCite    int64
	answers     int64
	useful      int64
	rated       int64
	currentRep  int64
	prevRep     int64
	gapCreated  int64
	gapClosed   int64
	samplingErr error
}

func (s *stubQualityMetricsRepo) CountPublishedKnowledge(
	context.Context, uint64,
) (int64, error) {
	return s.published, nil
}

func (s *stubQualityMetricsRepo) CountUsedKnowledge(
	context.Context, uint64, time.Time,
) (int64, error) {
	return s.used, nil
}

func (s *stubQualityMetricsRepo) SignalToUpdatePairs(
	context.Context, uint64, time.Time, int,
) ([]types.QualityLatencySample, error) {
	return s.pairs, s.pairsErr
}

func (s *stubQualityMetricsRepo) ReviewOnTime(
	context.Context, uint64, time.Time,
) (int64, int64, error) {
	return s.onTime, s.completed, nil
}

func (s *stubQualityMetricsRepo) CorrectionCloseDurations(
	context.Context, uint64, time.Time, int,
) ([]types.QualityLatencySample, error) {
	return s.corrections, nil
}

func (s *stubQualityMetricsRepo) SamplingStats(
	context.Context, uint64, time.Time,
) (int64, int64, error) {
	return s.passed, s.failed, s.samplingErr
}

func (s *stubQualityMetricsRepo) CountErrorPublished(
	context.Context, uint64, time.Time,
) (int64, error) {
	return s.flagged, nil
}

func (s *stubQualityMetricsRepo) AnswerCounts(
	context.Context, uint64, time.Time,
) (int64, int64, error) {
	return s.withCite, s.answers, nil
}

func (s *stubQualityMetricsRepo) FeedbackResolution(
	context.Context, uint64, time.Time,
) (int64, int64, error) {
	return s.useful, s.rated, nil
}

func (s *stubQualityMetricsRepo) RepeatedZeroResultQueries(
	_ context.Context, _ uint64, since, until time.Time,
) (int64, error) {
	// The service calls this twice: current window, then the previous one.
	if since.After(testBase) {
		return s.currentRep, nil
	}
	return s.prevRep, nil
}

func (s *stubQualityMetricsRepo) GapCounts(
	context.Context, uint64, time.Time, time.Time,
) (int64, int64, error) {
	return s.gapCreated, s.gapClosed, nil
}

// testBase anchors the stub's window comparison.
var testBase = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

func newQualityMetricsService(repo *stubQualityMetricsRepo) *qualityMetricsService {
	return &qualityMetricsService{
		repo: repo,
		now:  func() time.Time { return time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC) },
	}
}

// TestQualityMetricsTimeliness covers the first group.
func TestQualityMetricsTimeliness(t *testing.T) {
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	repo := &stubQualityMetricsRepo{
		pairs: []types.QualityLatencySample{
			{From: base, To: base.Add(6 * time.Hour)},
			{From: base, To: base.Add(12 * time.Hour)},
		},
		onTime:    8,
		completed: 10,
		corrections: []types.QualityLatencySample{
			{From: base, To: base.Add(24 * time.Hour)},
		},
	}
	metrics, err := newQualityMetricsService(repo).Get(context.Background(), 7, 30)
	require.NoError(t, err)

	require.NotNil(t, metrics.Timeliness.SourceToUpdateHours.Value)
	assert.InDelta(t, 9.0, *metrics.Timeliness.SourceToUpdateHours.Value, 0.01)
	require.NotNil(t, metrics.Timeliness.ReviewOnTimeRate.Value)
	assert.InDelta(t, 0.8, *metrics.Timeliness.ReviewOnTimeRate.Value, 0.001)
	require.NotNil(t, metrics.Timeliness.CorrectionCloseHours.Value)
	assert.InDelta(t, 24.0, *metrics.Timeliness.CorrectionCloseHours.Value, 0.01)
}

// TestQualityMetricsAccuracy covers the second group.
func TestQualityMetricsAccuracy(t *testing.T) {
	repo := &stubQualityMetricsRepo{
		published: 200,
		flagged:   4,
		passed:    18,
		failed:    2,
		withCite:  45,
		answers:   50,
	}
	metrics, err := newQualityMetricsService(repo).Get(context.Background(), 7, 30)
	require.NoError(t, err)

	require.NotNil(t, metrics.Accuracy.ErrorPublishRate.Value)
	assert.InDelta(t, 0.02, *metrics.Accuracy.ErrorPublishRate.Value, 0.001)
	require.NotNil(t, metrics.Accuracy.SamplingFailRate.Value)
	assert.InDelta(t, 0.1, *metrics.Accuracy.SamplingFailRate.Value, 0.001)
	require.NotNil(t, metrics.Accuracy.CitationCoverageRate.Value)
	assert.InDelta(t, 0.9, *metrics.Accuracy.CitationCoverageRate.Value, 0.001)
}

// TestQualityMetricsValue covers the third group.
func TestQualityMetricsValue(t *testing.T) {
	repo := &stubQualityMetricsRepo{
		published:  100,
		used:       35,
		useful:     12,
		rated:      20,
		currentRep: 4,
		prevRep:    10,
		gapCreated: 5,
		gapClosed:  4,
	}
	svc := &qualityMetricsService{
		repo: repo,
		// Pin the clock so "previous window" is before the stub's anchor.
		now: func() time.Time { return time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC) },
	}
	metrics, err := svc.Get(context.Background(), 7, 30)
	require.NoError(t, err)

	require.NotNil(t, metrics.Value.ReuseRate.Value)
	assert.InDelta(t, 0.35, *metrics.Value.ReuseRate.Value, 0.001)
	require.NotNil(t, metrics.Value.ResolutionRate.Value)
	assert.InDelta(t, 0.6, *metrics.Value.ResolutionRate.Value, 0.001)
	require.NotNil(t, metrics.Value.GapCloseRate.Value)
	assert.InDelta(t, 0.8, *metrics.Value.GapCloseRate.Value, 0.001)
	// 10 repeats last window, 4 this window: a 60% drop.
	require.NotNil(t, metrics.Value.RepeatedQuestionDrop.Value)
	assert.InDelta(t, 0.6, *metrics.Value.RepeatedQuestionDrop.Value, 0.001)
}

// TestQualityMetricsMissingSamplesStayNil is the honesty rule at the service
// level: an empty tenant reports null ratios, not zeros.
func TestQualityMetricsMissingSamplesStayNil(t *testing.T) {
	repo := &stubQualityMetricsRepo{}
	metrics, err := newQualityMetricsService(repo).Get(context.Background(), 7, 30)
	require.NoError(t, err)
	for name, ratio := range map[string]types.QualityRatio{
		"source_to_update":  metrics.Timeliness.SourceToUpdateHours,
		"review_on_time":    metrics.Timeliness.ReviewOnTimeRate,
		"correction_close":  metrics.Timeliness.CorrectionCloseHours,
		"error_publish":     metrics.Accuracy.ErrorPublishRate,
		"sampling_fail":     metrics.Accuracy.SamplingFailRate,
		"citation_coverage": metrics.Accuracy.CitationCoverageRate,
		"reuse":             metrics.Value.ReuseRate,
		"resolution":        metrics.Value.ResolutionRate,
		"gap_close":         metrics.Value.GapCloseRate,
		"repeated_drop":     metrics.Value.RepeatedQuestionDrop,
	} {
		assert.Nil(t, ratio.Value, "%s must be null with no sample", name)
	}
}

// TestQualityMetricsGroupFailureIsIsolated keeps one broken query from
// blanking the others.
func TestQualityMetricsGroupFailureIsIsolated(t *testing.T) {
	repo := &stubQualityMetricsRepo{
		published:   100,
		used:        50,
		onTime:      5,
		completed:   5,
		pairsErr:    errors.New("latency query failed"),
		samplingErr: errors.New("sampling query failed"),
	}
	metrics, err := newQualityMetricsService(repo).Get(context.Background(), 7, 30)
	require.NoError(t, err)

	// The failed metrics say so.
	assert.Nil(t, metrics.Timeliness.SourceToUpdateHours.Value)
	assert.Equal(t, "取数失败", metrics.Timeliness.SourceToUpdateHours.Note)
	assert.Nil(t, metrics.Accuracy.SamplingFailRate.Value)
	assert.Equal(t, "取数失败", metrics.Accuracy.SamplingFailRate.Note)

	// The healthy ones still compute.
	require.NotNil(t, metrics.Timeliness.ReviewOnTimeRate.Value)
	assert.InDelta(t, 1.0, *metrics.Timeliness.ReviewOnTimeRate.Value, 0.001)
	require.NotNil(t, metrics.Value.ReuseRate.Value)
	assert.InDelta(t, 0.5, *metrics.Value.ReuseRate.Value, 0.001)
}

// TestQualityMetricsWindowIsReported covers the echo of the window bounds.
func TestQualityMetricsWindowIsReported(t *testing.T) {
	metrics, err := newQualityMetricsService(&stubQualityMetricsRepo{}).Get(
		context.Background(), 7, 7)
	require.NoError(t, err)
	assert.Equal(t, 7, metrics.Window.Days)
	assert.Equal(t, time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC), metrics.Window.Until)
	assert.Equal(t, metrics.Window.Until.AddDate(0, 0, -7), metrics.Window.Since)
}

// TestQualityMetricsRequiresTenant covers the guard.
func TestQualityMetricsRequiresTenant(t *testing.T) {
	_, err := newQualityMetricsService(&stubQualityMetricsRepo{}).Get(
		context.Background(), 0, 30)
	require.Error(t, err)
}
