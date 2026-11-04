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

// TestKnowledgeLevelFromCustomMetadata covers the readable-metadata parser.
//
// 命中行携带的是 "key: value" 逐行的可读形式（同一份文本也进提示词），
// 因此解析器按行工作，而不是当 JSON。
func TestKnowledgeLevelFromCustomMetadata(t *testing.T) {
	assert.Equal(t, "L3", knowledgeLevelFromCustomMetadata(
		"knowaxis_level: L3\nsource: work_order"))
	assert.Equal(t, "L2", knowledgeLevelFromCustomMetadata(
		"knowaxis_level: L2"))
	// Whitespace around the key/value is tolerated.
	assert.Equal(t, "L1", knowledgeLevelFromCustomMetadata(
		"  knowaxis_level :  L1  "))

	// Unknown, missing, or malformed values mean "unlabelled".
	assert.Equal(t, "", knowledgeLevelFromCustomMetadata(""))
	assert.Equal(t, "", knowledgeLevelFromCustomMetadata("source: work_order"))
	assert.Equal(t, "", knowledgeLevelFromCustomMetadata("knowaxis_level: L9"))
	assert.Equal(t, "", knowledgeLevelFromCustomMetadata("not a key value line"))
}

// TestAssessHighRiskCoverageUsesHitMetadata wires the parser into the rule.
func TestAssessHighRiskCoverageUsesHitMetadata(t *testing.T) {
	manage := &types.ChatManage{PipelineRequest: types.PipelineRequest{Query: "这笔付款是否合规"}}
	results := []*types.SearchResult{
		{
			KnowledgeID:              "official",
			KnowledgeLifecycleStatus: string(types.KnowledgeLifecycleValid),
			KnowledgeCustomMetadata:  "knowaxis_level: L3",
		},
		{
			KnowledgeID:              "reference",
			KnowledgeLifecycleStatus: string(types.KnowledgeLifecycleValid),
			KnowledgeCustomMetadata:  "source: manual",
		},
	}
	coverage := assessHighRiskCoverage(manage, results)
	assert.Equal(t, types.AnswerRiskHigh, coverage.RiskLevel)
	assert.Equal(t, 2, coverage.CandidateCount)
	assert.Equal(t, 1, coverage.OfficialCount)
	assert.True(t, coverage.RequiresCaveat,
		"one reference-only source breaks a high-risk 100% requirement")
}

// TestAssessHighRiskCoverageFullyOfficialNeedsNoCaveat is the positive control.
func TestAssessHighRiskCoverageFullyOfficialNeedsNoCaveat(t *testing.T) {
	manage := &types.ChatManage{PipelineRequest: types.PipelineRequest{Query: "这笔付款是否合规"}}
	results := []*types.SearchResult{
		{
			KnowledgeID:              "l3",
			KnowledgeLifecycleStatus: string(types.KnowledgeLifecycleValid),
			KnowledgeCustomMetadata:  "knowaxis_level: L3",
		},
		{
			KnowledgeID:              "l2",
			KnowledgeLifecycleStatus: string(types.KnowledgeLifecycleValid),
			KnowledgeCustomMetadata:  "knowaxis_level: L2",
		},
	}
	coverage := assessHighRiskCoverage(manage, results)
	assert.True(t, coverage.MeetsRequirement)
	assert.False(t, coverage.RequiresCaveat)
}

// TestAssessHighRiskCoverageIgnoresLowRiskQuestions keeps ordinary browsing
// free of caveats even when every hit is a plain uploaded document.
func TestAssessHighRiskCoverageIgnoresLowRiskQuestions(t *testing.T) {
	manage := &types.ChatManage{PipelineRequest: types.PipelineRequest{Query: "E45 报警灯是什么意思"}}
	results := []*types.SearchResult{{
		KnowledgeID:              "plain",
		KnowledgeLifecycleStatus: string(types.KnowledgeLifecycleValid),
	}}
	coverage := assessHighRiskCoverage(manage, results)
	assert.Equal(t, types.AnswerRiskLow, coverage.RiskLevel)
	assert.False(t, coverage.RequiresCaveat)
}

// TestAssessHighRiskCoverageHandlesNilManage guards the defensive path: a nil
// ChatManage must not panic inside the assessment.
func TestAssessHighRiskCoverageHandlesNilManage(t *testing.T) {
	coverage := assessHighRiskCoverage(nil, []*types.SearchResult{{
		KnowledgeID:              "x",
		KnowledgeLifecycleStatus: string(types.KnowledgeLifecycleValid),
	}})
	assert.Equal(t, types.AnswerRiskLow, coverage.RiskLevel,
		"an unknown question cannot be classified as high risk")
	assert.False(t, coverage.RequiresCaveat)
}
