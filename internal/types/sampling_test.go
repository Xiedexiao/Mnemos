package types

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSamplingRatioForRisk pins the §6.6 rates.
func TestSamplingRatioForRisk(t *testing.T) {
	assert.Equal(t, SamplingRatioFull, SamplingRatioForRisk("high"))
	assert.Equal(t, SamplingRatioTen, SamplingRatioForRisk("mid"))
	assert.Equal(t, SamplingRatioTwo, SamplingRatioForRisk("low"))
	// An unset risk must not default to the loosest rate.
	assert.Equal(t, SamplingRatioTen, SamplingRatioForRisk(""))
	assert.Equal(t, SamplingRatioTen, SamplingRatioForRisk("unknown"))
}

// TestShouldSampleIsDeterministic is why hashing beats randomness: an audit can
// reproduce exactly which documents were drawn.
func TestShouldSampleIsDeterministic(t *testing.T) {
	first := ShouldSample("k-1", "2026-11", SamplingRatioTen)
	for i := 0; i < 100; i++ {
		assert.Equal(t, first, ShouldSample("k-1", "2026-11", SamplingRatioTen))
	}
	// A different period is a different draw.
	_ = ShouldSample("k-1", "2026-12", SamplingRatioTen)
}

// TestShouldSampleFullAlwaysTrue covers the high-risk rule.
func TestShouldSampleFullAlwaysTrue(t *testing.T) {
	for _, id := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		assert.True(t, ShouldSample(id, "2026-11", SamplingRatioFull), id)
	}
}

// TestShouldSampleRoughlyMatchesRatio checks the distribution is in the right
// ballpark. It is not exact by design (a hash is not a random sampler), so the
// bounds are wide.
func TestShouldSampleRoughlyMatchesRatio(t *testing.T) {
	const total = 4000
	ten, two := 0, 0
	for i := 0; i < total; i++ {
		id := "doc-" + itoaSampling(i)
		if ShouldSample(id, "2026-11", SamplingRatioTen) {
			ten++
		}
		if ShouldSample(id, "2026-11", SamplingRatioTwo) {
			two++
		}
	}
	tenRate := float64(ten) / float64(total)
	twoRate := float64(two) / float64(total)
	assert.InDelta(t, 0.10, tenRate, 0.04, "10%% sampling should be near 10%%")
	assert.InDelta(t, 0.02, twoRate, 0.02, "2%% sampling should be near 2%%")
	assert.Less(t, two, ten, "a lower ratio must draw fewer documents")
}

// TestShouldSampleZeroRatio covers the disabled case.
func TestShouldSampleZeroRatio(t *testing.T) {
	assert.False(t, ShouldSample("k-1", "2026-11", SamplingRatio(0)))
}

// TestValidateSamplingDecision requires a reason for failures and waivers.
func TestValidateSamplingDecision(t *testing.T) {
	require.NoError(t, ValidateSamplingDecision(SamplingDecisionInput{Status: "passed"}))
	require.Error(t, ValidateSamplingDecision(SamplingDecisionInput{Status: "failed"}),
		"a failure without a note cannot be reviewed later")
	require.Error(t, ValidateSamplingDecision(SamplingDecisionInput{Status: "waived"}),
		"a waiver without a reason is a silent pass")
	require.NoError(t, ValidateSamplingDecision(SamplingDecisionInput{
		Status: "failed", Note: "步骤 3 与设备手册不符",
	}))
	require.Error(t, ValidateSamplingDecision(SamplingDecisionInput{Status: "pending"}),
		"pending is not a verdict")
	require.Error(t, ValidateSamplingDecision(SamplingDecisionInput{Status: ""}))
}

// TestSamplingPeriod covers the window label.
func TestSamplingPeriod(t *testing.T) {
	assert.Equal(t, "2026-11",
		SamplingPeriod(time.Date(2026, 11, 15, 3, 0, 0, 0, time.UTC)))
}

// TestSamplingDecisionStatusValid covers the enum guard.
func TestSamplingDecisionStatusValid(t *testing.T) {
	for _, status := range []SamplingDecisionStatus{
		SamplingPending, SamplingPassed, SamplingFailed, SamplingWaived,
	} {
		assert.True(t, status.Valid())
	}
	assert.False(t, SamplingDecisionStatus("skipped").Valid())
}

// TestQualityTaskSamplingFailedIsSystemOnly keeps users from fabricating a
// failed inspection.
func TestQualityTaskSamplingFailedIsSystemOnly(t *testing.T) {
	assert.True(t, QualityTaskTypeSamplingFailed.Valid())
	assert.True(t, QualityTaskTypeSamplingFailed.SystemOnly())
	assert.False(t, QualityTaskTypeSamplingFailed.Reportable())
}

// itoaSampling renders a small non-negative int.
func itoaSampling(value int) string {
	if value == 0 {
		return "0"
	}
	digits := make([]byte, 0, 6)
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}
