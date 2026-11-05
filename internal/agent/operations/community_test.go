package operations

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCommunityQuestionGetIsInTheCatalog covers the ninth read endpoint.
func TestCommunityQuestionGetIsInTheCatalog(t *testing.T) {
	op, ok := Builtin().Get("community_question.get")
	require.True(t, ok, "community_question.get must be reachable")
	assert.Equal(t, "GET", op.Method)
	assert.Equal(t, "/api/v1/community/questions/:id", op.Route)
	assert.Equal(t, RiskRead, op.Risk)
}

// TestCommunityQuestionProjectsItsFields proves an agent can surface how a
// question was answered in practice.
func TestCommunityQuestionProjectsItsFields(t *testing.T) {
	op, ok := Builtin().Get("community_question.get")
	require.True(t, ok)

	raw, err := json.Marshal(map[string]any{
		"success": true,
		"data": map[string]any{
			"id":                 "q-1",
			"title":              "E45 报警频繁触发怎么排查",
			"body":               "最近一周 E45 每天报警三次",
			"status":             "resolved",
			"knowledge_id":       "doc-1",
			"accepted_answer_id": "a-1",
			"created_at":         "2026-11-01T10:00:00Z",
			"answers": []any{map[string]any{
				"id":       "a-1",
				"body":     "先检查限位传感器，多数是积尘",
				"votes":    7,
				"accepted": true,
			}},
			// Internal: who asked, who was pinged, the bounty and the
			// promotion target are participation metadata, not answer content.
			"asked_by":               "u-asker",
			"expert_id":              "u-expert",
			"bounty":                 50,
			"converted_knowledge_id": "doc-2",
			"tenant_id":              7,
		},
	})
	require.NoError(t, err)

	projected, err := op.Project(raw)
	require.NoError(t, err)
	payload, ok := projected.Data.(map[string]any)
	require.True(t, ok)

	assert.Equal(t, "q-1", payload["question_id"])
	assert.Equal(t, "E45 报警频繁触发怎么排查", payload["title"])
	assert.Equal(t, "resolved", payload["status"])
	assert.Equal(t, "doc-1", payload["knowledge_id"])
	assert.Equal(t, "a-1", payload["accepted_answer_id"])

	assert.NotContains(t, payload, "asked_by")
	assert.NotContains(t, payload, "expert_id")
	assert.NotContains(t, payload, "bounty")
	assert.NotContains(t, payload, "converted_knowledge_id")
	assert.NotContains(t, payload, "tenant_id")
}

// TestCommunityWritesStayOut keeps participation human: asking, answering,
// adopting and converting are all deliberate acts by a person.
func TestCommunityWritesStayOut(t *testing.T) {
	catalog := Builtin()
	for _, id := range []string{
		"community_question.ask",
		"community_question.answer",
		"community_question.accept",
		"community_question.convert",
	} {
		_, ok := catalog.Get(id)
		assert.False(t, ok, "%s must not be reachable by the model", id)
	}
}
