package types

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRelevanceWithoutKeywordIsNeutral is the "not measured" rule: a list
// browse carries no query, and recording 0 would flatten every row.
func TestRelevanceWithoutKeywordIsNeutral(t *testing.T) {
	assert.Equal(t, NeutralRelevance, RelevanceForKeyword("E45 报警处理", "", "  "))
	assert.Equal(t, NeutralRelevance, RelevanceForKeyword("", "", ""))
}

// TestRelevanceExactMatchScoresHighest covers the tiers.
func TestRelevanceExactMatchScoresHighest(t *testing.T) {
	assert.Equal(t, 1.0, RelevanceForKeyword("E45 报警处理", "", "E45 报警处理"))
	// Whitespace and case are normalized away before comparison.
	assert.Equal(t, 1.0, RelevanceForKeyword("e45报警处理", "", "E45 报警处理"))
	assert.Equal(t, 0.75, RelevanceForKeyword("E45 报警处理 SOP", "", "报警处理"))
	// Subsequence: the query's characters appear in order but not contiguously.
	assert.Equal(t, 0.5, RelevanceForKeyword("E45 报警处理", "", "E4报处"))
	assert.Equal(t, 0.0, RelevanceForKeyword("备件清单", "", "报警"))
}

// TestRelevanceFallsBackToFileName covers rows whose display name is the file.
func TestRelevanceFallsBackToFileName(t *testing.T) {
	// Extensions and separators are normalized away, so the query is a
	// contained prefix of "alarmhandlingmd" rather than an exact match.
	assert.Equal(t, 0.75, RelevanceForKeyword("", "alarm-handling.md", "alarmhandling"))
	assert.Equal(t, 0.75, RelevanceForKeyword("", "alarm-handling.md", "handling"))
	// A stale row whose title carries the exact term still scores 1.0.
	assert.Equal(t, 1.0, RelevanceForKeyword("", "alarmhandling", "alarmhandling"))
}

// TestAuthorityUsesTheStrongestSignal is the "max, not sum" rule: an L3
// document is authoritative regardless of its classification.
func TestAuthorityUsesTheStrongestSignal(t *testing.T) {
	score, reason := AuthorityForKnowledge("L3", "internal", "valid")
	assert.Equal(t, 1.0, score)
	assert.Equal(t, "L3 权威", reason)

	// L2 beats the public floor.
	l2, _ := AuthorityForKnowledge("L2", "internal", "valid")
	assert.InDelta(t, 0.7, l2, 0.001)

	// A plain document still has baseline authority.
	plain, _ := AuthorityForKnowledge("", "internal", "valid")
	assert.InDelta(t, 0.5, plain, 0.001, "valid lifecycle lifts the 0.3 baseline")

	// A stale plain document keeps only the baseline.
	stale, _ := AuthorityForKnowledge("", "internal", "stale")
	assert.InDelta(t, 0.3, stale, 0.001, "a stale entry gets no lifecycle floor")
}

// TestAuthorityPublicLiftsBaseline covers the classification floor.
func TestAuthorityPublicLiftsBaseline(t *testing.T) {
	score, _ := AuthorityForKnowledge("", "public", "stale")
	assert.InDelta(t, 0.6, score, 0.001)
}

// TestHotnessNormalizesWithinTheGroup is the "组内归一" rule.
func TestHotnessNormalizesWithinTheGroup(t *testing.T) {
	inputs := []CompositeRankInput{
		{KnowledgeID: "cold"},
		{KnowledgeID: "warm", Views: 10},
		{KnowledgeID: "hot", Views: 20},
	}
	scores := HotnessForKnowledges(inputs)
	assert.Equal(t, 0.0, scores["cold"])
	assert.InDelta(t, 0.5, scores["warm"], 0.001)
	assert.InDelta(t, 1.0, scores["hot"], 0.001)
}

// TestHotnessWeightsCitationsOverViews pins the counter weights.
func TestHotnessWeightsCitationsOverViews(t *testing.T) {
	// 30 views vs 10 citations: 30 vs 30 → tie, both normalized to 1.
	tied := HotnessForKnowledges([]CompositeRankInput{
		{KnowledgeID: "views", Views: 30},
		{KnowledgeID: "citations", Citations: 10},
	})
	assert.InDelta(t, 1.0, tied["views"], 0.001)
	assert.InDelta(t, 1.0, tied["citations"], 0.001)

	// Citations weigh 3× a view: 2 citations (weight 6) outrank 3 views (3).
	weighted := HotnessForKnowledges([]CompositeRankInput{
		{KnowledgeID: "views", Views: 3},
		{KnowledgeID: "citation", Citations: 2},
	})
	assert.Greater(t, weighted["citation"], weighted["views"])
}

// TestHotnessWithoutDataStaysZero avoids fabricating "everyone is equally hot".
func TestHotnessWithoutDataStaysZero(t *testing.T) {
	scores := HotnessForKnowledges([]CompositeRankInput{
		{KnowledgeID: "a"}, {KnowledgeID: "b"},
	})
	assert.Equal(t, 0.0, scores["a"])
	assert.Equal(t, 0.0, scores["b"])
	assert.Empty(t, HotnessForKnowledges(nil))
}

// TestScoreCompositeRanksByWeightedSum is the core behaviour: the composite
// beat both single-dimension rankings.
//
// 算术（无障碍值，无关键词时相关度=0.5、新鲜度则按 §14.1 计）：
//
//	popular       0.40×0.5 + 0.20×1.0 + 0.25×0.5 + 0.15×f ≈ 0.575 + 0.15f
//	authoritative 0.40×0.5 + 0.20×0.0 + 0.25×1.0 + 0.15×f ≈ 0.450 + 0.15f
//
// 热度绝对领先的那条仍然排第一——这正是"综合"的含义：它不该被权威性
// 无条件压过，但也不该只凭热度取胜（见下一个断言）。
func TestScoreCompositeRanksByWeightedSum(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	inputs := []CompositeRankInput{
		{KnowledgeID: "popular", Title: "闲聊记录", Views: 1000, LifecycleStatus: "valid"},
		{KnowledgeID: "authoritative", Title: "报警处理规程", Level: "L3",
			LifecycleStatus: "valid", CurrentVersion: 3, Views: 1},
	}
	result := ScoreComposite(inputs, "", DefaultCompositeRankWeights(), now)
	require.Len(t, result.Rows, 2)
	assert.Equal(t, "popular", result.Rows[0].KnowledgeID,
		"1000 views vs 1: hotness alone still leads")
	assert.Greater(t, result.Rows[0].Breakdown.Score, result.Rows[1].Breakdown.Score)

	// And the authority dimension is what keeps the L3 document in contention:
	// with the same (zero) hotness it wins outright.
	sameHeat := []CompositeRankInput{
		{KnowledgeID: "plain", Title: "闲聊记录", LifecycleStatus: "valid"},
		{KnowledgeID: "authoritative", Title: "报警处理规程", Level: "L3",
			LifecycleStatus: "valid", CurrentVersion: 3},
	}
	tied := ScoreComposite(sameHeat, "", DefaultCompositeRankWeights(), now)
	assert.Equal(t, "authoritative", tied.Rows[0].KnowledgeID,
		"equal heat: the L3 document leads on authority")
}

// TestScoreCompositeKeywordChangesTheOrder proves relevance participates.
func TestScoreCompositeKeywordChangesTheOrder(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	inputs := []CompositeRankInput{
		{KnowledgeID: "offtopic", Title: "备件清单", Level: "L3", LifecycleStatus: "valid"},
		{KnowledgeID: "ontopic", Title: "报警处理规程", LifecycleStatus: "valid"},
	}
	withoutKeyword := ScoreComposite(inputs, "", DefaultCompositeRankWeights(), now)
	assert.Equal(t, "offtopic", withoutKeyword.Rows[0].KnowledgeID,
		"without a query, authority wins")

	withKeyword := ScoreComposite(inputs, "报警处理规程", DefaultCompositeRankWeights(), now)
	assert.Equal(t, "ontopic", withKeyword.Rows[0].KnowledgeID,
		"a full title match outweighs the L3 bonus")
	assert.Equal(t, "报警处理规程", withKeyword.Keyword)
}

// TestScoreCompositeEchoesEveryDimension is the explainability contract.
func TestScoreCompositeEchoesEveryDimension(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	result := ScoreComposite([]CompositeRankInput{{
		KnowledgeID: "doc", Title: "E45 报警处理", Level: "L2",
		Classification: "internal", LifecycleStatus: "valid",
		Views: 12, Favorites: 2, Citations: 3,
	}}, "报警", DefaultCompositeRankWeights(), now)

	require.Len(t, result.Rows, 1)
	breakdown := result.Rows[0].Breakdown
	assert.InDelta(t, 0.75, breakdown.Relevance.Value, 0.001)
	assert.InDelta(t, 1.0, breakdown.Hotness.Value, 0.001, "the only row is its own maximum")
	assert.InDelta(t, 0.7, breakdown.Authority.Value, 0.001)
	assert.NotEmpty(t, breakdown.Freshness.Raw, "the freshness grade is shown")

	// The score must equal the sum of the echoed contributions, so a reader
	// can recompute it.
	sum := breakdown.Relevance.Contribution + breakdown.Hotness.Contribution +
		breakdown.Authority.Contribution + breakdown.Freshness.Contribution
	assert.InDelta(t, sum, breakdown.Score, 0.0002)
	assert.Contains(t, breakdown.Hotness.Raw, "views=12")
	assert.Contains(t, breakdown.Hotness.Raw, "citations=3")
	assert.Equal(t, "L2 已确认", breakdown.Authority.Raw)
}

// TestScoreCompositeWeightsMatchDefaults keeps the echo honest.
func TestScoreCompositeWeightsMatchDefaults(t *testing.T) {
	result := ScoreComposite(nil, "x", DefaultCompositeRankWeights(), time.Now())
	assert.Equal(t, DefaultCompositeRankWeights(), result.Weights)
	assert.Empty(t, result.Rows)
}

// TestScoreCompositeIsDeterministic pins the tie-break.
func TestScoreCompositeIsDeterministic(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	inputs := []CompositeRankInput{
		{KnowledgeID: "b", Title: "same", LifecycleStatus: "valid"},
		{KnowledgeID: "a", Title: "same", LifecycleStatus: "valid"},
		{KnowledgeID: "c", Title: "same", LifecycleStatus: "valid"},
	}
	first := ScoreComposite(inputs, "", DefaultCompositeRankWeights(), now)
	require.Len(t, first.Rows, 3)
	// Identical scores tie-break on the knowledge id.
	assert.Equal(t, "a", first.Rows[0].KnowledgeID)
	assert.Equal(t, "b", first.Rows[1].KnowledgeID)
	assert.Equal(t, "c", first.Rows[2].KnowledgeID)

	for i := 0; i < 5; i++ {
		assert.Equal(t, first.Rows, ScoreComposite(inputs, "", DefaultCompositeRankWeights(), now).Rows)
	}
}

// TestScoreCompositeFreshnessDeclinesWithAge proves the dimension is live.
func TestScoreCompositeFreshnessDeclinesWithAge(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	old := now.AddDate(-3, 0, 0)
	inputs := []CompositeRankInput{
		{KnowledgeID: "fresh", Title: "t", LifecycleStatus: "valid",
			CreatedAt: now.AddDate(0, 0, -1), UpdatedAt: now, CurrentVersion: 2},
		{KnowledgeID: "old", Title: "t", LifecycleStatus: "valid",
			CreatedAt: old, UpdatedAt: old, CurrentVersion: 1},
	}
	result := ScoreComposite(inputs, "", DefaultCompositeRankWeights(), now)
	freshness := map[string]float64{}
	for _, row := range result.Rows {
		freshness[row.KnowledgeID] = row.Breakdown.Freshness.Value
	}
	assert.Greater(t, freshness["fresh"], freshness["old"],
		"a recently refreshed document scores higher on freshness")
}

// TestScoreCompositeExpiredLowersFreshness covers the expiry input.
func TestScoreCompositeExpiredLowersFreshness(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	past := now.AddDate(0, 0, -1)
	result := ScoreComposite([]CompositeRankInput{{
		KnowledgeID: "expired", Title: "t", LifecycleStatus: "valid",
		CreatedAt: now, UpdatedAt: now, ExpiresAt: &past,
	}}, "", DefaultCompositeRankWeights(), now)
	assert.Less(t, result.Rows[0].Breakdown.Freshness.Value, 0.5,
		"an expired entry must not score well on freshness")
}

// TestValidCompositeRankWeights rejects sets that do not sum to one.
func TestValidCompositeRankWeights(t *testing.T) {
	assert.True(t, ValidCompositeRankWeights(DefaultCompositeRankWeights()))
	assert.False(t, ValidCompositeRankWeights(CompositeRankWeights{
		Relevance: 0.5, Hotness: 0.5, Authority: 0.5, Freshness: 0.5,
	}), "summing above one would scale every score past the unit interval")
	assert.False(t, ValidCompositeRankWeights(CompositeRankWeights{
		Relevance: -0.1, Hotness: 0.6, Authority: 0.25, Freshness: 0.25,
	}))
}

// TestDefaultCompositeRankWeightsSumToOne pins the documented invariant.
func TestDefaultCompositeRankWeightsSumToOne(t *testing.T) {
	weights := DefaultCompositeRankWeights()
	assert.InDelta(t, 1.0,
		weights.Relevance+weights.Hotness+weights.Authority+weights.Freshness, 1e-9)
	// Relevance first, authority over hotness: both documented in the type.
	assert.Greater(t, weights.Relevance, weights.Authority)
	assert.Greater(t, weights.Authority, weights.Hotness)
	assert.Greater(t, weights.Hotness, weights.Freshness)
}
