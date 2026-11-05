package operations

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestChangeRequestGetIsInTheCatalog is the template for the remaining
// endpoints: a typed response with x-agent field tags is what lets an
// operation reach the catalog at all. A map[string]interface{} response
// projects to nothing, so the operation would be listed and its output empty.
func TestChangeRequestGetIsInTheCatalog(t *testing.T) {
	catalog := Builtin()
	op, ok := catalog.Get("change_request.get")
	require.True(t, ok, "change_request.get must be reachable")
	assert.Equal(t, "GET", op.Method)
	assert.Equal(t, "/api/v1/change-requests/:id", op.Route)
	assert.Equal(t, RiskRead, op.Risk)
	assert.NotEmpty(t, op.Description, "the catalog carries guidance for the model")
}

// TestChangeRequestGetRequiresTheIDAsAnAgentParameter proves the path
// parameter is settable by the model; without it the operation is unusable.
func TestChangeRequestGetRequiresTheIDAsAnAgentParameter(t *testing.T) {
	op, ok := Builtin().Get("change_request.get")
	require.True(t, ok)

	names := make([]string, 0, len(op.Params))
	for _, param := range op.Params {
		names = append(names, param.Name)
	}
	assert.Contains(t, names, "change_request_id",
		"the x-agent name on the path parameter is what the model sets")
}

// TestChangeRequestGetProjectsItsFields is the substance of the DTO work: the
// model must receive the identifying fields, not an empty object.
func TestChangeRequestGetProjectsItsFields(t *testing.T) {
	op, ok := Builtin().Get("change_request.get")
	require.True(t, ok)

	raw, err := json.Marshal(map[string]any{
		"success": true,
		"data": map[string]any{
			"id":            "cr-1",
			"cr_number":     "CR-20261104-001",
			"title":         "E45 报警阈值调整",
			"reason":        "regulation",
			"change_type":   "param_change",
			"status":        "draft",
			"risk_level":    "mid",
			"rollback_plan": "恢复旧阈值",
			"created_at":    "2026-11-04T10:00:00Z",
			"updated_at":    "2026-11-04T11:00:00Z",
			"items": []any{map[string]any{
				"knowledge_id": "doc-1",
				"item_status":  "pending",
			}},
			// Untagged fields must not leak into the model-facing payload.
			"tenant_id":           7,
			"approval_receipt_id": "receipt-secret",
			"baseline":            map[string]any{"big": "snapshot"},
		},
	})
	require.NoError(t, err)

	projected, err := op.Project(raw)
	require.NoError(t, err)

	payload, ok := projected.Data.(map[string]any)
	require.True(t, ok, "projection returns an object: %T", projected.Data)

	assert.Equal(t, "cr-1", payload["id"])
	assert.Equal(t, "CR-20261104-001", payload["cr_number"])
	assert.Equal(t, "E45 报警阈值调整", payload["title"])
	assert.Equal(t, "param_change", payload["change_type"])
	assert.Equal(t, "draft", payload["status"])
	assert.Equal(t, "mid", payload["risk_level"])

	assert.NotContains(t, payload, "tenant_id",
		"an untagged field is not model-facing")
	assert.NotContains(t, payload, "approval_receipt_id",
		"an approval receipt is internal governance data")
	assert.NotContains(t, payload, "baseline",
		"the baseline snapshot is not annotated and stays internal")

	items, ok := payload["items"].([]any)
	require.True(t, ok, "items project as a list: %T", payload["items"])
	require.Len(t, items, 1)
	item, ok := items[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "doc-1", item["knowledge_id"])
	assert.Equal(t, "pending", item["item_status"])
}

// TestChangeRequestWriteOperationsStayOut is the safety counterpart: the
// agent may read a change request, never schedule, publish or roll one back.
func TestChangeRequestWriteOperationsStayOut(t *testing.T) {
	catalog := Builtin()
	for _, id := range []string{
		"change_request.create",
		"change_request.schedule",
		"change_request.publish",
		"change_request.rollback",
		"change_request.add_items",
	} {
		_, ok := catalog.Get(id)
		assert.False(t, ok, "%s must not be reachable by the model", id)
	}
}

// TestChangeRequestListIsInTheCatalog covers the list read.
func TestChangeRequestListIsInTheCatalog(t *testing.T) {
	op, ok := Builtin().Get("change_request.list")
	require.True(t, ok, "change_request.list must be reachable")
	assert.Equal(t, "GET", op.Method)
	assert.Equal(t, "/api/v1/change-requests", op.Route)
	assert.Equal(t, RiskRead, op.Risk)
	assert.True(t, op.Result.List)
}

// TestChangeRequestListProjectsItsFields is the "what is in flight" answer.
func TestChangeRequestListProjectsItsFields(t *testing.T) {
	op, ok := Builtin().Get("change_request.list")
	require.True(t, ok)

	raw, err := json.Marshal(map[string]any{
		"success": true,
		"total":   1,
		"data": []any{map[string]any{
			"id":          "cr-1",
			"cr_number":   "CR-20261105-001",
			"title":       "E45 报警阈值调整",
			"status":      "draft",
			"risk_level":  "mid",
			"change_type": "param_change",
			// Internal.
			"tenant_id":           7,
			"approval_receipt_id": "r-1",
		}},
	})
	require.NoError(t, err)

	projected, err := op.Project(raw)
	require.NoError(t, err)
	rows, ok := projected.Data.([]any)
	require.True(t, ok)
	require.Len(t, rows, 1)
	row := rows[0].(map[string]any)

	assert.Equal(t, "cr-1", row["id"])
	assert.Equal(t, "CR-20261105-001", row["cr_number"])
	assert.Equal(t, "draft", row["status"])
	assert.Equal(t, "mid", row["risk_level"])
	assert.NotContains(t, row, "tenant_id")
	assert.NotContains(t, row, "approval_receipt_id")
	// The total in the envelope is projected as meta when declared; either way
	// the row itself must not carry it.
	assert.NotContains(t, row, "total")
}
