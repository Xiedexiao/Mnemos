package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// stubGraphFactService records the projection calls without a database. Entity
// IDs are assigned the way the real repository does it: matching rows keep the
// stored ID, new rows get a fresh one.
type stubGraphFactService struct {
	interfaces.GraphFactService
	entities []*types.GraphEntity
	commits  []*types.GraphCommitInput
	existing map[string]string
}

func newStubGraphFactService() *stubGraphFactService {
	return &stubGraphFactService{existing: map[string]string{}}
}

func (s *stubGraphFactService) UpsertEntities(
	_ context.Context, _ uint64, entities []*types.GraphEntity, _ string,
) (*types.GraphEntityUpsertResult, error) {
	for _, entity := range entities {
		key := entity.EntityType + "/" + entity.NormalizedKey
		if id, ok := s.existing[key]; ok {
			entity.ID = id
			continue
		}
		entity.ID = "entity-" + entity.NormalizedKey
		s.existing[key] = entity.ID
		s.entities = append(s.entities, entity)
	}
	return &types.GraphEntityUpsertResult{Added: len(entities)}, nil
}

func (s *stubGraphFactService) CreateCommit(
	_ context.Context, _ uint64, input *types.GraphCommitInput, _ string,
) (*types.GraphCommit, error) {
	s.commits = append(s.commits, input)
	return &types.GraphCommit{ID: "commit-1", FactsAdded: len(input.Facts)}, nil
}

func extractionFixture() *types.GraphData {
	return &types.GraphData{
		Node: []*types.GraphNode{
			{Name: "安全门开关 K023"},
			{Name: "安全门开关 k023"}, // duplicate after normalization
			{Name: "E45 报警"},
		},
		Relation: []*types.GraphRelation{
			{Node1: "安全门开关 K023", Node2: "E45 报警", Type: "导致"},
			// Endpoint never extracted: must be skipped, not invented.
			{Node1: "安全门开关 K023", Node2: "不存在的实体", Type: "关联"},
		},
	}
}

// TestGraphProjectionBindsEvidence covers the §9.4 hard rule: every projected
// fact carries the source chunk, so it can be verified and withdrawn later.
func TestGraphProjectionBindsEvidence(t *testing.T) {
	stub := newStubGraphFactService()
	projection := &graphFactProjection{facts: stub}
	chunk := &types.Chunk{ID: "chunk-1", KnowledgeID: "doc-1", Content: "安全门开关接触不良导致 E45 报警"}

	commit, err := projection.projectChunk(context.Background(), 7, chunk, extractionFixture(), "user-1")
	require.NoError(t, err)
	require.NotNil(t, commit)

	require.Len(t, stub.commits, 1)
	require.Len(t, stub.commits[0].Facts, 1, "only the fully resolved relation is kept")
	fact := stub.commits[0].Facts[0]
	assert.Equal(t, "导致", fact.RelationName)

	var evidence []types.GraphFactEvidence
	require.NoError(t, json.Unmarshal(fact.Evidence, &evidence))
	require.Len(t, evidence, 1)
	assert.Equal(t, "doc-1", evidence[0].KnowledgeID)
	assert.Equal(t, "chunk-1", evidence[0].ChunkID)
	assert.NotEmpty(t, evidence[0].Quote)
}

// TestGraphProjectionDeduplicatesEntities keeps repeated names (different case)
// from producing duplicate rows; the graph store normalizes on the key.
func TestGraphProjectionDeduplicatesEntities(t *testing.T) {
	stub := newStubGraphFactService()
	projection := &graphFactProjection{facts: stub}
	chunk := &types.Chunk{ID: "chunk-1", KnowledgeID: "doc-1", Content: "text"}

	_, err := projection.projectChunk(context.Background(), 7, chunk, extractionFixture(), "user-1")
	require.NoError(t, err)
	assert.Len(t, stub.entities, 2, "K023 and E45; case variant is the same entity")
}

// TestGraphProjectionSkipsDanglingRelations is the anti-invention rule: an
// endpoint the extractor never produced is dropped rather than created.
func TestGraphProjectionSkipsDanglingRelations(t *testing.T) {
	stub := newStubGraphFactService()
	projection := &graphFactProjection{facts: stub}
	chunk := &types.Chunk{ID: "chunk-1", KnowledgeID: "doc-1", Content: "text"}
	graph := &types.GraphData{
		Node:     []*types.GraphNode{{Name: "A"}},
		Relation: []*types.GraphRelation{{Node1: "A", Node2: "B", Type: "关联"}},
	}
	commit, err := projection.projectChunk(context.Background(), 7, chunk, graph, "user-1")
	require.NoError(t, err)
	assert.Nil(t, commit, "no resolvable relation means no commit to make")
	assert.Empty(t, stub.commits)
}

// TestGraphProjectionEmptyInputIsNoop keeps an empty extraction from writing an
// empty commit row.
func TestGraphProjectionEmptyInputIsNoop(t *testing.T) {
	stub := newStubGraphFactService()
	projection := &graphFactProjection{facts: stub}
	commit, err := projection.projectChunk(context.Background(), 7,
		&types.Chunk{ID: "c"}, &types.GraphData{}, "user-1")
	require.NoError(t, err)
	assert.Nil(t, commit)
	assert.Empty(t, stub.commits)
}

// TestFactHasEvidence pins the evidence predicate used to demote unsupported
// facts to AMBIGUOUS.
func TestFactHasEvidence(t *testing.T) {
	assert.False(t, factHasEvidence(nil))
	assert.False(t, factHasEvidence(&types.GraphFact{}))

	withKnowledge, err := json.Marshal([]types.GraphFactEvidence{{KnowledgeID: "doc-1"}})
	require.NoError(t, err)
	assert.True(t, factHasEvidence(&types.GraphFact{Evidence: types.JSON(withKnowledge)}))

	withChunk, err := json.Marshal([]types.GraphFactEvidence{{ChunkID: "chunk-1"}})
	require.NoError(t, err)
	assert.True(t, factHasEvidence(&types.GraphFact{Evidence: types.JSON(withChunk)}))

	// A quote without a source document cannot be traced back, so it is not
	// evidence (§9.4: 没有原文位置或业务记录不得进入正式图谱).
	quoteOnly, err := json.Marshal([]types.GraphFactEvidence{{Quote: "some text"}})
	require.NoError(t, err)
	assert.False(t, factHasEvidence(&types.GraphFact{Evidence: types.JSON(quoteOnly)}))
}

// TestNormalizeFactsDemotesUnsupportedEvidence is the enforcement point: a fact
// claiming EXTRACTED confidence without evidence is downgraded to AMBIGUOUS, so
// it stops backing retrieval.
func TestNormalizeFactsDemotesUnsupportedEvidence(t *testing.T) {
	svc := &graphFactService{}
	facts := []*types.GraphFact{
		{RelationName: "导致", SourceEntityID: "a", TargetEntityID: "b",
			Confidence: types.GraphConfidenceExtracted},
		{RelationName: "关联", SourceEntityID: "a", TargetEntityID: "b",
			Confidence: types.GraphConfidenceInferred},
	}
	require.NoError(t, svc.normalizeFacts(7, facts, ""))
	assert.Equal(t, types.GraphConfidenceAmbiguous, facts[0].Confidence,
		"EXTRACTED without evidence must not stay authoritative")
	assert.Equal(t, types.GraphConfidenceInferred, facts[1].Confidence,
		"an explicitly inferred fact keeps its own label")
}

// TestExcerptForEvidenceBoundsQuote keeps the stored quote small.
func TestExcerptForEvidenceBoundsQuote(t *testing.T) {
	short := "接触不良"
	assert.Equal(t, short, excerptForEvidence(short))

	long := make([]rune, 500)
	for i := range long {
		long[i] = '文'
	}
	excerpt := excerptForEvidence(string(long))
	assert.Equal(t, 200, len([]rune(excerpt)))
}
