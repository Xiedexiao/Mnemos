package operations

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOntologyProposalGetIsInTheCatalog covers the third read endpoint.
func TestOntologyProposalGetIsInTheCatalog(t *testing.T) {
	op, ok := Builtin().Get("ontology_proposal.get")
	require.True(t, ok, "ontology_proposal.get must be reachable")
	assert.Equal(t, "GET", op.Method)
	assert.Equal(t, "/api/v1/ontology/proposals/:id", op.Route)
	assert.Equal(t, RiskRead, op.Risk)
}

// TestOntologyProposalGetProjectsItsFields keeps the modelling decision
// readable while the source document list stays internal.
func TestOntologyProposalGetProjectsItsFields(t *testing.T) {
	op, ok := Builtin().Get("ontology_proposal.get")
	require.True(t, ok)

	raw, err := json.Marshal(map[string]any{
		"success": true,
		"data": map[string]any{
			"id":         "prop-1",
			"name":       "设备维修知识本体",
			"domain":     "设备运维",
			"status":     "pending",
			"rationale":  "现有 schema 无法表达备件与工单之间的替换关系",
			"created_at": "2026-11-04T10:00:00Z",
			"updated_at": "2026-11-04T11:00:00Z",
			// Internal: the exact source documents, the raw proposal payload,
			// the compatibility report and the reviewer identity are not what
			// the model needs to explain a proposed change.
			"source_knowledge_ids": []any{"doc-1", "doc-2"},
			"proposal":             map[string]any{"types": "raw"},
			"compatibility_report": map[string]any{"compatible": true},
			"reviewed_by":          "u-reviewer",
			"review_comment":       "internal note",
			"tenant_id":            7,
		},
	})
	require.NoError(t, err)

	projected, err := op.Project(raw)
	require.NoError(t, err)
	payload, ok := projected.Data.(map[string]any)
	require.True(t, ok)

	assert.Equal(t, "prop-1", payload["id"])
	assert.Equal(t, "设备维修知识本体", payload["name"])
	assert.Equal(t, "设备运维", payload["domain"])
	assert.Equal(t, "pending", payload["status"])
	assert.Contains(t, payload["rationale"], "替换关系")

	assert.NotContains(t, payload, "source_knowledge_ids")
	assert.NotContains(t, payload, "proposal")
	assert.NotContains(t, payload, "compatibility_report")
	assert.NotContains(t, payload, "reviewed_by")
	assert.NotContains(t, payload, "tenant_id")
}

// TestApprovalTaskListIsInTheCatalog covers the fourth read endpoint.
func TestApprovalTaskListIsInTheCatalog(t *testing.T) {
	op, ok := Builtin().Get("approval_task.list")
	require.True(t, ok, "approval_task.list must be reachable")
	assert.Equal(t, "GET", op.Method)
	assert.Equal(t, "/api/v1/governance-approvals", op.Route)
	assert.Equal(t, RiskRead, op.Risk)
	assert.True(t, op.Result.List, "it returns a list")
}

// TestApprovalTaskListProjectsItsFields keeps the queue readable and the
// ballot history internal.
func TestApprovalTaskListProjectsItsFields(t *testing.T) {
	op, ok := Builtin().Get("approval_task.list")
	require.True(t, ok)

	raw, err := json.Marshal(map[string]any{
		"success": true,
		"data": []any{map[string]any{
			"id":           "task-1",
			"title":        "E45 报警阈值调整",
			"subject_type": "knowledge",
			"subject_id":   "doc-1",
			"knowledge_id": "doc-1",
			"kb_id":        "kb-1",
			"risk_level":   "mid",
			"status":       "pending",
			"mode":         "dual",
			"sla_due":      "2026-11-05T10:00:00Z",
			"created_at":   "2026-11-04T10:00:00Z",
			// Internal: who asked, the frozen baseline, the content
			// fingerprint and the individual votes.
			"requested_by":    "u-secret",
			"baseline":        map[string]any{"snapshot": "raw"},
			"content_hash":    "sha256:abc",
			"decided_history": []any{map[string]any{"by": "u-2"}},
			"receipt_id":      "receipt-1",
			"comment":         "internal",
			"tenant_id":       7,
		}},
	})
	require.NoError(t, err)

	projected, err := op.Project(raw)
	require.NoError(t, err)
	rows, ok := projected.Data.([]any)
	require.True(t, ok, "projection returns a list: %T", projected.Data)
	require.Len(t, rows, 1)
	row, ok := rows[0].(map[string]any)
	require.True(t, ok)

	assert.Equal(t, "task-1", row["approval_task_id"])
	assert.Equal(t, "E45 报警阈值调整", row["title"])
	assert.Equal(t, "knowledge", row["subject_type"])
	assert.Equal(t, "mid", row["risk_level"])
	assert.Equal(t, "pending", row["status"])
	assert.Equal(t, "dual", row["mode"])

	assert.NotContains(t, row, "requested_by")
	assert.NotContains(t, row, "baseline")
	assert.NotContains(t, row, "content_hash")
	assert.NotContains(t, row, "decided_history")
	assert.NotContains(t, row, "tenant_id")
}

// TestApprovalDecisionStaysOut is the four-eyes rule: a model that could vote
// on its own action requests would make the rule meaningless.
func TestApprovalDecisionStaysOut(t *testing.T) {
	catalog := Builtin()
	for _, id := range []string{
		"approval_task.decide",
		"approval_task.submit",
		"ontology_proposal.create",
		"ontology_proposal.approve",
		"ontology_proposal.reject",
		"ontology_proposal.apply",
	} {
		_, ok := catalog.Get(id)
		assert.False(t, ok, "%s must not be reachable by the model", id)
	}
}
