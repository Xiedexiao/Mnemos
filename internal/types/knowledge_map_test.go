package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mapNode(id, folder string, tags ...string) KnowledgeMapNode {
	return KnowledgeMapNode{
		KnowledgeID: id, KnowledgeBaseID: "kb-1", Title: id,
		FolderPath: folder, Tags: tags,
	}
}

// TestKnowledgeMapViewParsing covers the query default.
func TestKnowledgeMapViewParsing(t *testing.T) {
	assert.Equal(t, KnowledgeMapViewRelation, ParseKnowledgeMapView("RELATION"))
	assert.Equal(t, KnowledgeMapViewTopic, ParseKnowledgeMapView(" topic "))
	assert.Equal(t, KnowledgeMapViewFolder, ParseKnowledgeMapView(""))
	assert.Equal(t, KnowledgeMapViewFolder, ParseKnowledgeMapView("nonsense"),
		"an unknown view falls back rather than erroring")
}

// TestBuildFolderClusters groups by exact path, not by root.
func TestBuildFolderClusters(t *testing.T) {
	nodes := []KnowledgeMapNode{
		mapNode("a", "sop"),
		mapNode("b", "sop"),
		mapNode("c", "sop/e45"),
		mapNode("d", ""),
	}
	clusters := BuildKnowledgeMapClusters(KnowledgeMapViewFolder, nodes, nil)
	require.Len(t, clusters, 3)

	byID := map[string]KnowledgeMapCluster{}
	for _, cluster := range clusters {
		byID[cluster.ID] = cluster
	}
	assert.EqualValues(t, 2, byID["sop"].NodeCount)
	assert.EqualValues(t, 1, byID["sop/e45"].NodeCount)
	assert.EqualValues(t, 1, byID[""].NodeCount)
	assert.Equal(t, "/", byID[""].Label, "the root folder still gets a readable label")
	// Largest cluster first.
	assert.Equal(t, "sop", clusters[0].ID)
}

// TestBuildTopicClustersAllowsMultiMembership is the documented semantic: a
// document with two tags appears in both clusters.
func TestBuildTopicClustersAllowsMultiMembership(t *testing.T) {
	nodes := []KnowledgeMapNode{
		mapNode("a", "", "工艺", "安全"),
		mapNode("b", "", "工艺"),
	}
	clusters := BuildKnowledgeMapClusters(KnowledgeMapViewTopic, nodes, nil)
	require.Len(t, clusters, 2)
	byID := map[string]KnowledgeMapCluster{}
	for _, cluster := range clusters {
		byID[cluster.ID] = cluster
	}
	assert.EqualValues(t, 2, byID["工艺"].NodeCount)
	assert.EqualValues(t, 1, byID["安全"].NodeCount)
	assert.Equal(t, "工艺", clusters[0].ID)
}

// TestBuildRelationClustersUsesComponents covers the connectivity grouping.
func TestBuildRelationClustersUsesComponents(t *testing.T) {
	nodes := []KnowledgeMapNode{mapNode("c", ""), mapNode("a", ""), mapNode("b", ""), mapNode("z", "")}
	edges := []KnowledgeMapEdge{
		{From: "b", To: "a", RelationType: "based_on"},
		{From: "a", To: "c", RelationType: "related"},
	}
	clusters := BuildKnowledgeMapClusters(KnowledgeMapViewRelation, nodes, edges)
	require.Len(t, clusters, 2)

	byID := map[string]KnowledgeMapCluster{}
	for _, cluster := range clusters {
		byID[cluster.ID] = cluster
	}
	// The component is identified by its lexicographically smallest member so
	// the id is stable across recomputations.
	require.Contains(t, byID, "a")
	assert.EqualValues(t, 3, byID["a"].NodeCount)
	assert.EqualValues(t, 2, byID["a"].EdgeCount)
	assert.ElementsMatch(t, []string{"a", "b", "c"}, byID["a"].KnowledgeIDs)
	require.Contains(t, byID, "z")
	assert.EqualValues(t, 1, byID["z"].NodeCount)
	assert.Zero(t, byID["z"].EdgeCount)
}

// TestBuildRelationClustersDropsEdgesToInvisibleNodes keeps a filtered node
// from appearing through an edge.
func TestBuildRelationClustersDropsEdgesToInvisibleNodes(t *testing.T) {
	nodes := []KnowledgeMapNode{mapNode("a", "")}
	edges := []KnowledgeMapEdge{{From: "a", To: "hidden", RelationType: "related"}}
	clusters := BuildKnowledgeMapClusters(KnowledgeMapViewRelation, nodes, edges)
	require.Len(t, clusters, 1)
	assert.Equal(t, []string{"a"}, clusters[0].KnowledgeIDs)
	assert.Zero(t, clusters[0].EdgeCount, "an edge to an invisible document does not count")
}

// TestKnowledgeMapStatsSeparatesIslandsAndHubs covers §4.6 governance input.
func TestKnowledgeMapStatsSeparatesIslandsAndHubs(t *testing.T) {
	nodes := []KnowledgeMapNode{mapNode("hub", ""), mapNode("mid", ""), mapNode("leaf", ""), mapNode("island", "")}
	edges := []KnowledgeMapEdge{
		{From: "hub", To: "mid"},
		{From: "hub", To: "leaf"},
	}
	islands, hubs := KnowledgeMapStats(nodes, edges)
	assert.Equal(t, []string{"island"}, islands)
	require.NotEmpty(t, hubs)
	assert.Equal(t, "hub", hubs[0])
	assert.Len(t, hubs, 3, "islands are not hubs")
}

// TestKnowledgeMapRootFolders covers the folder-view helper.
func TestKnowledgeMapRootFolders(t *testing.T) {
	roots := KnowledgeMapRootFolders([]KnowledgeMapNode{
		mapNode("a", "sop/e45"),
		mapNode("b", "sop/general"),
		mapNode("c", "manual"),
		mapNode("d", ""),
	})
	assert.Equal(t, []string{"manual", "sop"}, roots)
}

// TestKnowledgeMapClusterPreviewBoundsPayload keeps a large folder from
// inflating the response.
func TestKnowledgeMapClusterPreviewBoundsPayload(t *testing.T) {
	cluster := &KnowledgeMapCluster{
		KnowledgeIDs: []string{"a", "b", "c", "d"}, NodeCount: 4,
	}
	KnowledgeMapClusterPreview(cluster, 2)
	assert.Len(t, cluster.KnowledgeIDs, 2)
	assert.EqualValues(t, 4, cluster.NodeCount, "the count still reports the full membership")
}

// TestNormalizeKnowledgeMapLimit covers the clamp.
func TestNormalizeKnowledgeMapLimit(t *testing.T) {
	assert.Equal(t, DefaultKnowledgeMapNodes, NormalizeKnowledgeMapLimit(0))
	assert.Equal(t, DefaultKnowledgeMapNodes, NormalizeKnowledgeMapLimit(-5))
	assert.Equal(t, 50, NormalizeKnowledgeMapLimit(50))
	assert.Equal(t, MaxKnowledgeMapNodes, NormalizeKnowledgeMapLimit(99999))
}

// TestValidateKnowledgeMapTarget covers the request guard.
func TestValidateKnowledgeMapTarget(t *testing.T) {
	require.NoError(t, ValidateKnowledgeMapTarget("kb-1"))
	require.Error(t, ValidateKnowledgeMapTarget(""))
	require.Error(t, ValidateKnowledgeMapTarget("   "))
}
