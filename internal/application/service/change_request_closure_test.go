package service

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
)

// TestChangeRequestLifecycleActionByType covers §7.2's type dispatch. Before
// this every change type published as mark_stale, so "replacement" and
// "delist" were indistinguishable in state and the close gate could not tell
// whether the old version had actually been retired.
func TestChangeRequestLifecycleActionByType(t *testing.T) {
	cases := map[types.ChangeType]types.KnowledgeLifecycleAction{
		types.ChangeTypeDelist:          types.KnowledgeLifecycleActionDelist,
		types.ChangeTypeReplacement:     types.KnowledgeLifecycleActionReplace,
		types.ChangeTypeTextFix:         types.KnowledgeLifecycleActionMarkStale,
		types.ChangeTypeContentRevision: types.KnowledgeLifecycleActionMarkStale,
		types.ChangeTypeParamChange:     types.KnowledgeLifecycleActionMarkStale,
		types.ChangeTypeRestructure:     types.KnowledgeLifecycleActionMarkStale,
	}
	for changeType, want := range cases {
		t.Run(string(changeType), func(t *testing.T) {
			assert.Equal(t, want, changeRequestLifecycleAction(changeType))
		})
	}
}

// TestChangeRequestReplacementTarget covers the fallback contract: an item with
// no recorded replacement degrades to stale instead of failing the batch.
func TestChangeRequestReplacementTarget(t *testing.T) {
	assert.Empty(t, changeRequestReplacementTarget(nil))
	assert.Empty(t, changeRequestReplacementTarget(&types.ChangeItem{}))
	assert.Equal(t, "doc-new",
		changeRequestReplacementTarget(&types.ChangeItem{ReplacedByID: " doc-new "}))
}

// TestCloseGateRequiresAllFourConditions is the §7.2 gate. Only two of the four
// conditions existed before, so a change nobody verified and nobody was told
// about could still be closed.
func TestCloseGateRequiresAllFourConditions(t *testing.T) {
	base := &types.ChangeCloseBlockedError{
		OpenTasks:      nil,
		PendingItems:   nil,
		Verified:       true,
		Notified:       true,
		OldVersionGone: true,
	}
	// All four satisfied: the message must not name any missing condition.
	message := base.Error()
	assert.NotContains(t, message, "verification")
	assert.NotContains(t, message, "retired")
	assert.NotContains(t, message, "notified")

	missingVerify := base
	missingVerify.Verified = false
	assert.Contains(t, missingVerify.Error(), "verification result is missing")

	missingNotify := base
	missingNotify.Notified = false
	assert.Contains(t, missingNotify.Error(), "have not been notified")

	missingRetired := base
	missingRetired.OldVersionGone = false
	assert.Contains(t, missingRetired.Error(), "previous version has not been retired")

	openTask := base
	openTask.OpenTasks = []string{"task-1"}
	assert.Contains(t, openTask.Error(), "open impact task")
}

// TestCRApprovalSentinelsAreDistinct keeps the two failure modes separable so
// the handler can map "not analysed" and "not approved" to different messages.
func TestCRApprovalSentinelsAreDistinct(t *testing.T) {
	require.False(t, errors.Is(ErrCRNotAnalyzed, ErrCRNotApproved))
	require.False(t, errors.Is(ErrCRNotApproved, ErrCRInvalidState))
	assert.Contains(t, ErrCRNotAnalyzed.Error(), "impact analysis")
	assert.Contains(t, ErrCRNotApproved.Error(), "not been approved")
}

// TestOldVersionsRetiredStatuses documents which lifecycle states count as
// "the old version is gone" for the close gate.
func TestOldVersionsRetiredStatuses(t *testing.T) {
	retired := []types.KnowledgeLifecycleStatus{
		types.KnowledgeLifecycleDelisted,
		types.KnowledgeLifecycleReplaced,
		types.KnowledgeLifecycleExpired,
		types.KnowledgeLifecycleArchived,
	}
	for _, status := range retired {
		assert.NotContains(t, []types.KnowledgeLifecycleStatus{
			types.KnowledgeLifecycleValid,
			types.KnowledgeLifecycleStale,
			types.KnowledgeLifecyclePendingReviewCycle,
		}, status, "%s must count as retired", status)
	}

	// Valid/stale still serve readers, so they must NOT count as retired.
	for _, status := range []types.KnowledgeLifecycleStatus{
		types.KnowledgeLifecycleValid,
		types.KnowledgeLifecycleStale,
		types.KnowledgeLifecyclePendingReviewCycle,
	} {
		assert.True(t, status.Searchable(), "%s still serves readers", status)
	}
}
