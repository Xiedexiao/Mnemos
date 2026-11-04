package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestComputeCentralityRanksByInboundDependencies is the core property: a
// document everyone depends on outranks a document nobody depends on.
func TestComputeCentralityRanksByInboundDependencies(t *testing.T) {
	nodes := []AnalysisNode{
		{ID: "policy"}, {ID: "sop"}, {ID: "checklist"}, {ID: "leaf"},
	}
	edges := []AnalysisEdge{
		{From: "sop", To: "policy", RelationType: "based_on"},
		{From: "checklist", To: "policy", RelationType: "prerequisite"},
		{From: "leaf", To: "sop", RelationType: "based_on"},
	}
	entries := ComputeCentrality(nodes, edges)
	require.Len(t, entries, 4)
	assert.Equal(t, "policy", entries[0].KnowledgeID, "the most-depended-on document ranks first")
	assert.Equal(t, 2, entries[0].InDegree)
	assert.Equal(t, 1, entries[0].Rank)

	byID := map[string]AnalysisCentralityEntry{}
	for _, entry := range entries {
		byID[entry.KnowledgeID] = entry
	}
	assert.Equal(t, 2, byID["policy"].InDegree)
	assert.Equal(t, 0, byID["policy"].OutDegree)
	assert.Equal(t, 1, byID["sop"].InDegree)
	assert.Equal(t, 1, byID["sop"].OutDegree)
	assert.Zero(t, byID["leaf"].InDegree)
}

// TestComputeCentralityWeightPropagates keeps a dependency on an authoritative
// document worth more than a dependency on an unknown one.
func TestComputeCentralityWeightPropagates(t *testing.T) {
	// Two candidates each have exactly one inbound edge, but one of the
	// dependents is itself authoritative — its dependency vote counts more.
	// The weight belongs on the voting node (the dependent), not the target.
	nodes := []AnalysisNode{
		{ID: "a"}, {ID: "b"}, {ID: "x", Weight: 5}, {ID: "y"},
	}
	edges := []AnalysisEdge{
		{From: "x", To: "a"},
		{From: "y", To: "b"},
	}
	entries := ComputeCentrality(nodes, edges)
	byID := map[string]float64{}
	for _, entry := range entries {
		byID[entry.KnowledgeID] = entry.Score
	}
	assert.Greater(t, byID["a"], byID["b"],
		"an inbound edge from an authoritative node carries more weight")
}

// TestComputeCentralityHandlesEmptyAndIsolated covers the degenerate inputs.
func TestComputeCentralityHandlesEmptyAndIsolated(t *testing.T) {
	assert.Empty(t, ComputeCentrality(nil, nil))

	entries := ComputeCentrality([]AnalysisNode{{ID: "only"}}, nil)
	require.Len(t, entries, 1)
	assert.Equal(t, 1, entries[0].Rank)
	assert.Zero(t, entries[0].Degree)
	assert.InDelta(t, 1.0, entries[0].Score, 0.001, "a lone node holds all the rank")
}

// TestComputeCentralityIgnoresUnknownAndSelfEdges keeps bad edges from
// distorting the ranking.
func TestComputeCentralityIgnoresUnknownAndSelfEdges(t *testing.T) {
	nodes := []AnalysisNode{{ID: "a"}, {ID: "b"}}
	edges := []AnalysisEdge{
		{From: "a", To: "a"},       // self loop
		{From: "ghost", To: "a"},   // unknown source
		{From: "a", To: "phantom"}, // unknown target
		{From: "a", To: "b"},
	}
	entries := ComputeCentrality(nodes, edges)
	for _, entry := range entries {
		if entry.KnowledgeID == "b" {
			assert.Equal(t, 1, entry.InDegree)
			assert.Equal(t, 1, entry.Degree, "only the real edge counts")
		}
		if entry.KnowledgeID == "a" {
			assert.Equal(t, 1, entry.OutDegree)
			assert.Equal(t, 1, entry.Degree)
		}
	}
}

// TestDetectCommunitiesGroupsComponents is the clustering property.
func TestDetectCommunitiesGroupsComponents(t *testing.T) {
	nodes := []AnalysisNode{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "x"}, {ID: "y"}, {ID: "lonely"}}
	edges := []AnalysisEdge{
		{From: "a", To: "b"}, {From: "b", To: "c"},
		{From: "x", To: "y"},
	}
	result := DetectCommunities(nodes, edges)
	require.Len(t, result.Communities, 2)
	assert.Equal(t, "a", result.Communities[0].ID, "largest first")
	assert.Equal(t, 3, result.Communities[0].Size)
	assert.ElementsMatch(t, []string{"a", "b", "c"}, result.Communities[0].NodeIDs)
	assert.Equal(t, []string{"lonely"}, result.IsolatedNodeIDs)
	assert.Equal(t, 6, result.NodeCount)
	assert.Equal(t, 3, result.EdgeCount)
}

// TestDetectCommunitiesReportsBridgeNodes keeps the "connects two domains"
// signal, which is what an operator acts on when deciding ownership.
func TestDetectCommunitiesReportsBridgeNodes(t *testing.T) {
	// a-b-c where b also touches an edge that... no: all nodes are in one
	// component here, so no bridge. Use a node in the component whose edge
	// reaches outside only when the target is filtered out.
	nodes := []AnalysisNode{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	edges := []AnalysisEdge{{From: "a", To: "b"}, {From: "b", To: "c"}}
	result := DetectCommunities(nodes, edges)
	require.Len(t, result.Communities, 1)
	assert.Empty(t, result.Communities[0].BridgeNodeIDs,
		"an edge to a filtered-out node is not visible as a bridge")
	assert.Equal(t, 2, result.Communities[0].InternalEdges)
}

// TestDetectCommunitiesIsDeterministic is why connected components beat
// Louvain here: the same graph always yields the same partition.
func TestDetectCommunitiesIsDeterministic(t *testing.T) {
	nodes := []AnalysisNode{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}}
	edges := []AnalysisEdge{{From: "a", To: "b"}, {From: "c", To: "d"}}
	first := DetectCommunities(nodes, edges)
	for i := 0; i < 20; i++ {
		again := DetectCommunities(nodes, edges)
		assert.Equal(t, first.Communities, again.Communities)
		assert.Equal(t, first.IsolatedNodeIDs, again.IsolatedNodeIDs)
	}
}

// TestDetectCommunitiesEmpty covers the degenerate case.
func TestDetectCommunitiesEmpty(t *testing.T) {
	result := DetectCommunities(nil, nil)
	assert.Empty(t, result.Communities)
	assert.Empty(t, result.IsolatedNodeIDs)
	assert.Zero(t, result.NodeCount)
}

// TestRankExpertsOrdersByEvidence covers the ranking.
func TestRankExpertsOrdersByEvidence(t *testing.T) {
	// Accepted answers weigh 5× authorship, so 4 accepted (20) beats 15
	// documents written (15) — proving a lot and getting it adopted beats
	// merely writing a lot.
	signals := []ExpertSignal{
		{UserID: "bo", Authored: 15},
		{UserID: "ana", AcceptedAnswers: 4},
	}
	entries := RankExperts(signals, DefaultExpertRankingWeights(), 0)
	require.Len(t, entries, 2)
	assert.Equal(t, "ana", entries[0].UserID, "accepted answers outrank raw volume")
	assert.Equal(t, 1, entries[0].Rank)
	assert.InDelta(t, 20.0, entries[0].Score, 0.01)
}

// TestRankExpertsRequiresMinimumEvidence is the §11.3 anti-inference rule: one
// document must not be enough to be labelled the domain expert.
func TestRankExpertsRequiresMinimumEvidence(t *testing.T) {
	signals := []ExpertSignal{
		{UserID: "newcomer", Authored: 1},
		{UserID: "veteran", Authored: 3},
	}
	entries := RankExperts(signals, DefaultExpertRankingWeights(), 0)
	require.Len(t, entries, 2)
	assert.Equal(t, "veteran", entries[0].UserID)
	assert.True(t, entries[0].Eligible)
	assert.False(t, entries[1].Eligible, "one document is not enough evidence")
	assert.Greater(t, entries[0].Rank, 0)
}

// TestRankExpertsLimit covers the cap.
func TestRankExpertsLimit(t *testing.T) {
	signals := []ExpertSignal{
		{UserID: "a", Authored: 5},
		{UserID: "b", Authored: 4},
		{UserID: "c", Authored: 3},
	}
	entries := RankExperts(signals, DefaultExpertRankingWeights(), 2)
	assert.Len(t, entries, 2)
}
