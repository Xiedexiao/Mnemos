package operations

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestKnowledgeFreshnessIsInTheCatalog covers the per-document freshness read.
func TestKnowledgeFreshnessIsInTheCatalog(t *testing.T) {
	op, ok := Builtin().Get("knowledge.freshness")
	require.True(t, ok, "knowledge.freshness must be reachable")
	assert.Equal(t, "GET", op.Method)
	assert.Equal(t, "/api/v1/knowledge/:id/freshness", op.Route)
	assert.Equal(t, RiskRead, op.Risk)
}

// TestKnowledgeFreshnessProjectsItsFields proves an agent can tell whether a
// document it is about to cite is still current.
func TestKnowledgeFreshnessProjectsItsFields(t *testing.T) {
	op, ok := Builtin().Get("knowledge.freshness")
	require.True(t, ok)

	raw, err := json.Marshal(map[string]any{
		"success":      true,
		"knowledge_id": "doc-1",
		"data": map[string]any{
			"total":       42,
			"grade":       "aging",
			"top_signals": []any{"review_cycle_overdue", "single_version_not_refreshed"},
		},
	})
	require.NoError(t, err)

	projected, err := op.Project(raw)
	require.NoError(t, err)
	payload, ok := projected.Data.(map[string]any)
	require.True(t, ok)

	assert.Equal(t, "42", payload["total"].(json.Number).String(),
		"numbers project as json.Number, preserving the wire value")
	assert.Equal(t, "aging", payload["grade"])
	signals, ok := payload["top_signals"].([]any)
	require.True(t, ok)
	assert.Len(t, signals, 2)
	assert.Equal(t, "review_cycle_overdue", signals[0])
}

// TestKnowledgeHealthIsNotInTheCatalog documents a deliberate omission.
//
// GET /knowledge-health 的角色门槛取决于 query：scope=tenant 需要 admin，其余
// 只需 viewer。目录只能声明一个角色，模型又会在不传参数时落到默认口径——
// 声明 viewer 会让模型在租户口径上稳定 403，声明 admin 又会把一条本可给
// viewer 的读面伪装成管理接口。参数相关的权限无法被目录正确表达，因此这个
// 端点不进目录，而不是给它一个必然错一半的声明。
func TestKnowledgeHealthIsNotInTheCatalog(t *testing.T) {
	catalog := Builtin()
	_, ok := catalog.Get("knowledge.health")
	assert.False(t, ok,
		"a route whose role depends on a query parameter cannot be declared correctly")

	// The forced recompute is a write behind Admin; it stays out for the
	// obvious reason too.
	_, ok = catalog.Get("knowledge.health_compute")
	assert.False(t, ok)
}

// TestFreshnessIsReadOnly proves no freshness mutation is reachable.
func TestFreshnessIsReadOnly(t *testing.T) {
	catalog := Builtin()
	for _, id := range []string{
		"knowledge.review_queue",
		"knowledge.freshness_recompute",
	} {
		_, ok := catalog.Get(id)
		assert.False(t, ok, "%s must not be reachable by the model", id)
	}
	// The review queue is the Admin-only governance list; only the
	// per-document score is a reader surface.
	queue, ok := catalog.Get("review_queue.list")
	assert.False(t, ok, "the review queue is an Admin governance surface")
	_ = queue
}
