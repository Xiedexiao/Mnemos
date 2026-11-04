package operations

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEvidencePackGetIsInTheCatalog covers the second DTO in the series.
func TestEvidencePackGetIsInTheCatalog(t *testing.T) {
	op, ok := Builtin().Get("evidence_pack.get")
	require.True(t, ok, "evidence_pack.get must be reachable")
	assert.Equal(t, "GET", op.Method)
	assert.Equal(t, "/api/v1/evidence-packs/:request_id", op.Route)
	assert.Equal(t, RiskRead, op.Risk)
	assert.NotEmpty(t, op.Description)
}

// TestEvidencePackGetRequiresTheRequestID proves the path parameter is settable.
func TestEvidencePackGetRequiresTheRequestID(t *testing.T) {
	op, ok := Builtin().Get("evidence_pack.get")
	require.True(t, ok)
	names := make([]string, 0, len(op.Params))
	for _, param := range op.Params {
		names = append(names, param.Name)
	}
	assert.Contains(t, names, "request_id")
}

// TestEvidencePackGetProjectsItsFields is the reason for the DTO: the answer's
// basis reaches the model, and the hash chain does not.
func TestEvidencePackGetProjectsItsFields(t *testing.T) {
	op, ok := Builtin().Get("evidence_pack.get")
	require.True(t, ok)

	raw, err := json.Marshal(map[string]any{
		"success": true,
		"data": map[string]any{
			"request_id":     "req-1",
			"session_id":     "sess-1",
			"question":       "E45 报警怎么处理",
			"answer":         "先断电再复位",
			"refusal":        "",
			"model_version":  "qwen-max",
			"prompt_version": "v3",
			"created_at":     "2026-11-04T10:00:00Z",
			"citation_refs": []any{
				map[string]any{"knowledge_id": "doc-1", "title": "报警处理规程"},
			},
			"candidate_count": 12,
			// Internal fields: the hash chain anchors the tamper-evident
			// ledger and the permission snapshot records the authorization
			// baseline. Neither is model-facing.
			"hash_chain_prev":     "GENESIS",
			"hash_chain_row":      "abc123",
			"permission_snapshot": map[string]any{"highest_classification": "confidential"},
			"candidates":          []any{map[string]any{"snippet": "raw snapshot"}},
		},
	})
	require.NoError(t, err)

	projected, err := op.Project(raw)
	require.NoError(t, err)
	payload, ok := projected.Data.(map[string]any)
	require.True(t, ok, "projection returns an object: %T", projected.Data)

	assert.Equal(t, "req-1", payload["request_id"])
	assert.Equal(t, "E45 报警怎么处理", payload["question"])
	assert.Equal(t, "先断电再复位", payload["answer"])
	assert.Equal(t, "qwen-max", payload["model_version"])

	assert.NotContains(t, payload, "hash_chain_row",
		"the tamper-evident anchor is internal")
	assert.NotContains(t, payload, "hash_chain_prev")
	assert.NotContains(t, payload, "permission_snapshot",
		"the authorization baseline is internal")
	assert.NotContains(t, payload, "candidates",
		"the raw candidate snapshot is internal")
}

// TestKnowledgeCandidateGetIsInTheCatalog covers the third DTO.
func TestKnowledgeCandidateGetIsInTheCatalog(t *testing.T) {
	op, ok := Builtin().Get("knowledge_candidate.get")
	require.True(t, ok, "knowledge_candidate.get must be reachable")
	assert.Equal(t, "GET", op.Method)
	assert.Equal(t, "/api/v1/knowledge-candidates/:id", op.Route)
	assert.Equal(t, RiskRead, op.Risk)
}

// TestKnowledgeCandidateGetProjectsItsFields keeps the review state readable
// while the submitter and audit notes stay internal.
func TestKnowledgeCandidateGetProjectsItsFields(t *testing.T) {
	op, ok := Builtin().Get("knowledge_candidate.get")
	require.True(t, ok)

	raw, err := json.Marshal(map[string]any{
		"success": true,
		"data": map[string]any{
			"title":        "E45 报警频发处理",
			"level":        "L1",
			"status":       "candidate",
			"confidence":   "AMBIGUOUS",
			"dedup_status": "unique",
			"source_type":  "qa_answer",
			"knowledge_id": "",
			// Internal: who submitted it, the review comment, and the frozen
			// card payload are not what an agent needs to answer "was this
			// already captured".
			"requested_by":   "u-secret",
			"review_comment": "internal note",
			"card":           map[string]any{"what": "raw card"},
			"duplicate_of":   "cand-other",
			"tenant_id":      7,
		},
	})
	require.NoError(t, err)

	projected, err := op.Project(raw)
	require.NoError(t, err)
	payload, ok := projected.Data.(map[string]any)
	require.True(t, ok)

	assert.Equal(t, "E45 报警频发处理", payload["title"])
	assert.Equal(t, "L1", payload["level"])
	assert.Equal(t, "candidate", payload["status"])
	assert.Equal(t, "AMBIGUOUS", payload["confidence"])
	assert.Equal(t, "qa_answer", payload["source_type"])

	assert.NotContains(t, payload, "requested_by")
	assert.NotContains(t, payload, "review_comment")
	assert.NotContains(t, payload, "tenant_id")
}

// TestCandidateWriteOperationsStayOut is the safety counterpart: reviewing an
// experience is a human decision.
func TestCandidateWriteOperationsStayOut(t *testing.T) {
	catalog := Builtin()
	for _, id := range []string{
		"knowledge_candidate.create",
		"knowledge_candidate.confirm",
		"knowledge_candidate.publish",
		"knowledge_candidate.reject",
		"knowledge_candidate.withdraw",
	} {
		_, ok := catalog.Get(id)
		assert.False(t, ok, "%s must not be reachable by the model", id)
	}
}
