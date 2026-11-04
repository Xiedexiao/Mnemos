package types

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewQualityRatioLeavesNilWithoutSample is the honesty rule: "no sample"
// must not be reported as zero.
func TestNewQualityRatioLeavesNilWithoutSample(t *testing.T) {
	empty := NewQualityRatio(0, 0, "%")
	assert.Nil(t, empty.Value, "an empty denominator is not a measured zero")
	assert.NotEmpty(t, empty.Note)
	assert.EqualValues(t, 0, empty.Denominator)

	measured := NewQualityRatio(4, 5, "%")
	require.NotNil(t, measured.Value)
	assert.InDelta(t, 0.8, *measured.Value, 0.001)
	assert.EqualValues(t, 4, measured.Numerator)
	assert.EqualValues(t, 5, measured.Denominator)
	assert.Empty(t, measured.Note)
}

// TestNewQualityRatioAllZeroIsMeasured separates "0/5 failed" from "0/0".
func TestNewQualityRatioAllZeroIsMeasured(t *testing.T) {
	ratio := NewQualityRatio(0, 5, "%")
	require.NotNil(t, ratio.Value, "0 out of 5 is a real measurement")
	assert.Zero(t, *ratio.Value)
}

// TestMeanHoursAverages covers the duration metric.
func TestMeanHoursAverages(t *testing.T) {
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	samples := []QualityLatencySample{
		{From: base, To: base.Add(2 * time.Hour)},
		{From: base, To: base.Add(4 * time.Hour)},
	}
	ratio := MeanHours(samples)
	require.NotNil(t, ratio.Value)
	assert.InDelta(t, 3.0, *ratio.Value, 0.001)
	assert.Equal(t, "hours", ratio.Unit)
}

// TestMeanHoursSkipsImpossibleSamples keeps a publish-before-signal row from
// skewing the average.
func TestMeanHoursSkipsImpossibleSamples(t *testing.T) {
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	samples := []QualityLatencySample{
		{From: base, To: base.Add(2 * time.Hour)},
		// Published before the signal: not a valid latency.
		{From: base, To: base.Add(-5 * time.Hour)},
	}
	ratio := MeanHours(samples)
	require.NotNil(t, ratio.Value)
	assert.InDelta(t, 2.0, *ratio.Value, 0.001, "the impossible sample must be dropped")
	assert.EqualValues(t, 1, ratio.Numerator)
	assert.EqualValues(t, 2, ratio.Denominator, "the denominator still shows it was excluded")
}

// TestMeanHoursWithoutSamples covers the empty case.
func TestMeanHoursWithoutSamples(t *testing.T) {
	ratio := MeanHours(nil)
	assert.Nil(t, ratio.Value)
	assert.NotEmpty(t, ratio.Note)
}

// TestRepeatedQuestionDrop covers both directions.
func TestRepeatedQuestionDrop(t *testing.T) {
	// 10 repeats last window, 4 this window: a 60% drop.
	drop := RepeatedQuestionDrop(4, 10)
	require.NotNil(t, drop.Value)
	assert.InDelta(t, 0.6, *drop.Value, 0.001)

	// Repeats went up: negative drop.
	rise := RepeatedQuestionDrop(12, 10)
	require.NotNil(t, rise.Value)
	assert.InDelta(t, -0.2, *rise.Value, 0.001)
}

// TestRepeatedQuestionDropWithoutBaseline refuses to claim a drop when the
// previous window had none.
func TestRepeatedQuestionDropWithoutBaseline(t *testing.T) {
	ratio := RepeatedQuestionDrop(5, 0)
	assert.Nil(t, ratio.Value, "no baseline means no measurable drop")
	assert.NotEmpty(t, ratio.Note)

	// Both zero: also no baseline.
	assert.Nil(t, RepeatedQuestionDrop(0, 0).Value)
}

// TestComputeReuseRate covers the reuse metric.
func TestComputeReuseRate(t *testing.T) {
	signals := []KnowledgeUsageSignal{
		{KnowledgeID: "a", Used: 3},
		{KnowledgeID: "b", Used: 0},
		{KnowledgeID: "c", Used: 1},
	}
	ratio := ComputeReuseRate(signals, 10)
	require.NotNil(t, ratio.Value)
	assert.InDelta(t, 0.2, *ratio.Value, 0.001, "2 of 10 entries were used")
	assert.EqualValues(t, 2, ratio.Numerator)

	// A published set with nothing reusable still measures.
	assert.Nil(t, ComputeReuseRate(nil, 0).Value)
}

// TestBuildQualityWindow covers the window resolution.
func TestBuildQualityWindow(t *testing.T) {
	now := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	window := BuildQualityWindow(now, 30)
	assert.Equal(t, 30, window.Days)
	assert.Equal(t, now, window.Until)
	assert.Equal(t, now.AddDate(0, 0, -30), window.Since)

	// Clamping.
	assert.Equal(t, 30, BuildQualityWindow(now, 0).Days)
	assert.Equal(t, 30, BuildQualityWindow(now, -5).Days)
	assert.Equal(t, 365, BuildQualityWindow(now, 9999).Days)
}

// TestSortUsageSignals orders by use count then id.
func TestSortUsageSignals(t *testing.T) {
	signals := []KnowledgeUsageSignal{
		{KnowledgeID: "b", Used: 1},
		{KnowledgeID: "a", Used: 5},
		{KnowledgeID: "c", Used: 1},
	}
	SortUsageSignals(signals)
	assert.Equal(t, "a", signals[0].KnowledgeID)
	assert.Equal(t, "b", signals[1].KnowledgeID)
	assert.Equal(t, "c", signals[2].KnowledgeID)
}
