package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// stubEvidencePacks serves one evidence pack.
type stubEvidencePacks struct {
	interfaces.EvidencePackRepository

	pack  *types.EvidencePack
	err   error
	calls int
}

func (s *stubEvidencePacks) GetByRequestID(
	_ context.Context, _ uint64, _ string,
) (*types.EvidencePack, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.pack, nil
}

// stubGraphFacts serves one impact result.
type stubGraphFacts struct {
	interfaces.GraphFactService

	result *types.GraphImpactResult
	err    error
}

func (s *stubGraphFacts) ImpactQuery(
	_ context.Context, _ uint64, _ string,
) (*types.GraphImpactResult, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.result, nil
}

// stubCandidates serves a candidate page.
type stubCandidates struct {
	interfaces.KnowledgeCandidateService

	rows  []*types.KnowledgeCandidate
	total int64
	err   error
	got   types.KnowledgeCandidateListFilter
}

func (s *stubCandidates) ListCandidates(
	_ context.Context, filter types.KnowledgeCandidateListFilter,
) ([]*types.KnowledgeCandidate, int64, error) {
	s.got = filter
	if s.err != nil {
		return nil, 0, s.err
	}
	return s.rows, s.total, nil
}

// tenantContext carries a tenant for the tools' scope check.
func tenantContext(tenantID uint64) context.Context {
	return context.WithValue(context.Background(), types.TenantIDContextKey, tenantID)
}

// decodeOutput unpacks a tool's JSON output.
func decodeOutput(t *testing.T, result *types.ToolResult) map[string]interface{} {
	t.Helper()
	require.True(t, result.Success, "tool failed: %s", result.Error)
	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(result.Output), &payload))
	return payload
}

// TestKnowledgeEvidenceReturnsCitations covers the happy path.
func TestKnowledgeEvidenceReturnsCitations(t *testing.T) {
	pack := &types.EvidencePack{
		RequestID: "req-1",
		Question:  "E45 报警怎么处理",
		Answer:    "先断电再复位",
	}
	pack.SetCitations([]types.EvidenceCitation{
		{Index: 1, KnowledgeID: "doc-1", Title: "报警处理规程"},
	})
	tool := NewKnowledgeEvidenceTool(&stubEvidencePacks{pack: pack})

	result, err := tool.Execute(tenantContext(7), json.RawMessage(`{"request_id":"req-1"}`))
	require.NoError(t, err)
	payload := decodeOutput(t, result)
	assert.Equal(t, "req-1", payload["request_id"])
	assert.Equal(t, "先断电再复位", payload["answer"], "the answer is included by default")
	assert.EqualValues(t, 1, payload["citation_count"])
	// Internal audit fields never reach an external caller.
	assert.NotContains(t, payload, "hash_chain_row")
	assert.NotContains(t, payload, "permission_snapshot")
}

// TestKnowledgeEvidenceCanOmitAnswer keeps the answer optional.
func TestKnowledgeEvidenceCanOmitAnswer(t *testing.T) {
	pack := &types.EvidencePack{RequestID: "req-1", Answer: "很长的一段答案"}
	tool := NewKnowledgeEvidenceTool(&stubEvidencePacks{pack: pack})

	result, err := tool.Execute(tenantContext(7),
		json.RawMessage(`{"request_id":"req-1","include_answer":false}`))
	require.NoError(t, err)
	payload := decodeOutput(t, result)
	assert.NotContains(t, payload, "answer")
}

// TestKnowledgeEvidenceHidesMissingTurns returns one answer for both
// "missing" and "someone else's", so the tool is not an existence probe.
func TestKnowledgeEvidenceHidesMissingTurns(t *testing.T) {
	tool := NewKnowledgeEvidenceTool(&stubEvidencePacks{err: errors.New("record not found")})
	result, err := tool.Execute(tenantContext(7), json.RawMessage(`{"request_id":"nope"}`))
	require.NoError(t, err, "a miss is not a tool failure")
	assert.False(t, result.Success)
	assert.Equal(t, "answered turn not found", result.Error)
}

// TestKnowledgeEvidenceRequiresRequestID covers the payload check.
func TestKnowledgeEvidenceRequiresRequestID(t *testing.T) {
	tool := NewKnowledgeEvidenceTool(&stubEvidencePacks{})
	_, err := tool.Execute(tenantContext(7), json.RawMessage(`{}`))
	require.Error(t, err)
}

// TestKnowledgeEvidenceRequiresTenant refuses an unscoped call.
func TestKnowledgeEvidenceRequiresTenant(t *testing.T) {
	packs := &stubEvidencePacks{pack: &types.EvidencePack{RequestID: "req-1"}}
	tool := NewKnowledgeEvidenceTool(packs)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"request_id":"req-1"}`))
	require.Error(t, err)
	assert.Equal(t, 0, packs.calls, "no lookup without a tenant")
}

// TestKnowledgeImpactRendersFacts covers the impact tool.
func TestKnowledgeImpactRendersFacts(t *testing.T) {
	tool := NewKnowledgeImpactTool(&stubGraphFacts{result: &types.GraphImpactResult{
		Depth: 2,
		Facts: []*types.GraphFact{{
			ID: "fact-1", RelationName: "depends_on",
			Confidence:     types.GraphConfidenceExtracted,
			SourceEntityID: "e-1", TargetEntityID: "e-2",
			Evidence: types.JSON(`[{"knowledge_id":"doc-1"}]`),
		}},
		AffectedEntities: []*types.GraphEntity{{ID: "e-2"}},
	}})

	result, err := tool.Execute(tenantContext(7), json.RawMessage(`{"entity_id":"e-1"}`))
	require.NoError(t, err)
	payload := decodeOutput(t, result)
	assert.Equal(t, "e-1", payload["entity_id"])
	assert.EqualValues(t, 2, payload["depth"])
	assert.EqualValues(t, 1, payload["fact_count"])
	assert.EqualValues(t, 1, payload["affected_count"])
	assert.Equal(t, false, payload["truncated"])

	facts := payload["facts"].([]interface{})
	require.Len(t, facts, 1)
	fact := facts[0].(map[string]interface{})
	assert.Equal(t, "depends_on", fact["relation_name"])
	assert.Equal(t, "EXTRACTED", fact["confidence"])
	assert.EqualValues(t, 1, fact["evidence_count"],
		"the count is reported without shipping the quotes")
}

// TestKnowledgeImpactTruncatesLargeResults keeps the model's context bounded.
func TestKnowledgeImpactTruncatesLargeResults(t *testing.T) {
	facts := make([]*types.GraphFact, 0, maxImpactFactsPerOutput+10)
	for i := 0; i < maxImpactFactsPerOutput+10; i++ {
		facts = append(facts, &types.GraphFact{ID: "fact", RelationName: "depends_on"})
	}
	tool := NewKnowledgeImpactTool(&stubGraphFacts{result: &types.GraphImpactResult{Facts: facts}})

	result, err := tool.Execute(tenantContext(7), json.RawMessage(`{"entity_id":"e-1"}`))
	require.NoError(t, err)
	payload := decodeOutput(t, result)
	assert.Equal(t, true, payload["truncated"])
	assert.EqualValues(t, maxImpactFactsPerOutput, len(payload["facts"].([]interface{})))
	assert.EqualValues(t, 10, payload["omitted_fact_count"],
		"the caller is told how much was left out")
}

// TestKnowledgeImpactHidesMissingEntities mirrors the evidence rule.
func TestKnowledgeImpactHidesMissingEntities(t *testing.T) {
	tool := NewKnowledgeImpactTool(&stubGraphFacts{err: errors.New("not found")})
	result, err := tool.Execute(tenantContext(7), json.RawMessage(`{"entity_id":"e-x"}`))
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Equal(t, "entity not found or not visible", result.Error)
}

// TestKnowledgeCandidatesRendersBacklog covers the candidate tool.
func TestKnowledgeCandidatesRendersBacklog(t *testing.T) {
	stub := &stubCandidates{
		total: 12,
		rows: []*types.KnowledgeCandidate{{
			ID: "cand-1", Title: "E45 报警频发处理", Level: "L1",
			Status: "candidate", SourceType: "qa_answer",
			DedupStatus: "unique", Confidence: "AMBIGUOUS",
		}},
	}
	tool := NewKnowledgeCandidatesTool(stub)

	result, err := tool.Execute(tenantContext(7), json.RawMessage(`{"status":"candidate","limit":5}`))
	require.NoError(t, err)
	payload := decodeOutput(t, result)
	assert.EqualValues(t, 12, payload["total"], "the total is the unpaged count")
	assert.EqualValues(t, 1, payload["returned"])
	assert.Equal(t, "candidate", stub.got.Status)
	assert.Equal(t, 5, stub.got.Limit)

	rows := payload["candidates"].([]interface{})
	require.Len(t, rows, 1)
	row := rows[0].(map[string]interface{})
	assert.Equal(t, "qa_answer", row["source_type"])
	assert.Equal(t, "AMBIGUOUS", row["confidence"])
}

// TestKnowledgeCandidatesClampsLimit keeps the page bounded.
func TestKnowledgeCandidatesClampsLimit(t *testing.T) {
	stub := &stubCandidates{}
	tool := NewKnowledgeCandidatesTool(stub)

	_, err := tool.Execute(tenantContext(7), json.RawMessage(`{"limit":9999}`))
	require.NoError(t, err)
	assert.Equal(t, knowledgeCandidatesMaxLimit, stub.got.Limit)

	stub.got = types.KnowledgeCandidateListFilter{}
	_, err = tool.Execute(tenantContext(7), json.RawMessage(`{}`))
	require.NoError(t, err)
	assert.Equal(t, knowledgeCandidatesDefaultLimit, stub.got.Limit,
		"a missing limit uses the default rather than zero")
}

// TestKnowledgeCandidatesRejectsUnknownFilters keeps a typo from silently
// returning an empty list.
func TestKnowledgeCandidatesRejectsUnknownFilters(t *testing.T) {
	tool := NewKnowledgeCandidatesTool(&stubCandidates{})
	result, err := tool.Execute(tenantContext(7), json.RawMessage(`{"status":"nonsense"}`))
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "unknown candidate status")

	result, err = tool.Execute(tenantContext(7), json.RawMessage(`{"level":"L9"}`))
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "unknown knowledge level")
}

// TestKnowledgeGovernanceToolsAreInTheCatalog proves the three tools are
// selectable and carry capability requirements.
func TestKnowledgeGovernanceToolsAreInTheCatalog(t *testing.T) {
	catalog := BuildAgentToolCatalog()
	seen := map[string]AgentToolInfo{}
	for _, info := range catalog.Tools {
		seen[info.Name] = info
	}
	for _, name := range []string{
		ToolKnowledgeEvidence, ToolKnowledgeImpact, ToolKnowledgeCandidates,
	} {
		require.Contains(t, seen, name, "%s must be selectable", name)
		assert.NotEmpty(t, seen[name].Label)
		assert.NotEmpty(t, seen[name].Description)
	}
	// impact follows graph edges, so it requires the graph capability.
	impact := seen[ToolKnowledgeImpact].Requirement
	assert.Equal(t, []KBCapability{CapGraph}, impact.AllOf)
	// evidence and candidates read stored rows: no KB capability needed.
	assert.Empty(t, seen[ToolKnowledgeEvidence].Requirement.AllOf)
	assert.Empty(t, seen[ToolKnowledgeEvidence].Requirement.AnyOf)
	assert.Empty(t, seen[ToolKnowledgeCandidates].Requirement.AllOf)
}
