package types

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// explainNow is the reference instant for freshness math.
var explainNow = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

// explainItem builds a recommendation item with a reason.
func explainItem(reason RecommendationReason, values ...func(*RecommendationItem)) RecommendationItem {
	item := RecommendationItem{
		KnowledgeID: "doc-1",
		Title:       "报警处理规程",
		Reason:      reason,
		Score:       10,
		UpdatedAt:   explainNow.Add(-24 * time.Hour),
	}
	for _, apply := range values {
		apply(&item)
	}
	return item
}

// TestExplanationNamesTheSharedTag is the caption requirement: "因为相关" is
// not an explanation, "共享标签：E45" is.
func TestExplanationNamesTheSharedTag(t *testing.T) {
	item := explainItem(RecommendReasonSameTag, func(i *RecommendationItem) {
		i.MatchedTag = "E45"
	})
	explanation := BuildRecommendationExplanation(
		item, DefaultRecommendationWeights(), explainNow, 27, 0)

	require.NotEmpty(t, explanation.Signals)
	assert.Equal(t, RecommendationSignalSharedTag, explanation.Signals[0].Type)
	assert.Contains(t, explanation.Summary, "E45")
	assert.Contains(t, explanation.Signals[0].Evidence, "E45")
	assert.InDelta(t, 2.0, explanation.Signals[0].Contribution, 0.001,
		"the contribution is the configured weight")
}

// TestExplanationNamesTheInterestTag covers the "猜你想学" caption.
func TestExplanationNamesTheInterestTag(t *testing.T) {
	item := explainItem(RecommendReasonInterest, func(i *RecommendationItem) {
		i.MatchedTag = "设备保养"
	})
	explanation := BuildRecommendationExplanation(
		item, DefaultRecommendationWeights(), explainNow, 23, 0)
	require.NotEmpty(t, explanation.Signals)
	assert.Equal(t, RecommendationSignalInterest, explanation.Signals[0].Type)
	assert.Contains(t, explanation.Summary, "设备保养")
}

// TestExplanationReportsViewCount proves the hot signal carries its raw number.
func TestExplanationReportsViewCount(t *testing.T) {
	item := explainItem(RecommendReasonHot)
	explanation := BuildRecommendationExplanation(
		item, DefaultRecommendationWeights(), explainNow, 60, 12)
	require.NotEmpty(t, explanation.Signals)

	hot := explanation.Signals[0]
	assert.Equal(t, RecommendationSignalHot, hot.Type)
	assert.Contains(t, hot.Evidence, "12")
	assert.InDelta(t, 12.0, hot.Contribution, 0.001, "1 point per view")
}

// TestExplanationIncludesFreshnessIndependently keeps the decay visible even
// when the primary reason is a different signal.
func TestExplanationIncludesFreshnessIndependently(t *testing.T) {
	item := explainItem(RecommendReasonSameTag, func(i *RecommendationItem) {
		i.MatchedTag = "E45"
	})
	explanation := BuildRecommendationExplanation(
		item, DefaultRecommendationWeights(), explainNow, 20, 0)

	byType := map[RecommendationSignalType]RecommendationSignal{}
	for _, signal := range explanation.Signals {
		byType[signal.Type] = signal
	}
	require.Contains(t, byType, RecommendationSignalSharedTag)
	require.Contains(t, byType, RecommendationSignalFreshness)

	// One day old with a one-week half-life ≈ 0.906 × 5.
	freshness := byType[RecommendationSignalFreshness]
	assert.InDelta(t, 5*0.906, freshness.Contribution, 0.05)
	assert.Contains(t, freshness.Evidence, "2026-11-03")
}

// TestExplanationOrdersSignalsByContribution is what makes the caption useful:
// the first line must be the signal that actually decided the rank.
func TestExplanationOrdersSignalsByContribution(t *testing.T) {
	// A relation match (10) beats a shared tag (2) beats freshness (≤5 but
	// this item is old, so ~0).
	item := explainItem(RecommendReasonRelated, func(i *RecommendationItem) {
		i.MatchedTag = "based_on"
		i.UpdatedAt = explainNow.AddDate(-1, 0, 0)
	})
	explanation := BuildRecommendationExplanation(
		item, DefaultRecommendationWeights(), explainNow, 12, 0)
	require.GreaterOrEqual(t, len(explanation.Signals), 2)
	assert.Equal(t, RecommendationSignalRelation, explanation.Signals[0].Type)
	assert.True(t, explanation.Signals[0].Primary,
		"the primary signal leads even when a shared bonus outscores it")
	// Everything after the primary is ordered by contribution.
	for i := 2; i < len(explanation.Signals); i++ {
		assert.GreaterOrEqual(t,
			explanation.Signals[i-1].Contribution, explanation.Signals[i].Contribution,
			"secondary signals are ordered by contribution")
	}
}

// TestExplanationSummaryUsesAtMostTwoSignals keeps the caption short.
func TestExplanationSummaryUsesAtMostTwoSignals(t *testing.T) {
	item := explainItem(RecommendReasonSameTag, func(i *RecommendationItem) {
		i.MatchedTag = "E45"
	})
	explanation := BuildRecommendationExplanation(
		item, DefaultRecommendationWeights(), explainNow, 5, 0)
	require.Len(t, explanation.Signals, 2, "tag + freshness")
	// At most two evidence fragments joined by the separator.
	assert.LessOrEqual(t, countOccurrences(explanation.Summary, "；"), 1)
}

// countOccurrences counts non-overlapping occurrences of needle.
func countOccurrences(haystack, needle string) int {
	count := 0
	for {
		index := indexOf(haystack, needle)
		if index < 0 {
			return count
		}
		count++
		haystack = haystack[index+len(needle):]
	}
}

// indexOf is a local helper so the test does not import strings.
func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// TestExplanationSameSpaceFallbackIsHonest is the honesty rule: the fallback
// signal must not pretend to be an interest match.
func TestExplanationSameSpaceFallbackIsHonest(t *testing.T) {
	item := explainItem(RecommendReasonSameSpace, func(i *RecommendationItem) {
		i.KnowledgeBaseName = "设备运维空间"
	})
	explanation := BuildRecommendationExplanation(
		item, DefaultRecommendationWeights(), explainNow, 9, 0)
	require.NotEmpty(t, explanation.Signals)
	assert.Equal(t, RecommendationSignalSameSpace, explanation.Signals[0].Type)
	assert.Contains(t, explanation.Signals[0].Evidence, "设备运维空间")
}

// TestExplanationLatestUsesTheDate covers the "最新动态" caption.
func TestExplanationLatestUsesTheDate(t *testing.T) {
	item := explainItem(RecommendReasonLatest)
	explanation := BuildRecommendationExplanation(
		item, DefaultRecommendationWeights(), explainNow, 6, 0)
	require.NotEmpty(t, explanation.Signals)
	assert.Equal(t, RecommendationSignalLatest, explanation.Signals[0].Type)
	assert.Contains(t, explanation.Signals[0].Evidence, "2026-11-03")
}

// TestExplanationWithoutEvidenceStaysEmpty keeps a caption from being invented.
func TestExplanationWithoutEvidenceStaysEmpty(t *testing.T) {
	item := RecommendationItem{
		KnowledgeID: "doc-1",
		Reason:      RecommendationReason("unknown"),
	}
	explanation := BuildRecommendationExplanation(
		item, DefaultRecommendationWeights(), explainNow, 0, 0)
	assert.Empty(t, explanation.Signals)
	assert.Empty(t, explanation.Summary,
		"no evidence means no caption, not an empty sentence")
}

// TestExplanationEchoesTotalScore lets a reader reconcile the panel with the
// rank.
func TestExplanationEchoesTotalScore(t *testing.T) {
	explanation := BuildRecommendationExplanation(
		explainItem(RecommendReasonHot), DefaultRecommendationWeights(), explainNow, 42.5, 3)
	assert.InDelta(t, 42.5, explanation.Score, 0.001)
}

// TestExplanationReportsAge covers the freshness hours field.
func TestExplanationReportsAge(t *testing.T) {
	item := explainItem(RecommendReasonSameTag, func(i *RecommendationItem) {
		i.UpdatedAt = explainNow.Add(-48 * time.Hour)
	})
	explanation := BuildRecommendationExplanation(
		item, DefaultRecommendationWeights(), explainNow, 2, 0)
	assert.InDelta(t, 48.0, explanation.FreshnessHours, 0.001)
}

// TestExplanationDisablesFreshnessWhenHalfLifeIsZero documents the config edge.
func TestExplanationDisablesFreshnessWhenHalfLifeIsZero(t *testing.T) {
	weights := DefaultRecommendationWeights()
	weights.FreshnessHalfLifeHours = 0
	item := explainItem(RecommendReasonSameTag, func(i *RecommendationItem) {
		i.MatchedTag = "E45"
	})
	explanation := BuildRecommendationExplanation(item, weights, explainNow, 2, 0)
	for _, signal := range explanation.Signals {
		assert.NotEqual(t, RecommendationSignalFreshness, signal.Type,
			"a zero half-life turns the freshness term off entirely")
	}
}

// TestExplainableBoardAttachesToEveryList proves the board transformation is
// complete and does not reorder anything.
func TestExplainableBoardAttachesToEveryList(t *testing.T) {
	board := RecommendationBoard{
		Related: []RecommendationItem{
			explainItem(RecommendReasonRelated),
			explainItem(RecommendReasonRelated, func(i *RecommendationItem) {
				i.KnowledgeID = "doc-2"
			}),
		},
		ForYou: []RecommendationItem{explainItem(RecommendReasonInterest)},
		Hot:    []RecommendationItem{explainItem(RecommendReasonHot)},
		Latest: []RecommendationItem{explainItem(RecommendReasonLatest)},
	}
	viewsCalls := 0
	explained := ExplainableBoard(board, DefaultRecommendationWeights(), explainNow,
		func(string) int64 { viewsCalls++; return 7 })

	for label, items := range map[string][]RecommendationItem{
		"related": explained.Related, "forYou": explained.ForYou,
		"hot": explained.Hot, "latest": explained.Latest,
	} {
		for _, item := range items {
			require.NotNil(t, item.Explanation, "%s: every item gets a panel", label)
			assert.NotEmpty(t, item.Explanation.Signals, label)
		}
	}
	assert.Equal(t, 5, viewsCalls, "views are looked up once per item")

	// Ordering is preserved: the explanation never re-ranks.
	assert.Equal(t, "doc-1", explained.Related[0].KnowledgeID)
	assert.Equal(t, "doc-2", explained.Related[1].KnowledgeID)
}

// TestExplainableBoardHandlesEmptyLists keeps the empty case allocation-free.
func TestExplainableBoardHandlesEmptyLists(t *testing.T) {
	board := RecommendationBoard{
		Related: []RecommendationItem{}, ForYou: []RecommendationItem{},
		Hot: []RecommendationItem{}, Latest: []RecommendationItem{},
	}
	explained := ExplainableBoard(board, DefaultRecommendationWeights(), explainNow, nil)
	assert.Empty(t, explained.Related)
	assert.Empty(t, explained.ForYou)
	assert.Empty(t, explained.Hot)
	assert.Empty(t, explained.Latest)
}

// TestExplainableBoardHandlesNilViewCounter proves the callback is optional.
func TestExplainableBoardHandlesNilViewCounter(t *testing.T) {
	board := RecommendationBoard{
		Hot: []RecommendationItem{explainItem(RecommendReasonHot)},
	}
	explained := ExplainableBoard(board, DefaultRecommendationWeights(), explainNow, nil)
	require.Len(t, explained.Hot, 1)
	require.NotNil(t, explained.Hot[0].Explanation)
	assert.NotEmpty(t, explained.Hot[0].Explanation.Signals)
}

// TestDefaultWeightsMatchServiceConstants is a guard against drift.
//
// 解释面板回显的贡献分必须与实际排序用的权重一致；两处不一致会让"为什么
// 这条排第一"给出错误答案，而这种偏差在生产里极难被发现。
func TestDefaultWeightsMatchServiceConstants(t *testing.T) {
	weights := DefaultRecommendationWeights()
	assert.InDelta(t, 10, weights.RelationEdge, 0.001)
	assert.InDelta(t, 2, weights.SharedTag, 0.001)
	assert.InDelta(t, 1, weights.SameSpace, 0.001)
	assert.InDelta(t, 3, weights.InterestTag, 0.001)
	assert.InDelta(t, 1, weights.HotPerView, 0.001)
	assert.InDelta(t, 168, weights.FreshnessHalfLifeHours, 0.001)
	// The freshness ceiling must match service.baseScore's literal.
	assert.InDelta(t, 5, recommendationFreshnessMax, 0.001)
}
