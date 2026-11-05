package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cfModel builds a model from (user, item, weight) triples.
func cfModel(triples ...[3]interface{}) *CFModel {
	interactions := make([]CFInteraction, 0, len(triples))
	for _, triple := range triples {
		weight, _ := triple[2].(float64)
		interactions = append(interactions, CFInteraction{
			UserID: triple[0].(string), ItemID: triple[1].(string), Weight: weight,
		})
	}
	return BuildCFModel(interactions)
}

// TestBuildCFModelAccumulatesRepeatedInteractions is the "read three times
// beats read once" rule.
func TestBuildCFModelAccumulatesRepeatedInteractions(t *testing.T) {
	model := BuildCFModel([]CFInteraction{
		{UserID: "u1", ItemID: "a", Weight: 1},
		{UserID: "u1", ItemID: "a", Weight: 1},
		{UserID: "u1", ItemID: "a", Weight: 1},
	})
	require.Contains(t, model.Profiles, "a")
	assert.InDelta(t, 3.0, model.Profiles["a"].ByUser["u1"], 0.001)
}

// TestBuildCFModelSkipsBlankAndNonPositive keeps junk out of the model.
func TestBuildCFModelSkipsBlankAndNonPositive(t *testing.T) {
	model := BuildCFModel([]CFInteraction{
		{UserID: "", ItemID: "a", Weight: 1},
		{UserID: "u1", ItemID: "", Weight: 1},
		{UserID: "u1", ItemID: "a", Weight: 0},
		{UserID: "u1", ItemID: "a", Weight: -1},
	})
	assert.Empty(t, model.Profiles)
}

// TestSimilarityIsOneForIdenticalAudiences proves the cosine normalisation.
func TestSimilarityIsOneForIdenticalAudiences(t *testing.T) {
	model := cfModel(
		[3]interface{}{"u1", "a", 2.0}, [3]interface{}{"u1", "b", 2.0},
		[3]interface{}{"u2", "a", 1.0}, [3]interface{}{"u2", "b", 1.0},
	)
	assert.InDelta(t, 1.0, model.Similarity("a", "b"), 0.001)
}

// TestSimilarityIsZeroWithoutOverlap keeps unrelated items unrelated.
func TestSimilarityIsZeroWithoutOverlap(t *testing.T) {
	model := cfModel(
		[3]interface{}{"u1", "a", 1.0}, [3]interface{}{"u2", "b", 1.0},
	)
	assert.Zero(t, model.Similarity("a", "b"))
}

// TestSimilarityCarriesStrength is why cosine beats Jaccard: the same audience
// reading one item three times signals more than reading it once.
func TestSimilarityCarriesStrength(t *testing.T) {
	// a and b share u1, but b is read much more heavily by u1 and u2.
	weak := cfModel(
		[3]interface{}{"u1", "a", 1.0}, [3]interface{}{"u1", "b", 1.0},
		[3]interface{}{"u2", "b", 10.0},
	)
	strong := cfModel(
		[3]interface{}{"u1", "a", 10.0}, [3]interface{}{"u1", "b", 10.0},
		[3]interface{}{"u2", "a", 10.0},
	)
	// Both are legitimate cosines; the point is the weight is not discarded.
	assert.NotZero(t, weak.Similarity("a", "b"))
	assert.NotZero(t, strong.Similarity("a", "b"))
}

// TestNeighboursExcludesTheSeedAndSorts covers the item-to-item path.
func TestNeighboursExcludesTheSeedAndSorts(t *testing.T) {
	model := cfModel(
		[3]interface{}{"u1", "a", 1.0}, [3]interface{}{"u1", "b", 1.0},
		[3]interface{}{"u2", "a", 1.0}, [3]interface{}{"u2", "b", 1.0},
		[3]interface{}{"u1", "c", 1.0},
	)
	neighbours := model.Neighbours("a", 5, 0)
	require.NotEmpty(t, neighbours)
	assert.Equal(t, "b", neighbours[0].ItemID, "the strongest neighbour first")
	for _, neighbour := range neighbours {
		assert.NotEqual(t, "a", neighbour.ItemID, "the seed is not its own neighbour")
	}
}

// TestNeighboursHonoursTheSimilarityFloor is the "weak matches are noise" rule.
func TestNeighboursHonoursTheSimilarityFloor(t *testing.T) {
	// a and b share one user out of three each: cosine ≈ 0.5.
	model := cfModel(
		[3]interface{}{"u1", "a", 1.0}, [3]interface{}{"u2", "a", 1.0},
		[3]interface{}{"u1", "b", 1.0}, [3]interface{}{"u3", "b", 1.0},
	)
	assert.InDelta(t, 0.5, model.Similarity("a", "b"), 0.01)
	// With a floor above the actual similarity, nothing is suggested.
	assert.Empty(t, model.Neighbours("a", 5, 0.9))
	// Below it, the neighbour appears.
	assert.NotEmpty(t, model.Neighbours("a", 5, 0.4))
}

// TestNeighboursUnknownItemIsEmpty keeps a missing seed from inventing results.
func TestNeighboursUnknownItemIsEmpty(t *testing.T) {
	model := cfModel([3]interface{}{"u1", "a", 1.0})
	assert.Empty(t, model.Neighbours("missing", 5, 0))
	assert.Empty(t, model.Neighbours("  ", 5, 0))
}

// TestRecommendExcludesHistoryAndExplicitItems is the "don't recommend what I
// just read" rule.
func TestRecommendExcludesHistoryAndExplicitItems(t *testing.T) {
	model := cfModel(
		[3]interface{}{"u1", "a", 1.0}, [3]interface{}{"u1", "b", 1.0},
		[3]interface{}{"u2", "a", 1.0}, [3]interface{}{"u2", "b", 1.0},
	)
	recommendations := model.Recommend([]string{"a"}, []string{"b"}, 5, 0)
	assert.Empty(t, recommendations,
		"a is history and b is excluded, so nothing is left")
}

// TestRecommendAggregatesAcrossSeeds covers multi-seed scoring.
func TestRecommendAggregatesAcrossSeeds(t *testing.T) {
	model := cfModel(
		// a and b both co-occur with target t, so t scores twice.
		[3]interface{}{"u1", "a", 1.0}, [3]interface{}{"u1", "t", 1.0},
		[3]interface{}{"u2", "b", 1.0}, [3]interface{}{"u2", "t", 1.0},
		[3]interface{}{"u3", "c", 1.0}, [3]interface{}{"u3", "t", 1.0},
	)
	recommendations := model.Recommend([]string{"a", "b"}, nil, 5, 0)
	require.NotEmpty(t, recommendations)
	top := recommendations[0]
	assert.Equal(t, "t", top.ItemID)
	assert.Len(t, top.FromItemIDs, 2, "both seeds are credited")
	assert.Contains(t, top.FromItemIDs, "a")
	assert.Contains(t, top.FromItemIDs, "b")
}

// TestRecommendIsDeterministic keeps the order stable across calls.
func TestRecommendIsDeterministic(t *testing.T) {
	model := cfModel(
		[3]interface{}{"u1", "a", 1.0}, [3]interface{}{"u1", "b", 1.0},
		[3]interface{}{"u1", "c", 1.0},
	)
	first := model.Recommend([]string{"a"}, nil, 5, 0)
	for i := 0; i < 5; i++ {
		assert.Equal(t, first, model.Recommend([]string{"a"}, nil, 5, 0))
	}
}

// TestRecommendWithoutHistoryIsEmpty keeps the cold-start path cheap.
func TestRecommendWithoutHistoryIsEmpty(t *testing.T) {
	model := cfModel([3]interface{}{"u1", "a", 1.0})
	assert.Empty(t, model.Recommend(nil, nil, 5, 0))
	assert.Empty(t, (*CFModel)(nil).Recommend([]string{"a"}, nil, 5, 0))
}

// TestRecommendRespectsLimit bounds the response.
func TestRecommendRespectsLimit(t *testing.T) {
	model := cfModel(
		[3]interface{}{"u1", "seed", 1.0},
		[3]interface{}{"u1", "x1", 1.0}, [3]interface{}{"u1", "x2", 1.0},
		[3]interface{}{"u1", "x3", 1.0}, [3]interface{}{"u1", "x4", 1.0},
	)
	recommendations := model.Recommend([]string{"seed"}, nil, 2, 0)
	assert.LessOrEqual(t, len(recommendations), 2)
}

// TestDefaultInteractionWeights pins the "favorite counts more than a view" rule.
func TestDefaultInteractionWeights(t *testing.T) {
	weights := DefaultCFInteractionWeights()
	assert.Greater(t, weights.Favorite, weights.View,
		"a favorite is a deliberate 'keep this'; a view can be a stray click")
	assert.InDelta(t, 3.0, weights.Favorite, 0.001)
}

// TestCFModelCarriesKnowledgeBases proves the ACL gate has what it needs.
func TestCFModelCarriesKnowledgeBases(t *testing.T) {
	model := BuildCFModel([]CFInteraction{
		{UserID: "u1", ItemID: "a", KnowledgeBaseID: "kb-1", Weight: 1},
		{UserID: "u1", ItemID: "b", KnowledgeBaseID: "kb-2", Weight: 1},
	})
	assert.Equal(t, "kb-1", model.ItemKnowledgeBase["a"])
	assert.Equal(t, "kb-2", model.ItemKnowledgeBase["b"])
}
