package operations

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWebhookDeliveryListIsInTheCatalog covers the tenth read endpoint.
func TestWebhookDeliveryListIsInTheCatalog(t *testing.T) {
	op, ok := Builtin().Get("webhook_delivery.list")
	require.True(t, ok, "webhook_delivery.list must be reachable")
	assert.Equal(t, "GET", op.Method)
	assert.Equal(t, "/api/v1/webhooks/deliveries", op.Route)
	assert.Equal(t, RiskRead, op.Risk)
	assert.True(t, op.Result.List)
}

// TestWebhookDeliveryListProjectsItsFields proves an agent can explain why an
// integration stopped receiving events.
func TestWebhookDeliveryListProjectsItsFields(t *testing.T) {
	op, ok := Builtin().Get("webhook_delivery.list")
	require.True(t, ok)

	raw, err := json.Marshal(map[string]any{
		"success": true,
		"data": []any{map[string]any{
			"id":               "d-1",
			"endpoint_id":      "ep-1",
			"event_id":         "evt-1",
			"event_type":       "knowledge.published",
			"state":            "dead",
			"attempts":         6,
			"next_attempt_at":  "2026-11-05T10:00:00Z",
			"last_status_code": 500,
			"last_error":       "upstream returned 500",
			"created_at":       "2026-11-04T10:00:00Z",
			// Internal: the payload is the full event body, and the tenant id
			// is not model-facing.
			"payload":   map[string]any{"knowledge_id": "doc-1", "big": "body"},
			"tenant_id": 7,
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

	assert.Equal(t, "d-1", row["delivery_id"])
	assert.Equal(t, "ep-1", row["endpoint_id"])
	assert.Equal(t, "knowledge.published", row["event_type"])
	assert.Equal(t, "dead", row["state"])
	assert.Equal(t, "upstream returned 500", row["last_error"])

	assert.NotContains(t, row, "payload",
		"the event body is not model context and may carry internal ids")
	assert.NotContains(t, row, "tenant_id")
}

// TestWebhookWritesStayOut keeps endpoint creation and replay human: an
// endpoint carries a signing secret, and replaying re-sends a real event.
func TestWebhookWritesStayOut(t *testing.T) {
	catalog := Builtin()
	for _, id := range []string{
		"webhook_endpoint.create",
		"webhook_endpoint.update",
		"webhook_endpoint.delete",
		"webhook_delivery.replay",
	} {
		_, ok := catalog.Get(id)
		assert.False(t, ok, "%s must not be reachable by the model", id)
	}
}
