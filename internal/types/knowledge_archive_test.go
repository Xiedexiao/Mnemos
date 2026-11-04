package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseLifecycleStatusList covers the archive-library filter parser.
func TestParseLifecycleStatusList(t *testing.T) {
	statuses, err := ParseLifecycleStatusList("archived")
	require.NoError(t, err)
	assert.Equal(t, []string{"archived"}, statuses)

	statuses, err = ParseLifecycleStatusList("archived, expired，delisted")
	require.NoError(t, err)
	assert.Equal(t, []string{"archived", "expired", "delisted"}, statuses)

	statuses, err = ParseLifecycleStatusList("archived,archived")
	require.NoError(t, err)
	assert.Equal(t, []string{"archived"}, statuses, "duplicates collapse")

	statuses, err = ParseLifecycleStatusList("  ")
	require.NoError(t, err)
	assert.Nil(t, statuses, "an empty filter means no restriction")

	// A typo must fail loudly: a silently empty result reads as "the archive
	// library is empty".
	_, err = ParseLifecycleStatusList("archived,archivd")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "archivd")
}

// TestParseLifecycleStatusListAcceptsEveryDefinedStatus keeps the whitelist
// and the state machine in step.
func TestParseLifecycleStatusListAcceptsEveryDefinedStatus(t *testing.T) {
	for _, status := range []KnowledgeLifecycleStatus{
		KnowledgeLifecycleDraft, KnowledgeLifecyclePendingReview,
		KnowledgeLifecyclePendingEffective, KnowledgeLifecycleValid,
		KnowledgeLifecyclePendingReviewCycle, KnowledgeLifecycleStale,
		KnowledgeLifecycleExpired, KnowledgeLifecycleReplaced,
		KnowledgeLifecycleConflict, KnowledgeLifecycleDelisted,
		KnowledgeLifecycleArchived,
	} {
		_, err := ParseLifecycleStatusList(string(status))
		require.NoError(t, err, string(status))
	}
}
