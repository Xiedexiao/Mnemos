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

// stubKnowledgeMapRepo serves the map rows.
type stubKnowledgeMapRepo struct {
	interfaces.KnowledgeMapRepository

	nodes      []types.KnowledgeMapNode
	edges      []types.KnowledgeMapEdge
	roleNodes  []types.KnowledgeMapNode
	total      int64
	edgesGiven []string
	nodesErr   error
}

func (s *stubKnowledgeMapRepo) MapNodes(
	_ context.Context, _ uint64, _ string, _ int,
) ([]types.KnowledgeMapNode, error) {
	return s.nodes, s.nodesErr
}

func (s *stubKnowledgeMapRepo) MapEdges(
	_ context.Context, _ uint64, ids []string,
) ([]types.KnowledgeMapEdge, error) {
	s.edgesGiven = ids
	// 模拟仓库的"两端都可见"约束：只返回两端都在 ids 里的边。
	allowed := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		allowed[id] = struct{}{}
	}
	out := make([]types.KnowledgeMapEdge, 0, len(s.edges))
	for _, edge := range s.edges {
		_, fromOK := allowed[edge.From]
		_, toOK := allowed[edge.To]
		if fromOK && toOK {
			out = append(out, edge)
		}
	}
	return out, nil
}

func (s *stubKnowledgeMapRepo) CountSpaceKnowledge(
	context.Context, uint64, string,
) (int64, error) {
	return s.total, nil
}

func (s *stubKnowledgeMapRepo) RoleMapNodes(
	_ context.Context, _ uint64, _ []string, _ int,
) ([]types.KnowledgeMapNode, error) {
	return s.roleNodes, nil
}

// stubMapACL denies a fixed set.
type stubMapACL struct {
	interfaces.KnowledgeACLRepository
	denied []string
	err    error
}

func (s *stubMapACL) DeniedKnowledgeIDsForSubjects(
	context.Context, []string, []types.ACLSubject, types.PermissionAction, time.Time,
) ([]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.denied, nil
}

// stubMapLearning serves learning tasks.
type stubMapLearning struct {
	interfaces.LearningTaskService
	tasks []*types.LearningTask
	err   error
}

func (s *stubMapLearning) List(
	context.Context, uint64, string, types.LearningTaskStatus,
) ([]*types.LearningTask, error) {
	return s.tasks, s.err
}

func newMapFixture() (*knowledgeMapService, *stubKnowledgeMapRepo, *stubMapACL, *stubMapLearning) {
	repo := &stubKnowledgeMapRepo{
		nodes: []types.KnowledgeMapNode{
			{KnowledgeID: "a", KnowledgeBaseID: "kb-1", Title: "A", FolderPath: "sop"},
			{KnowledgeID: "b", KnowledgeBaseID: "kb-1", Title: "B", FolderPath: "sop"},
			{KnowledgeID: "secret", KnowledgeBaseID: "kb-1", Title: "机密", FolderPath: "sop"},
			{KnowledgeID: "island", KnowledgeBaseID: "kb-1", Title: "孤岛", FolderPath: "manual"},
		},
		edges: []types.KnowledgeMapEdge{
			{From: "a", To: "b", RelationType: "based_on"},
			{From: "b", To: "secret", RelationType: "related"},
		},
		total: 4,
	}
	acl := &stubMapACL{denied: []string{"secret"}}
	learning := &stubMapLearning{}
	svc := &knowledgeMapService{
		repo:     repo,
		acl:      acl,
		subjects: newACLSubjectResolver(nil),
		learning: learning,
		now:      func() time.Time { return time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC) },
	}
	return svc, repo, acl, learning
}

// TestSpaceMapDropsDeniedNodesAndTheirEdges is the security core: a denied
// document must not appear as a node, and an edge to it must not survive
// either — the edge alone would reveal it exists.
func TestSpaceMapDropsDeniedNodesAndTheirEdges(t *testing.T) {
	svc, _, _, _ := newMapFixture()
	knowledgeMap, err := svc.SpaceMap(recommendationCaller(), 7, "kb-1",
		types.KnowledgeMapViewFolder, 100)
	require.NoError(t, err)

	ids := make([]string, 0, len(knowledgeMap.Nodes))
	for _, node := range knowledgeMap.Nodes {
		ids = append(ids, node.KnowledgeID)
	}
	assert.NotContains(t, ids, "secret")
	assert.ElementsMatch(t, []string{"a", "b", "island"}, ids)

	require.Len(t, knowledgeMap.Edges, 1)
	assert.Equal(t, "a", knowledgeMap.Edges[0].From)
	assert.Equal(t, "b", knowledgeMap.Edges[0].To)
	assert.Equal(t, 1, knowledgeMap.HiddenCount, "the hidden count is an aggregate")
}

// TestSpaceMapReportsIslandsAndHubs covers the governance signals.
func TestSpaceMapReportsIslandsAndHubs(t *testing.T) {
	svc, _, _, _ := newMapFixture()
	knowledgeMap, err := svc.SpaceMap(recommendationCaller(), 7, "kb-1",
		types.KnowledgeMapViewRelation, 100)
	require.NoError(t, err)
	assert.Equal(t, []string{"island"}, knowledgeMap.Islands)
	require.NotEmpty(t, knowledgeMap.Hubs)
	assert.Equal(t, "a", knowledgeMap.Hubs[0])
}

// TestSpaceMapFailsClosedOnACLError keeps a broken ACL from producing a full
// map instead of an empty one.
func TestSpaceMapFailsClosedOnACLError(t *testing.T) {
	svc, _, acl, _ := newMapFixture()
	acl.err = errors.New("acl db down")

	knowledgeMap, err := svc.SpaceMap(recommendationCaller(), 7, "kb-1",
		types.KnowledgeMapViewFolder, 100)
	require.NoError(t, err)
	assert.Empty(t, knowledgeMap.Nodes)
	assert.Empty(t, knowledgeMap.Edges)
	assert.Equal(t, 4, knowledgeMap.HiddenCount,
		"every loaded document is reported hidden, which explains the empty map")
}

// TestSpaceMapAnonymousGetsNothing keeps the map from being a public listing.
func TestSpaceMapAnonymousGetsNothing(t *testing.T) {
	svc, _, _, _ := newMapFixture()
	knowledgeMap, err := svc.SpaceMap(context.Background(), 7, "kb-1",
		types.KnowledgeMapViewFolder, 100)
	require.NoError(t, err)
	assert.Empty(t, knowledgeMap.Nodes)
}

// TestSpaceMapRequiresTarget covers the request guard.
func TestSpaceMapRequiresTarget(t *testing.T) {
	svc, _, _, _ := newMapFixture()
	_, err := svc.SpaceMap(recommendationCaller(), 7, "  ", types.KnowledgeMapViewFolder, 10)
	require.Error(t, err)
}

// TestSpaceMapTruncates covers the node cap and its flag.
func TestSpaceMapTruncates(t *testing.T) {
	svc, repo, _, _ := newMapFixture()
	// The stub returns 4 documents; one is denied and the limit of 2 cuts one
	// more. HiddenCount covers everything not on the map: 500 - 2 = 498.
	repo.total = 500
	knowledgeMap, err := svc.SpaceMap(recommendationCaller(), 7, "kb-1",
		types.KnowledgeMapViewFolder, 2)
	require.NoError(t, err)
	assert.Len(t, knowledgeMap.Nodes, 2)
	assert.True(t, knowledgeMap.Truncated)
	assert.Equal(t, 498, knowledgeMap.HiddenCount)
}

// TestSpaceMapNodeDegrees covers the per-node degree used for hub styling.
func TestSpaceMapNodeDegrees(t *testing.T) {
	svc, _, _, _ := newMapFixture()
	knowledgeMap, err := svc.SpaceMap(recommendationCaller(), 7, "kb-1",
		types.KnowledgeMapViewFolder, 100)
	require.NoError(t, err)
	degree := map[string]int{}
	for _, node := range knowledgeMap.Nodes {
		degree[node.KnowledgeID] = node.Degree
	}
	assert.Equal(t, 1, degree["a"])
	assert.Equal(t, 1, degree["b"])
	assert.Zero(t, degree["island"])
}

// TestSpaceMapAssignsClusterIDs keeps every node's cluster resolvable.
func TestSpaceMapAssignsClusterIDs(t *testing.T) {
	svc, _, _, _ := newMapFixture()
	knowledgeMap, err := svc.SpaceMap(recommendationCaller(), 7, "kb-1",
		types.KnowledgeMapViewFolder, 100)
	require.NoError(t, err)
	clusterIDs := map[string]struct{}{}
	for _, cluster := range knowledgeMap.Clusters {
		clusterIDs[cluster.ID] = struct{}{}
	}
	for _, node := range knowledgeMap.Nodes {
		assert.Contains(t, clusterIDs, node.ClusterID,
			"%s must point at an existing cluster", node.KnowledgeID)
	}
}

// TestRoleMapCoverageCountsOnlyRoleTasks is the §15.5 linkage: coverage must
// reflect the role's documents, not every task the user happens to hold.
func TestRoleMapCoverageCountsOnlyRoleTasks(t *testing.T) {
	svc, repo, _, learning := newMapFixture()
	repo.roleNodes = []types.KnowledgeMapNode{
		{KnowledgeID: "a", KnowledgeBaseID: "kb-1", Title: "A", FolderPath: "sop"},
		{KnowledgeID: "b", KnowledgeBaseID: "kb-1", Title: "B", FolderPath: "sop"},
	}
	learning.tasks = []*types.LearningTask{
		{KnowledgeID: "a", Status: string(types.LearningTaskDone)},
		{KnowledgeID: "b", Status: string(types.LearningTaskOpen)},
		{KnowledgeID: "unrelated", Status: string(types.LearningTaskOpen)},
	}

	roleMap, err := svc.RoleMap(recommendationCaller(), 7, "u-1", []string{"工艺"}, 100)
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, roleMap.RequiredKnowledgeIDs)
	assert.Equal(t, []string{"a"}, roleMap.CompletedKnowledgeIDs)
	assert.InDelta(t, 50.0, roleMap.CoveragePct, 0.01,
		"an open task outside the role must not lower the role's coverage")
}

// TestRoleMapEmptyRequirementIsNotFullCoverage keeps a vacuous 100% out.
func TestRoleMapEmptyRequirementIsNotFullCoverage(t *testing.T) {
	svc, repo, _, _ := newMapFixture()
	repo.roleNodes = []types.KnowledgeMapNode{
		{KnowledgeID: "a", KnowledgeBaseID: "kb-1", Title: "A"},
	}
	roleMap, err := svc.RoleMap(recommendationCaller(), 7, "u-1", []string{"工艺"}, 100)
	require.NoError(t, err)
	assert.Zero(t, roleMap.CoveragePct, "no requirement is not the same as fully covered")
	assert.Empty(t, roleMap.RequiredKnowledgeIDs)
}

// TestRoleMapRequiresTags covers the input guard.
func TestRoleMapRequiresTags(t *testing.T) {
	svc, _, _, _ := newMapFixture()
	_, err := svc.RoleMap(recommendationCaller(), 7, "u-1", []string{"  "}, 100)
	require.Error(t, err)
	_, err = svc.RoleMap(recommendationCaller(), 7, "", []string{"工艺"}, 100)
	require.Error(t, err)
}

// TestRoleMapGroupsByFolder covers the role map's clustering.
func TestRoleMapGroupsByFolder(t *testing.T) {
	svc, repo, _, _ := newMapFixture()
	repo.roleNodes = []types.KnowledgeMapNode{
		{KnowledgeID: "a", KnowledgeBaseID: "kb-1", FolderPath: "sop"},
		{KnowledgeID: "b", KnowledgeBaseID: "kb-1", FolderPath: "sop"},
		{KnowledgeID: "c", KnowledgeBaseID: "kb-1", FolderPath: "manual"},
	}
	roleMap, err := svc.RoleMap(recommendationCaller(), 7, "u-1", []string{"工艺"}, 100)
	require.NoError(t, err)
	require.Len(t, roleMap.Clusters, 2)
	assert.EqualValues(t, 2, roleMap.Clusters[0].NodeCount)
}

// TestRoleMapFilteredByACL covers the permission gate on the role map.
func TestRoleMapFilteredByACL(t *testing.T) {
	svc, repo, acl, _ := newMapFixture()
	repo.roleNodes = []types.KnowledgeMapNode{
		{KnowledgeID: "a", KnowledgeBaseID: "kb-1"},
		{KnowledgeID: "secret", KnowledgeBaseID: "kb-1"},
	}
	acl.denied = []string{"secret"}
	roleMap, err := svc.RoleMap(recommendationCaller(), 7, "u-1", []string{"工艺"}, 100)
	require.NoError(t, err)
	require.Len(t, roleMap.Nodes, 1)
	assert.Equal(t, "a", roleMap.Nodes[0].KnowledgeID)
}

// TestRoleMapWorksWithoutLearningService keeps the map usable in deployments
// that never turned learning tasks on.
func TestRoleMapWorksWithoutLearningService(t *testing.T) {
	svc, repo, _, _ := newMapFixture()
	svc.learning = nil
	repo.roleNodes = []types.KnowledgeMapNode{{KnowledgeID: "a", KnowledgeBaseID: "kb-1"}}
	roleMap, err := svc.RoleMap(recommendationCaller(), 7, "u-1", []string{"工艺"}, 100)
	require.NoError(t, err)
	assert.Len(t, roleMap.Nodes, 1)
	assert.Zero(t, roleMap.CoveragePct)
}

// TestRoleMapWaivedCountsAsCoveredButStaysRequired covers the waived rule.
func TestRoleMapWaivedCountsAsCoveredButStaysRequired(t *testing.T) {
	svc, repo, _, learning := newMapFixture()
	repo.roleNodes = []types.KnowledgeMapNode{{KnowledgeID: "a", KnowledgeBaseID: "kb-1"}}
	learning.tasks = []*types.LearningTask{
		{KnowledgeID: "a", Status: string(types.LearningTaskWaived)},
	}
	roleMap, err := svc.RoleMap(recommendationCaller(), 7, "u-1", []string{"工艺"}, 100)
	require.NoError(t, err)
	assert.Equal(t, []string{"a"}, roleMap.RequiredKnowledgeIDs)
	assert.Equal(t, []string{"a"}, roleMap.CompletedKnowledgeIDs)
	assert.InDelta(t, 100.0, roleMap.CoveragePct, 0.01)
}
