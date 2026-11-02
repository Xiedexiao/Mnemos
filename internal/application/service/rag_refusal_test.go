package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
)

func resultWithLifecycle(status types.KnowledgeLifecycleStatus) *types.SearchResult {
	return &types.SearchResult{ID: "chunk", KnowledgeLifecycleStatus: string(status)}
}

// TestAssessRefusal covers the §13.5 matrix that the pipeline previously
// collapsed into a single "no evidence" fallback: an answer built only from
// expired sources used to read exactly like a normal answer.
func TestAssessRefusal(t *testing.T) {
	cases := []struct {
		name       string
		results    []*types.SearchResult
		wantMarker types.EvidencePackRefusal
		wantAnswer bool
		wantSilent bool
	}{
		{
			name:       "no candidates at all",
			results:    nil,
			wantMarker: types.EvidenceRefusalNoEvidence,
			wantAnswer: false,
		},
		{
			name:       "one authoritative source answers normally",
			results:    []*types.SearchResult{resultWithLifecycle(types.KnowledgeLifecycleValid)},
			wantMarker: types.EvidenceRefusalNone,
			wantAnswer: false,
			wantSilent: true,
		},
		{
			name: "one live source out of several still answers normally",
			results: []*types.SearchResult{
				resultWithLifecycle(types.KnowledgeLifecycleValid),
				resultWithLifecycle(types.KnowledgeLifecycleExpired),
			},
			wantMarker: types.EvidenceRefusalNone,
			wantSilent: true,
		},
		{
			name:       "stale only answers with a caveat",
			results:    []*types.SearchResult{resultWithLifecycle(types.KnowledgeLifecycleStale)},
			wantMarker: types.EvidenceRefusalStaleOnly,
			wantAnswer: true,
		},
		{
			name:       "pending review cycle is also a caveat",
			results:    []*types.SearchResult{resultWithLifecycle(types.KnowledgeLifecyclePendingReviewCycle)},
			wantMarker: types.EvidenceRefusalStaleOnly,
			wantAnswer: true,
		},
		{
			name:       "expired only refuses",
			results:    []*types.SearchResult{resultWithLifecycle(types.KnowledgeLifecycleExpired)},
			wantMarker: types.EvidenceRefusalExpiredOnly,
			wantAnswer: false,
		},
		{
			name: "conflict outranks freshness",
			results: []*types.SearchResult{
				resultWithLifecycle(types.KnowledgeLifecycleValid),
				resultWithLifecycle(types.KnowledgeLifecycleConflict),
			},
			wantMarker: types.EvidenceRefusalConflict,
			wantAnswer: false,
		},
		{
			name:       "replaced is not authoritative",
			results:    []*types.SearchResult{resultWithLifecycle(types.KnowledgeLifecycleReplaced)},
			wantMarker: types.EvidenceRefusalExpiredOnly,
			wantAnswer: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decision := assessRefusal(tc.results)
			assert.Equal(t, tc.wantMarker, decision.Marker)
			assert.Equal(t, tc.wantAnswer, decision.AllowsAnswer)
			assert.Equal(t, tc.wantSilent, decision.Silent())
			if decision.Marker != types.EvidenceRefusalNone {
				assert.NotEmpty(t, decision.Notice, "every refusal case needs user-facing wording")
			}
		})
	}
}

// TestRefusalScenariosCoverTheSpecMatrix keeps the matrix complete: the spec
// lists seven distinct situations, and each must have wording so the answer
// path never has to invent text.
func TestRefusalScenariosCoverTheSpecMatrix(t *testing.T) {
	for _, marker := range []types.EvidencePackRefusal{
		types.EvidenceRefusalNoEvidence,
		types.EvidenceRefusalOutOfScope,
		types.EvidenceRefusalRefused,
		types.EvidenceRefusalConflict,
		types.EvidenceRefusalExpiredOnly,
		types.EvidenceRefusalStaleOnly,
		types.EvidenceRefusalSpeculative,
		types.EvidenceRefusalNoOfficialSource,
	} {
		scenario, ok := types.RefusalScenarios[marker]
		require.True(t, ok, "missing §13.5 scenario: %s", marker)
		assert.Equal(t, marker, scenario.Marker)
		assert.NotEmpty(t, scenario.Message)
		assert.True(t, marker.Valid(), "marker must pass the request whitelist: %s", marker)
	}
	// Existence must never leak on the out-of-scope case.
	assert.True(t, types.RefusalScenarios[types.EvidenceRefusalOutOfScope].HidesExistence)
}

func TestRefusalNoticeFor(t *testing.T) {
	assert.Empty(t, refusalNoticeFor(types.EvidenceRefusalNone))
	assert.NotEmpty(t, refusalNoticeFor(types.EvidenceRefusalExpiredOnly))
	assert.Equal(t,
		types.RefusalScenarios[types.EvidenceRefusalConflict].Message,
		refusalNoticeFor(types.EvidenceRefusalConflict))
}
