package operations

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGlossaryTermGetIsInTheCatalog covers the vocabulary read.
func TestGlossaryTermGetIsInTheCatalog(t *testing.T) {
	op, ok := Builtin().Get("glossary_term.get")
	require.True(t, ok, "glossary_term.get must be reachable")
	assert.Equal(t, "GET", op.Method)
	assert.Equal(t, "/api/v1/glossary/terms/:id", op.Route)
	assert.Equal(t, RiskRead, op.Risk)
}

// TestGlossaryTermProjectsItsFields proves an agent can use the organisation's
// definition rather than a generic one.
func TestGlossaryTermProjectsItsFields(t *testing.T) {
	op, ok := Builtin().Get("glossary_term.get")
	require.True(t, ok)

	raw, err := json.Marshal(map[string]any{
		"success": true,
		"data": map[string]any{
			"id":               "term-1",
			"standard_name":    "报警",
			"definition":       "设备出现异常参数时触发的强制提示，区别于一般的状态提示",
			"domain":           "设备运维",
			"status":           "active",
			"superseded_by_id": "",
			// Internal: aliases are retrieval-expansion material, the counter
			// examples shape prompting, and the owner is governance metadata.
			"aliases":          []any{"告警", "alarm"},
			"counter_examples": "不包含计划性停机通知",
			"owner_id":         "u-owner",
			"tenant_id":        7,
		},
	})
	require.NoError(t, err)

	projected, err := op.Project(raw)
	require.NoError(t, err)
	payload, ok := projected.Data.(map[string]any)
	require.True(t, ok)

	assert.Equal(t, "term-1", payload["term_id"])
	assert.Equal(t, "报警", payload["standard_name"])
	assert.Contains(t, payload["definition"], "强制提示")
	assert.Equal(t, "设备运维", payload["domain"])
	assert.Equal(t, "active", payload["status"])

	assert.NotContains(t, payload, "aliases")
	assert.NotContains(t, payload, "counter_examples")
	assert.NotContains(t, payload, "owner_id")
	assert.NotContains(t, payload, "tenant_id")
}

// TestGlossaryWritesStayOut keeps vocabulary governance human.
func TestGlossaryWritesStayOut(t *testing.T) {
	catalog := Builtin()
	for _, id := range []string{
		"glossary_term.create",
		"glossary_term.update",
		"glossary_term.delete",
		"glossary_term.merge",
	} {
		_, ok := catalog.Get(id)
		assert.False(t, ok, "%s must not be reachable by the model", id)
	}
}

// TestSavedViewGetIsInTheCatalog covers the saved-filter read.
func TestSavedViewGetIsInTheCatalog(t *testing.T) {
	op, ok := Builtin().Get("saved_view.get")
	require.True(t, ok, "saved_view.get must be reachable")
	assert.Equal(t, "GET", op.Method)
	assert.Equal(t, "/api/v1/saved-views/:view_id", op.Route)
	assert.Equal(t, RiskRead, op.Risk)
}

// TestSavedViewProjectsItsFields keeps the view's identity without shipping the
// frozen filter blob.
func TestSavedViewProjectsItsFields(t *testing.T) {
	op, ok := Builtin().Get("saved_view.get")
	require.True(t, ok)

	raw, err := json.Marshal(map[string]any{
		"success": true,
		"data": map[string]any{
			"id":                "view-1",
			"name":              "我负责的待复审",
			"knowledge_base_id": "kb-1",
			"is_default":        true,
			// Internal: the frozen filter set is a JSON blob the catalog cannot
			// flatten, and the owner identity is already implied by "the
			// caller's own views".
			"filter":    map[string]any{"keyword": "E45", "tag_ids": []any{"t1"}},
			"user_id":   "u-1",
			"tenant_id": 7,
		},
	})
	require.NoError(t, err)

	projected, err := op.Project(raw)
	require.NoError(t, err)
	payload, ok := projected.Data.(map[string]any)
	require.True(t, ok)

	assert.Equal(t, "view-1", payload["view_id"])
	assert.Equal(t, "我负责的待复审", payload["name"])
	assert.Equal(t, "kb-1", payload["knowledge_base_id"])
	assert.Equal(t, true, payload["is_default"])

	assert.NotContains(t, payload, "filter")
	assert.NotContains(t, payload, "user_id")
	assert.NotContains(t, payload, "tenant_id")
}

// TestSavedViewMutationsStayOut keeps the user's own view editing in the UI.
func TestSavedViewMutationsStayOut(t *testing.T) {
	catalog := Builtin()
	for _, id := range []string{
		"saved_view.create",
		"saved_view.update",
		"saved_view.delete",
		"saved_view.set_default",
	} {
		_, ok := catalog.Get(id)
		assert.False(t, ok, "%s must not be reachable by the model", id)
	}
}
