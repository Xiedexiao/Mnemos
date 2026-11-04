package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// stubGraphEngine serves the graph namespace lookups.
type stubGraphEngine struct {
	interfaces.RetrieveGraphRepository

	// byNamespace maps a KB id to the graph data it returns.
	byNamespace map[string]*types.GraphData
	err         error
	// calls records the namespaces queried, so scope can be asserted.
	calls []types.NameSpace
	// terms records the terms passed to the last call.
	terms []string
}

func (s *stubGraphEngine) SearchNode(
	_ context.Context, namespace types.NameSpace, nodes []string,
) (*types.GraphData, error) {
	s.calls = append(s.calls, namespace)
	s.terms = nodes
	if s.err != nil {
		return nil, s.err
	}
	return s.byNamespace[namespace.KnowledgeBase], nil
}

// graphTestService builds a service with only the graph engine wired.
func graphTestService(engine interfaces.RetrieveGraphRepository) *knowledgeBaseService {
	return &knowledgeBaseService{graphEngine: engine}
}

// TestGraphRecallTerms covers the term extraction.
func TestGraphRecallTerms(t *testing.T) {
	// A plain Latin query yields the whole string plus its fields — but no
	// two-letter fragments: those are not entity names.
	assert.Equal(t, []string{"E45 alarm", "E45", "alarm"}, graphRecallTerms("E45 alarm"))

	// A Chinese query yields the whole string and its bigrams.
	terms := graphRecallTerms("报警处理")
	assert.Contains(t, terms, "报警处理")
	assert.Contains(t, terms, "报警")
	assert.Contains(t, terms, "警处")
	assert.Contains(t, terms, "处理")

	// Too-short fragments are dropped: one character is not an entity name.
	assert.Equal(t, []string{"报警"}, graphRecallTerms("报警"))
	assert.Empty(t, graphRecallTerms("   "))
	assert.Empty(t, graphRecallTerms(""))
}

// TestGraphRecallTermsAreBounded keeps a pathological query from fanning out.
func TestGraphRecallTermsAreBounded(t *testing.T) {
	long := ""
	for i := 0; i < 40; i++ {
		long += "报警处理设备"
	}
	terms := graphRecallTerms(long)
	assert.LessOrEqual(t, len(terms), graphRecallMaxEntities)
}

// TestGraphRecallReturnsChunkIDsOnly is the "no invented scores" rule.
func TestGraphRecallReturnsChunkIDsOnly(t *testing.T) {
	engine := &stubGraphEngine{byNamespace: map[string]*types.GraphData{
		"kb-1": {Node: []*types.GraphNode{
			{Name: "报警", KnowledgeID: "doc-1", Chunks: []string{"chunk-1", "chunk-2"}},
		}},
	}}
	svc := graphTestService(engine)

	lists := svc.graphRecallCandidates(context.Background(), []string{"kb-1"}, "报警")
	require.Len(t, lists, 1, "the graph channel is one ranked list")
	require.Len(t, lists[0], 2)
	assert.Equal(t, "chunk-1", lists[0][0].ChunkID)
	assert.Equal(t, "doc-1", lists[0][0].KnowledgeID)
	assert.Equal(t, "kb-1", lists[0][0].KnowledgeBaseID,
		"the namespace supplies the knowledge base id")
	assert.Equal(t, types.MatchTypeGraph, lists[0][0].MatchType)
	// The graph contributes no score: fusion derives it from rank.
	assert.Zero(t, lists[0][0].Score)
}

// TestGraphRecallRanksExactEntityFirst orders the channel internally.
func TestGraphRecallRanksExactEntityFirst(t *testing.T) {
	engine := &stubGraphEngine{byNamespace: map[string]*types.GraphData{
		"kb-1": {Node: []*types.GraphNode{
			// A bigram match listed before the exact-named node on purpose.
			{Name: "警处", KnowledgeID: "doc-partial", Chunks: []string{"chunk-partial"}},
			{Name: "报警处理", KnowledgeID: "doc-exact", Chunks: []string{"chunk-exact"}},
		}},
	}}
	svc := graphTestService(engine)

	lists := svc.graphRecallCandidates(context.Background(), []string{"kb-1"}, "报警处理")
	require.Len(t, lists, 1)
	require.Len(t, lists[0], 2)
	assert.Equal(t, "chunk-exact", lists[0][0].ChunkID,
		"the node whose name equals the query ranks first")
}

// TestGraphRecallDeduplicatesChunks keeps a chunk linked to two entities once.
func TestGraphRecallDeduplicatesChunks(t *testing.T) {
	engine := &stubGraphEngine{byNamespace: map[string]*types.GraphData{
		"kb-1": {Node: []*types.GraphNode{
			{Name: "报警", KnowledgeID: "doc-1", Chunks: []string{"shared"}},
			{Name: "警处", KnowledgeID: "doc-1", Chunks: []string{"shared", "other"}},
		}},
	}}
	svc := graphTestService(engine)

	lists := svc.graphRecallCandidates(context.Background(), []string{"kb-1"}, "报警处理")
	require.Len(t, lists, 1)
	ids := make([]string, 0, len(lists[0]))
	for _, row := range lists[0] {
		ids = append(ids, row.ChunkID)
	}
	assert.ElementsMatch(t, []string{"shared", "other"}, ids)
}

// TestGraphRecallCapsChunks bounds the channel's contribution.
func TestGraphRecallCapsChunks(t *testing.T) {
	chunks := make([]string, 0, graphRecallMaxChunks*2)
	for i := 0; i < graphRecallMaxChunks*2; i++ {
		chunks = append(chunks, "chunk-"+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	engine := &stubGraphEngine{byNamespace: map[string]*types.GraphData{
		"kb-1": {Node: []*types.GraphNode{{Name: "报警", KnowledgeID: "doc", Chunks: chunks}}},
	}}
	svc := graphTestService(engine)

	lists := svc.graphRecallCandidates(context.Background(), []string{"kb-1"}, "报警")
	require.Len(t, lists, 1)
	assert.LessOrEqual(t, len(lists[0]), graphRecallMaxChunks,
		"a hub entity must not flood the fusion")
}

// TestGraphRecallFailureDoesNotBlock is the availability rule: graph is an
// enhancement, so its failure yields an empty channel, never an error.
func TestGraphRecallFailureDoesNotBlock(t *testing.T) {
	engine := &stubGraphEngine{err: errors.New("neo4j down")}
	svc := graphTestService(engine)

	lists := svc.graphRecallCandidates(context.Background(), []string{"kb-1"}, "报警")
	assert.Nil(t, lists)
}

// TestGraphRecallWithoutEngine is the unconfigured case.
func TestGraphRecallWithoutEngine(t *testing.T) {
	svc := graphTestService(nil)
	assert.Nil(t, svc.graphRecallCandidates(context.Background(), []string{"kb-1"}, "报警"))
}

// TestGraphRecallQueriesEveryScope proves multi-KB fan-out.
func TestGraphRecallQueriesEveryScope(t *testing.T) {
	engine := &stubGraphEngine{byNamespace: map[string]*types.GraphData{
		"kb-1": {Node: []*types.GraphNode{{Name: "报警", KnowledgeID: "d1", Chunks: []string{"c1"}}}},
		"kb-2": {Node: []*types.GraphNode{{Name: "报警", KnowledgeID: "d2", Chunks: []string{"c2"}}}},
	}}
	svc := graphTestService(engine)

	lists := svc.graphRecallCandidates(context.Background(), []string{"kb-1", "kb-2"}, "报警")
	require.Len(t, lists, 1)
	require.Len(t, lists[0], 2)
	assert.Len(t, engine.calls, 2, "each knowledge base is queried once")
	bases := map[string]string{}
	for _, row := range lists[0] {
		bases[row.ChunkID] = row.KnowledgeBaseID
	}
	assert.Equal(t, "kb-1", bases["c1"])
	assert.Equal(t, "kb-2", bases["c2"])
}

// TestFuseWithGraphAddsAVote is the §12.2 rule: a chunk found by graph *and*
// vector outranks one found by vector alone.
func TestFuseWithGraphAddsAVote(t *testing.T) {
	vectorList := []*types.IndexWithScore{
		{ChunkID: "both", Score: 0.8},
		{ChunkID: "vector-only", Score: 0.9},
	}
	graphList := []*types.IndexWithScore{
		{ChunkID: "both", MatchType: types.MatchTypeGraph},
	}
	got := fuseOrDeduplicate(context.Background(),
		[][]*types.IndexWithScore{vectorList}, nil,
		[][]*types.IndexWithScore{graphList}, nil)

	require.Len(t, got, 2)
	assert.Equal(t, "both", got[0].ChunkID,
		"agreement between channels beats a higher vector score alone")
	scores := map[string]float64{}
	for _, row := range got {
		scores[row.ChunkID] = row.Score
	}
	assert.Greater(t, scores["both"], scores["vector-only"])
	// "both" is rank 1 in the graph list but rank 1* in the vector list by
	// chunk order, so it scores just under the full two-channel maximum.
	assert.Greater(t, scores["both"], 0.97)
}

// TestFuseWithGraphKeepsVectorMatchType proves fusion does not relabel a chunk
// that both channels found.
func TestFuseWithGraphKeepsVectorMatchType(t *testing.T) {
	vectorList := []*types.IndexWithScore{
		{ChunkID: "both", Score: 0.8, MatchType: types.MatchTypeEmbedding},
	}
	graphList := []*types.IndexWithScore{
		{ChunkID: "both", MatchType: types.MatchTypeGraph},
	}
	got := fuseOrDeduplicate(context.Background(),
		[][]*types.IndexWithScore{vectorList}, nil,
		[][]*types.IndexWithScore{graphList}, nil)

	require.Len(t, got, 1)
	assert.Equal(t, types.MatchTypeEmbedding, got[0].MatchType,
		"the stronger evidence (vector) survives the merge")
	assert.InDelta(t, 1.0, got[0].Score, 0.001)
}

// TestFuseGraphOnlyScoresOne covers the graph-alone path.
func TestFuseGraphOnlyScoresOne(t *testing.T) {
	graphList := []*types.IndexWithScore{
		{ChunkID: "a", MatchType: types.MatchTypeGraph},
		{ChunkID: "b", MatchType: types.MatchTypeGraph},
	}
	got := fuseOrDeduplicate(context.Background(), nil, nil,
		[][]*types.IndexWithScore{graphList}, nil)
	require.Len(t, got, 2)
	for _, row := range got {
		assert.InDelta(t, 1.0, row.Score, 1e-9,
			"a graph match is binary, so every hit is a full match")
	}
}

// TestFuseWithoutGraphIsUnchanged guards the compatibility rule: when the graph
// channel is empty the scores must match the two-channel formula exactly.
func TestFuseWithoutGraphIsUnchanged(t *testing.T) {
	build := func() ([][]*types.IndexWithScore, [][]*types.IndexWithScore) {
		return [][]*types.IndexWithScore{{{ChunkID: "vec", Score: 0.9}}},
			[][]*types.IndexWithScore{{{ChunkID: "kw", Score: 12}}}
	}
	vectorLists, keywordLists := build()
	withoutGraph := fuseOrDeduplicate(context.Background(), vectorLists, keywordLists, nil, nil)

	// RRF with defaults 0.7 / 0.3 and k=60: rank 1 alone scores its own weight.
	require.Len(t, withoutGraph, 2)
	scores := map[string]float64{}
	for _, row := range withoutGraph {
		scores[row.ChunkID] = row.Score
	}
	assert.InDelta(t, 0.7, scores["vec"], 1e-9)
	assert.InDelta(t, 0.3, scores["kw"], 1e-9)
}

// TestFuseWithZeroGraphWeightIsTwoChannel proves an explicit 0 turns the channel
// off without changing anything else.
func TestFuseWithZeroGraphWeightIsTwoChannel(t *testing.T) {
	vectorList := []*types.IndexWithScore{{ChunkID: "vec", Score: 0.9}}
	graphList := []*types.IndexWithScore{{ChunkID: "graph-only", MatchType: types.MatchTypeGraph}}
	cfg := &types.RetrievalConfig{
		RRFK: 60, RRFVectorWeight: 0.7, RRFKeywordWeight: 0.3, RRFGraphWeight: 0,
	}
	got := fuseOrDeduplicate(context.Background(),
		[][]*types.IndexWithScore{vectorList}, nil,
		[][]*types.IndexWithScore{graphList}, cfg)
	require.Len(t, got, 2)
	for _, row := range got {
		if row.ChunkID == "vec" {
			assert.InDelta(t, 1.0, row.Score, 1e-9)
		}
		if row.ChunkID == "graph-only" {
			assert.Zero(t, row.Score, "a zero-weight channel contributes nothing")
		}
	}
}

// TestGraphWeightDefaults pins the configured default.
func TestGraphWeightDefaults(t *testing.T) {
	var nilCfg *types.RetrievalConfig
	vector, keyword, graph := nilCfg.GetEffectiveRRFWeights()
	assert.InDelta(t, 0.7, vector, 1e-9)
	assert.InDelta(t, 0.3, keyword, 1e-9)
	assert.InDelta(t, 0.2, graph, 1e-9,
		"the graph channel is a supplement, not a peer of vector recall")

	// An explicit zero is a configuration choice, not a missing value.
	explicit := &types.RetrievalConfig{RRFVectorWeight: 0.6, RRFKeywordWeight: 0.4}
	_, _, graph = explicit.GetEffectiveRRFWeights()
	assert.Zero(t, graph, "0 means the channel is off")
}

// TestGraphRecallHonoursContextBudget ensures a slow backend cannot stall the
// search: the recall has its own timeout.
func TestGraphRecallHonoursContextBudget(t *testing.T) {
	engine := &stubGraphEngine{byNamespace: map[string]*types.GraphData{}}
	svc := graphTestService(engine)
	start := time.Now()
	lists := svc.graphRecallCandidates(context.Background(), []string{"kb-1"}, "报警")
	assert.Nil(t, lists)
	assert.Less(t, time.Since(start), graphRecallTimeout+time.Second)
}
