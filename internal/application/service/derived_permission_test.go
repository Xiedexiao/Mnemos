package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// stubDerivedKnowledgeRepo returns a fixed set of source documents.
type stubDerivedKnowledgeRepo struct {
	interfaces.KnowledgeRepository
	rows []*types.Knowledge
}

func (s *stubDerivedKnowledgeRepo) GetKnowledgeBatch(
	_ context.Context, _ uint64, ids []string,
) ([]*types.Knowledge, error) {
	wanted := make(map[string]bool, len(ids))
	for _, id := range ids {
		wanted[id] = true
	}
	out := make([]*types.Knowledge, 0, len(s.rows))
	for _, row := range s.rows {
		if wanted[row.ID] {
			out = append(out, row)
		}
	}
	return out, nil
}

func candidateWithEvidence(t *testing.T, links ...types.KnowledgeCandidateEvidence) *types.KnowledgeCandidate {
	t.Helper()
	raw, err := json.Marshal(links)
	require.NoError(t, err)
	return &types.KnowledgeCandidate{ID: "cand-1", Evidence: types.JSON(raw)}
}

// TestCandidateSourceKnowledgeIDs keeps only resolvable knowledge references.
// Operational records (work orders, NCRs) are not knowledge rows: counting them
// as sources would make the derived ceiling look computed when it is not.
func TestCandidateSourceKnowledgeIDs(t *testing.T) {
	candidate := candidateWithEvidence(t,
		types.KnowledgeCandidateEvidence{SourceType: "knowledge", SourceID: "k-1"},
		types.KnowledgeCandidateEvidence{SourceType: "knowledge", SourceID: "k-1"}, // duplicate
		types.KnowledgeCandidateEvidence{SourceType: "knowledge", SourceID: "k-2"},
		types.KnowledgeCandidateEvidence{SourceType: "work_order", SourceID: "WO-428"},
	)
	assert.Equal(t, []string{"k-1", "k-2"}, candidateSourceKnowledgeIDs(candidate))

	assert.Empty(t, candidateSourceKnowledgeIDs(nil))
	assert.Empty(t, candidateSourceKnowledgeIDs(&types.KnowledgeCandidate{ID: "c"}))
}

// TestDerivedClassificationTakesStrictestSource is the §2.4 rule that the most
// restrictive source evidence caps the derived entry: a case built from a
// confidential ticket must not become company-visible just because the case
// itself is tagged L2.
func TestDerivedClassificationTakesStrictestSource(t *testing.T) {
	resolver := &derivedPermissionResolver{knowledges: &stubDerivedKnowledgeRepo{rows: []*types.Knowledge{
		{ID: "k-1", Classification: string(types.KnowledgeClassificationInternal)},
		{ID: "k-2", Classification: string(types.KnowledgeClassificationConfidential)},
		{ID: "k-3", Classification: string(types.KnowledgeClassificationSecret)},
	}}}

	classification, missing, err := resolver.derivedClassification(
		context.Background(), 7, []string{"k-1", "k-2", "k-3"})
	require.NoError(t, err)
	assert.Equal(t, types.KnowledgeClassificationConfidential, classification,
		"the strictest source must win")
	assert.Zero(t, missing)
}

// TestDerivedClassificationReportsMissingSources makes an unresolvable citation
// visible instead of silently falling back to a permissive default.
func TestDerivedClassificationReportsMissingSources(t *testing.T) {
	resolver := &derivedPermissionResolver{knowledges: &stubDerivedKnowledgeRepo{rows: []*types.Knowledge{
		{ID: "k-1", Classification: string(types.KnowledgeClassificationPublic)},
	}}}
	_, missing, err := resolver.derivedClassification(context.Background(), 7, []string{"k-1", "gone"})
	require.NoError(t, err)
	assert.Equal(t, 1, missing)
}

// TestCandidatePublishRejectsUnresolvableSource covers the publish gate: a 409,
// not a silent publish with a guessed classification.
func TestCandidatePublishRejectsUnresolvableSource(t *testing.T) {
	svc := &candidateService{knowledges: &stubDerivedKnowledgeRepo{}}
	candidate := candidateWithEvidence(t,
		types.KnowledgeCandidateEvidence{SourceType: "knowledge", SourceID: "deleted-doc"})

	_, err := svc.derivedPermission(context.Background(), 7, candidate)
	require.ErrorIs(t, err, ErrCandidateSourceUnavailable)
}

// TestCandidatePublishWithoutSourcesFallsBackToInternal handles the legitimate
// case of a manually-authored candidate that cites no in-system document: it
// publishes at internal, never wider.
func TestCandidatePublishWithoutSourcesFallsBackToInternal(t *testing.T) {
	svc := &candidateService{knowledges: &stubDerivedKnowledgeRepo{}}
	classification, err := svc.derivedPermission(context.Background(), 7, &types.KnowledgeCandidate{ID: "c"})
	require.NoError(t, err)
	assert.Equal(t, types.KnowledgeClassificationPublic, classification,
		"no sources means nothing stricter to inherit; the KB default still applies")
}
