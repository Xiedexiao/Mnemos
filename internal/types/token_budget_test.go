package types

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// budgetTestNow is the reference instant for window math.
var budgetTestNow = time.Date(2026, 11, 4, 15, 30, 0, 0, time.UTC)

// TestAssessTokenBudgetBands covers the four bands.
func TestAssessTokenBudgetBands(t *testing.T) {
	budget := TokenBudget{
		TenantID: 7, Period: TokenBudgetMonthly, LimitTokens: 1000, WarnPercent: 80,
	}
	cases := []struct {
		used   int64
		status TokenBudgetStatus
	}{
		{0, TokenBudgetOK},
		{799, TokenBudgetOK},
		{800, TokenBudgetWarn},
		{999, TokenBudgetWarn},
		{1000, TokenBudgetExceeded},
		{1199, TokenBudgetExceeded},
		{1200, TokenBudgetCritical},
		{5000, TokenBudgetCritical},
	}
	for _, tc := range cases {
		assessment := AssessTokenBudget(budget, tc.used, "")
		assert.Equal(t, tc.status, assessment.Status, "used=%d", tc.used)
		assert.InDelta(t, float64(tc.used)/10, assessment.UsedPercent, 0.01)
	}
}

// TestAssessTokenBudgetNotifiesOnlyOnBandChange is the "one notice per band"
// rule: repeating "83% used" every call trains people to ignore it.
func TestAssessTokenBudgetNotifiesOnlyOnBandChange(t *testing.T) {
	budget := TokenBudget{LimitTokens: 1000, WarnPercent: 80}

	first := AssessTokenBudget(budget, 850, "")
	assert.True(t, first.ShouldNotify, "crossing into warn notifies")

	same := AssessTokenBudget(budget, 900, TokenBudgetWarn)
	assert.False(t, same.ShouldNotify, "still in warn: silent")

	worse := AssessTokenBudget(budget, 1000, TokenBudgetWarn)
	assert.True(t, worse.ShouldNotify, "crossing into exceeded notifies")

	critical := AssessTokenBudget(budget, 1300, TokenBudgetExceeded)
	assert.True(t, critical.ShouldNotify)

	// Usage does not fall within a window, but a previous band may be ahead
	// (e.g. a budget was raised): that is not a notification.
	relaxed := AssessTokenBudget(budget, 500, TokenBudgetCritical)
	assert.Equal(t, TokenBudgetOK, relaxed.Status)
	assert.False(t, relaxed.ShouldNotify, "improvement never notifies")
}

// TestAssessTokenBudgetWithoutLimitIsNotZero covers the unset case: "no budget"
// must not read as "over budget".
func TestAssessTokenBudgetWithoutLimitIsNotZero(t *testing.T) {
	assessment := AssessTokenBudget(TokenBudget{TenantID: 7}, 1_000_000, "")
	assert.Equal(t, TokenBudgetOK, assessment.Status)
	assert.Zero(t, assessment.UsedPercent)
	assert.NotEmpty(t, assessment.Note)
	assert.False(t, assessment.ShouldNotify)
}

// TestAssessTokenBudgetCustomWarnPercent honors the configured threshold.
func TestAssessTokenBudgetCustomWarnPercent(t *testing.T) {
	budget := TokenBudget{LimitTokens: 1000, WarnPercent: 50}
	assert.Equal(t, TokenBudgetOK, AssessTokenBudget(budget, 400, "").Status)
	assert.Equal(t, TokenBudgetWarn, AssessTokenBudget(budget, 500, "").Status)
}

// TestNormalizeTokenBudget covers validation and defaults.
func TestNormalizeTokenBudget(t *testing.T) {
	_, err := NormalizeTokenBudget(TokenBudget{Period: "weekly"})
	require.Error(t, err, "an unknown period is rejected")

	_, err = NormalizeTokenBudget(TokenBudget{
		Period: TokenBudgetDaily, LimitTokens: -1,
	})
	require.Error(t, err)

	normalized, err := NormalizeTokenBudget(TokenBudget{Period: TokenBudgetDaily})
	require.NoError(t, err)
	assert.Equal(t, DefaultTokenBudgetWarnPercent, normalized.WarnPercent,
		"a missing warn percent takes the default")

	outOfRange, err := NormalizeTokenBudget(TokenBudget{
		Period: TokenBudgetDaily, WarnPercent: 150,
	})
	require.NoError(t, err)
	assert.Equal(t, DefaultTokenBudgetWarnPercent, outOfRange.WarnPercent,
		"an out-of-range warn percent is replaced, not clamped to 100")
}

// TestTokenBudgetWindowStartUsesUTC is the shared-clock rule: the usage table's
// day column is a UTC calendar day, so the window must be too.
func TestTokenBudgetWindowStartUsesUTC(t *testing.T) {
	daily := TokenBudgetWindowStart(TokenBudgetDaily, budgetTestNow)
	assert.Equal(t, time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC), daily)

	monthly := TokenBudgetWindowStart(TokenBudgetMonthly, budgetTestNow)
	assert.Equal(t, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), monthly)

	// A local-time instant is converted, not truncated in its own zone.
	local := time.Date(2026, 11, 5, 2, 0, 0, 0, time.FixedZone("UTC+8", 8*3600))
	assert.Equal(t, time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC),
		TokenBudgetWindowStart(TokenBudgetDaily, local),
		"02:00 UTC+8 is the previous UTC day")
}

// TestNormalizeModelFallbackChainDedupes proves a repeated model is dropped:
// retrying the same model with the same parameters just doubles the latency.
func TestNormalizeModelFallbackChainDedupes(t *testing.T) {
	chain, err := NormalizeModelFallbackChain(ModelFallbackChain{
		Scene:    " chat ",
		ModelIDs: []string{"m1", " m2 ", "m1", "", "  ", "m3"},
	})
	require.NoError(t, err)
	assert.Equal(t, "chat", chain.Scene)
	assert.Equal(t, []string{"m1", "m2", "m3"}, chain.ModelIDs)
}

// TestNormalizeModelFallbackChainRequiresModels keeps an empty chain out.
func TestNormalizeModelFallbackChainRequiresModels(t *testing.T) {
	_, err := NormalizeModelFallbackChain(ModelFallbackChain{Scene: "chat"})
	require.Error(t, err)
	_, err = NormalizeModelFallbackChain(ModelFallbackChain{
		Scene: "chat", ModelIDs: []string{"  "},
	})
	require.Error(t, err)
	_, err = NormalizeModelFallbackChain(ModelFallbackChain{
		ModelIDs: []string{"m1"},
	})
	require.Error(t, err, "scene is required")
}

// TestNormalizeModelFallbackChainTruncates bounds the worst-case latency.
func TestNormalizeModelFallbackChainTruncates(t *testing.T) {
	ids := []string{"m1", "m2", "m3", "m4", "m5", "m6"}
	chain, err := NormalizeModelFallbackChain(ModelFallbackChain{
		Scene: "chat", ModelIDs: ids,
	})
	require.NoError(t, err)
	assert.Len(t, chain.ModelIDs, MaxModelFallbackChain)
	assert.Equal(t, ids[:MaxModelFallbackChain], chain.ModelIDs,
		"the earliest entries are kept: they are the most preferred")
}

// TestCandidatesForUnconfiguredChainIsJustThePrimary keeps default behaviour
// identical to before the feature existed.
func TestCandidatesForUnconfiguredChainIsJustThePrimary(t *testing.T) {
	var empty ModelFallbackChain
	assert.Equal(t, []string{"primary"}, empty.CandidatesFor("primary"))
	assert.Empty(t, empty.CandidatesFor("  "))
	assert.Empty(t, empty.PrimaryModelID())
}

// TestCandidatesForConfiguredChainIsTheChain ensures the configured order wins.
func TestCandidatesForConfiguredChainIsTheChain(t *testing.T) {
	chain := ModelFallbackChain{ModelIDs: []string{"a", "b", "c"}}
	assert.Equal(t, []string{"a", "b", "c"}, chain.CandidatesFor("ignored"))
	assert.Equal(t, "a", chain.PrimaryModelID())
}

// TestDescribeFallbackOutcome covers the audit rendering.
func TestDescribeFallbackOutcome(t *testing.T) {
	assert.Equal(t, "primary model served the call",
		DescribeFallbackOutcome(ModelFallbackOutcome{
			ModelID: "m1", Attempts: []ModelFallbackAttempt{{ModelID: "m1"}},
		}))

	degraded := DescribeFallbackOutcome(ModelFallbackOutcome{
		ModelID:  "m2",
		Degraded: true,
		Attempts: []ModelFallbackAttempt{
			{ModelID: "m1", Level: 0, Err: "timeout"},
			{ModelID: "m2", Level: 1},
		},
	})
	assert.Contains(t, degraded, "degraded to m2")
	assert.Contains(t, degraded, "1 failed")

	exhausted := DescribeFallbackOutcome(ModelFallbackOutcome{
		Exhausted: true,
		Attempts: []ModelFallbackAttempt{
			{ModelID: "m1", Level: 0, Err: "timeout"},
			{ModelID: "m2", Level: 1, Err: "quota"},
		},
	})
	assert.Contains(t, exhausted, "every model failed")
	assert.Contains(t, exhausted, "2 tried")
}

// TestTokenBudgetStatusSeverityRank pins the ordering used by "crossed a band".
func TestTokenBudgetStatusSeverityRank(t *testing.T) {
	assert.Less(t, TokenBudgetOK.severityRank(), TokenBudgetWarn.severityRank())
	assert.Less(t, TokenBudgetWarn.severityRank(), TokenBudgetExceeded.severityRank())
	assert.Less(t, TokenBudgetExceeded.severityRank(), TokenBudgetCritical.severityRank())
}
