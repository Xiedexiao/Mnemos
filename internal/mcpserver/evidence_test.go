package mcpserver

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

// recordingEvidence captures saved packs.
type recordingEvidence struct {
	interfaces.EvidencePackService

	packs []*types.EvidencePack
	err   error
}

func (r *recordingEvidence) Save(
	_ context.Context, pack *types.EvidencePack,
) (*types.EvidencePack, error) {
	if r.err != nil {
		return nil, r.err
	}
	copyOf := *pack
	r.packs = append(r.packs, &copyOf)
	return pack, nil
}

// evidenceContext carries a tenant and a caller.
func evidenceContext(tenantID uint64, userID string) context.Context {
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, tenantID)
	return types.WithCaller(ctx, types.Caller{
		TenantID: tenantID, UserID: userID, Role: types.TenantRoleViewer,
	})
}

// askRef builds one citation-bearing reference.
func askRef(knowledgeID, chunkID, title, classification string) *types.SearchResult {
	return &types.SearchResult{
		ID:                      chunkID,
		KnowledgeID:             knowledgeID,
		KnowledgeTitle:          title,
		Content:                 "报警处理规程正文片段",
		Score:                   0.87,
		KnowledgeClassification: classification,
	}
}

// TestRecordAskEvidenceFreezesTheAnswer covers the happy path.
func TestRecordAskEvidenceFreezesTheAnswer(t *testing.T) {
	evidence := &recordingEvidence{}
	server := (&Server{}).WithEvidenceService(evidence)

	server.recordAskEvidence(evidenceContext(7, "u-1"), "req-1", "sess-1", "msg-1",
		"E45 报警怎么处理", "先断电再复位", []*types.SearchResult{
			askRef("doc-1", "chunk-1", "报警处理规程", string(types.KnowledgeClassificationInternal)),
		})

	require.Len(t, evidence.packs, 1)
	pack := evidence.packs[0]
	assert.Equal(t, uint64(7), pack.TenantID)
	assert.Equal(t, "req-1", pack.RequestID, "the pack is keyed like the chat pipeline's")
	assert.Equal(t, "sess-1", pack.SessionID)
	assert.Equal(t, "msg-1", pack.MessageID)
	assert.Equal(t, "u-1", pack.UserID)
	assert.Equal(t, "E45 报警怎么处理", pack.Question)
	assert.Equal(t, "先断电再复位", pack.Answer)

	citations := pack.CitationsList()
	require.Len(t, citations, 1)
	assert.Equal(t, "doc-1", citations[0].KnowledgeID)
	assert.Equal(t, "chunk-1", citations[0].ChunkID)
	assert.Equal(t, "报警处理规程", citations[0].Title)
	assert.Equal(t, 1, citations[0].Index, "citation indexes start at 1")
}

// TestRecordAskEvidenceAttributesTheDelegatedUser proves the pack records the
// real subject, not the synthetic endpoint identity (§16.2).
func TestRecordAskEvidenceAttributesTheDelegatedUser(t *testing.T) {
	evidence := &recordingEvidence{}
	server := (&Server{}).WithEvidenceService(evidence)

	ctx := types.WithAgentDelegation(evidenceContext(7, "mcp-ep-1"), types.AgentDelegation{
		DelegatedUserID: "u-end", Purpose: "生成设备健康报告",
	})
	server.recordAskEvidence(ctx, "req-2", "sess", "msg", "问题", "回答", nil)

	require.Len(t, evidence.packs, 1)
	assert.Equal(t, "u-end", evidence.packs[0].UserID,
		"the pack names the delegated user, matching the audit actor")
}

// TestRecordAskEvidenceMarksNoEvidence proves an answer without citations is
// recorded as such rather than looking like a normal answer.
func TestRecordAskEvidenceMarksNoEvidence(t *testing.T) {
	evidence := &recordingEvidence{}
	server := (&Server{}).WithEvidenceService(evidence)

	server.recordAskEvidence(evidenceContext(7, "u-1"), "req-3", "sess", "msg",
		"问题", "凭空回答", nil)

	require.Len(t, evidence.packs, 1)
	assert.Equal(t, string(types.EvidenceRefusalNoEvidence), evidence.packs[0].Refusal,
		"no citations means no basis, and the pack says so")
}

// TestRecordAskEvidenceKeepsTheHighestClassification is the permission baseline.
func TestRecordAskEvidenceKeepsTheHighestClassification(t *testing.T) {
	evidence := &recordingEvidence{}
	server := (&Server{}).WithEvidenceService(evidence)

	server.recordAskEvidence(evidenceContext(7, "u-1"), "req-4", "sess", "msg",
		"问题", "回答", []*types.SearchResult{
			askRef("doc-1", "c1", "公开说明", string(types.KnowledgeClassificationPublic)),
			askRef("doc-2", "c2", "机密配方", string(types.KnowledgeClassificationConfidential)),
			askRef("doc-3", "c3", "内部规程", string(types.KnowledgeClassificationInternal)),
		})

	require.Len(t, evidence.packs, 1)
	snapshot := evidence.packs[0].PermissionSnapshot
	require.NotEmpty(t, snapshot)
	var decoded types.EvidencePermissionSnapshot
	require.NoError(t, json.Unmarshal(snapshot, &decoded))
	assert.Equal(t, string(types.KnowledgeClassificationConfidential),
		decoded.HighestClassification,
		"the most sensitive evidence sets the baseline")
}

// TestRecordAskEvidenceTruncatesSnippets keeps the pack a snapshot, not a copy.
func TestRecordAskEvidenceTruncatesSnippets(t *testing.T) {
	evidence := &recordingEvidence{}
	server := (&Server{}).WithEvidenceService(evidence)

	long := ""
	for i := 0; i < evidenceSnippetMaxRunes+200; i++ {
		long += "字"
	}
	ref := askRef("doc-1", "c1", "标题", string(types.KnowledgeClassificationInternal))
	ref.Content = long
	server.recordAskEvidence(evidenceContext(7, "u-1"), "req-5", "sess", "msg",
		"问题", "回答", []*types.SearchResult{ref})

	require.Len(t, evidence.packs, 1)
	var candidates []types.EvidenceCandidate
	require.NoError(t, json.Unmarshal(evidence.packs[0].Candidates, &candidates))
	require.Len(t, candidates, 1)
	assert.LessOrEqual(t, len([]rune(candidates[0].Snippet)), evidenceSnippetMaxRunes)
}

// TestRecordAskEvidenceSurvivesSaveFailure is the availability rule: the answer
// was already produced, so a failed write must not fail the call.
func TestRecordAskEvidenceSurvivesSaveFailure(t *testing.T) {
	evidence := &recordingEvidence{err: errors.New("evidence table locked")}
	server := (&Server{}).WithEvidenceService(evidence)

	assert.NotPanics(t, func() {
		server.recordAskEvidence(evidenceContext(7, "u-1"), "req-6", "sess", "msg",
			"问题", "回答", nil)
	})
}

// TestRecordAskEvidenceWithoutServiceIsNoop covers the unconfigured case.
func TestRecordAskEvidenceWithoutServiceIsNoop(t *testing.T) {
	server := &Server{}
	assert.NotPanics(t, func() {
		server.recordAskEvidence(evidenceContext(7, "u-1"), "req-7", "sess", "msg",
			"问题", "回答", nil)
	})
}

// TestRecordAskEvidenceRequiresTenantAndRequestID skips incomplete contexts.
func TestRecordAskEvidenceRequiresTenantAndRequestID(t *testing.T) {
	evidence := &recordingEvidence{}
	server := (&Server{}).WithEvidenceService(evidence)

	server.recordAskEvidence(context.Background(), "req-8", "sess", "msg", "q", "a", nil)
	assert.Empty(t, evidence.packs, "no tenant means no scoped pack")

	server.recordAskEvidence(evidenceContext(7, "u-1"), "", "sess", "msg", "q", "a", nil)
	assert.Empty(t, evidence.packs, "the request id is the pack's identity")
}

// TestRecordAskEvidenceSkipsNilReferences keeps a nil row from panicking.
func TestRecordAskEvidenceSkipsNilReferences(t *testing.T) {
	evidence := &recordingEvidence{}
	server := (&Server{}).WithEvidenceService(evidence)

	server.recordAskEvidence(evidenceContext(7, "u-1"), "req-9", "sess", "msg",
		"问题", "回答", []*types.SearchResult{nil,
			askRef("doc-1", "c1", "标题", string(types.KnowledgeClassificationInternal))})

	require.Len(t, evidence.packs, 1)
	citations := evidence.packs[0].CitationsList()
	require.Len(t, citations, 1)
	assert.Equal(t, "doc-1", citations[0].KnowledgeID)
}
