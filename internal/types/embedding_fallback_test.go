package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCheckEmbeddingFallbackSameModelIsNotADegrade keeps the normal path free.
func TestCheckEmbeddingFallbackSameModelIsNotADegrade(t *testing.T) {
	decision := CheckEmbeddingFallback("m1", 1024, EmbeddingFallbackCandidate{
		ModelID: "m1", Dimensions: 1024,
	})
	assert.True(t, decision.Allowed)
	assert.False(t, decision.Lossy, "the index model is not a degrade")
	assert.Empty(t, decision.Reason)
}

// TestCheckEmbeddingFallbackRejectsDimensionMismatch is the hard constraint:
// a differently-sized vector cannot be compared with the index at all.
func TestCheckEmbeddingFallbackRejectsDimensionMismatch(t *testing.T) {
	decision := CheckEmbeddingFallback("m1", 1024, EmbeddingFallbackCandidate{
		ModelID: "m2", Dimensions: 768,
	})
	assert.False(t, decision.Allowed)
	assert.Contains(t, decision.Reason, "dimension mismatch")
	assert.Contains(t, decision.Reason, "1024")
	assert.Contains(t, decision.Reason, "768")
}

// TestCheckEmbeddingFallbackRejectsUnknownDimensions is fail-closed: an
// unverifiable candidate must not be used, because the failure mode is silent.
func TestCheckEmbeddingFallbackRejectsUnknownDimensions(t *testing.T) {
	// Unknown index dimension: nothing can be checked.
	assert.False(t, CheckEmbeddingFallback("m1", 0, EmbeddingFallbackCandidate{
		ModelID: "m2", Dimensions: 1024,
	}).Allowed)

	// Unknown candidate dimension.
	assert.False(t, CheckEmbeddingFallback("m1", 1024, EmbeddingFallbackCandidate{
		ModelID: "m2",
	}).Allowed)
}

// TestCheckEmbeddingFallbackMarksSameDimensionAsLossy is the honest part: two
// models can both emit 1024 dimensions without sharing an embedding space.
func TestCheckEmbeddingFallbackMarksSameDimensionAsLossy(t *testing.T) {
	decision := CheckEmbeddingFallback("m1", 1024, EmbeddingFallbackCandidate{
		ModelID: "m2", Dimensions: 1024, Name: "backup-embed",
	})
	assert.True(t, decision.Allowed)
	assert.True(t, decision.Lossy,
		"a different model is a different vector space, even at the same width")
	assert.Contains(t, decision.Reason, "approximate")
}

// TestCheckEmbeddingFallbackRejectsBlankModelID covers the degenerate candidate.
func TestCheckEmbeddingFallbackRejectsBlankModelID(t *testing.T) {
	decision := CheckEmbeddingFallback("m1", 1024, EmbeddingFallbackCandidate{})
	assert.False(t, decision.Allowed)
	assert.Contains(t, decision.Reason, "no model id")
}

// TestDescribeEmbeddingFallback covers the audit rendering.
func TestDescribeEmbeddingFallback(t *testing.T) {
	assert.Equal(t, "index model served the query embedding",
		DescribeEmbeddingFallback(EmbeddingFallbackTrace{
			IndexModelID: "m1", ServedBy: "m1",
		}))

	lossy := DescribeEmbeddingFallback(EmbeddingFallbackTrace{
		IndexModelID: "m1", ServedBy: "m2", Degraded: true, Lossy: true,
	})
	assert.Contains(t, lossy, "degraded to m2")
	assert.Contains(t, lossy, "lossy")
	assert.Contains(t, lossy, "m1", "the index model is named for comparison")

	exact := DescribeEmbeddingFallback(EmbeddingFallbackTrace{
		IndexModelID: "m1", ServedBy: "m2", Degraded: true, Lossy: false,
	})
	assert.Contains(t, exact, "degraded to m2")
	assert.NotContains(t, exact, "lossy")
}

// TestEmbeddingFallbackSceneMatchesUsage pins the shared key: quotas and
// fallbacks must agree on the scene name or reports will not join.
func TestEmbeddingFallbackSceneMatchesUsage(t *testing.T) {
	assert.Equal(t, ModelUsageSceneEmbedding, EmbeddingFallbackScene)
	assert.Equal(t, "embedding", EmbeddingFallbackScene)
}
