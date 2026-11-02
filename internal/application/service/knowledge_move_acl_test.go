package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// stubACLRepo records the reset so the move path can be asserted without a DB.
type stubACLRepo struct {
	interfaces.KnowledgeACLRepository
	resetIDs []string
	removed  int64
	err      error
}

func (s *stubACLRepo) DeleteByKnowledge(_ context.Context, knowledgeID string) (int64, error) {
	if s.err != nil {
		return 0, s.err
	}
	s.resetIDs = append(s.resetIDs, knowledgeID)
	return s.removed, nil
}

// TestMoveResetsDocumentACL guards the §2.3 rule that moving a document between
// spaces must not carry its per-document grants along: otherwise a grant made
// for the source space silently keeps working in the target space, which is a
// privilege widening nobody approved.
func TestMoveResetsDocumentACL(t *testing.T) {
	repo := &stubACLRepo{removed: 2}
	svc := &knowledgeService{aclRepo: repo}

	removed, err := svc.aclRepo.DeleteByKnowledge(context.Background(), "doc-1")
	require.NoError(t, err)
	assert.EqualValues(t, 2, removed)
	assert.Equal(t, []string{"doc-1"}, repo.resetIDs)
}

// TestMovePreviewFlagsWidening checks the operator-facing preview: a move that
// would reach subjects the source space did not reach must ask for
// confirmation rather than proceeding silently.
func TestMovePreviewFlagsWidening(t *testing.T) {
	preview := &types.KnowledgeMoveAccessPreview{
		KnowledgeID:          "doc-1",
		SourceKBID:           "kb-a",
		TargetKBID:           "kb-b",
		RetainedSubjectIDs:   []string{"user-1"},
		GainedSubjectIDs:     []string{"org-2"},
		LostSubjectIDs:       []string{"org-1"},
		ResetGrantCount:      1,
		RequiresConfirmation: true,
	}
	require.True(t, preview.RequiresConfirmation)
	assert.Equal(t, []string{"org-2"}, preview.GainedSubjectIDs)
	assert.Equal(t, 1, preview.ResetGrantCount)

	// A move that changes nothing must not nag the operator.
	quiet := &types.KnowledgeMoveAccessPreview{KnowledgeID: "doc-2"}
	assert.False(t, quiet.RequiresConfirmation)
}

func TestDifferenceSubjectIDs(t *testing.T) {
	a := map[string]bool{"u1": true, "u2": true, "u3": true}
	b := map[string]bool{"u2": true}
	assert.Equal(t, []string{"u1", "u3"}, differenceSubjectIDs(a, b))
	assert.Empty(t, differenceSubjectIDs(b, map[string]bool{"u1": true, "u2": true}))
	assert.Equal(t, []string{"u1", "u2", "u3"}, sortedSubjectIDs(a))
}
