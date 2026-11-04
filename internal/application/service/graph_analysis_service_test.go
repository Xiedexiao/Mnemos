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

// stubGraphAnalysisRepo serves the analysis rows.
type stubGraphAnalysisRepo struct {
	interfaces.GraphAnalysisRepository

	nodes     []types.GraphAnalysisNode
	edges     []types.KnowledgeMapEdge
	authorOf  map[string]string
	counters  []types.ExpertAnswerCounters
	citations map[string]int
	nodesErr  error
	edgesErr  error
	// requestedIDs records what the service asked for, so the "both ends
	// visible" contract can be asserted.
	requestedIDs []string
}

func (s *stubGraphAnalysisRepo) AnalysisNodes(
	_ context.Context, _ uint64, _ string, _ int,
) ([]types.GraphAnalysisNode, error) {
	return s.nodes, s.nodesErr
}

func (s *stubGraphAnalysisRepo) AnalysisEdges(
	_ context.Context, _ uint64, ids []string,
) ([]types.KnowledgeMapEdge, error) {
	s.requestedIDs = ids
	if s.edgesErr != nil {
		return nil, s.edgesErr
	}
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

func (s *stubGraphAnalysisRepo) ExpertAuthorship(
	_ context.Context, _ uint64, ids []string,
) (map[string]string, error) {
	allowed := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		allowed[id] = struct{}{}
	}
	out := make(map[string]string, len(s.authorOf))
	for id, user := range s.authorOf {
		if _, ok := allowed[id]; ok {
			out[id] = user
		}
	}
	return out, nil
}

func (s *stubGraphAnalysisRepo) ExpertAnswerCounters(
	_ context.Context, _ uint64, _ []string, _ time.Time,
) ([]types.ExpertAnswerCounters, error) {
	return s.counters, nil
}

func (s *stubGraphAnalysisRepo) ExpertCitationCounts(
	_ context.Context, _ uint64, ids []string, _ time.Time, _ int,
) (map[string]int, error) {
	allowed := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		allowed[id] = struct{}{}
	}
	out := make(map[string]int, len(s.citations))
	for id, count := range s.citations {
		if _, ok := allowed[id]; ok {
			out[id] = count
		}
	}
	return out, nil
}

// stubGraphAnalysisUsers resolves usernames.
type stubGraphAnalysisUsers struct {
	interfaces.UserService
	users map[string]*types.User
}

func (s *stubGraphAnalysisUsers) GetUsersByIDs(
	_ context.Context, _ []string,
) (map[string]*types.User, error) {
	return s.users, nil
}

// stubGraphAnalysisACL denies a fixed set.
type stubGraphAnalysisACL struct {
	interfaces.KnowledgeACLRepository
	denied []string
	err    error
}

func (s *stubGraphAnalysisACL) DeniedKnowledgeIDsForSubjects(
	context.Context, []string, []types.ACLSubject, types.PermissionAction, time.Time,
) ([]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.denied, nil
}

// newGraphAnalysisServiceForTest builds the service against stubs.
func newGraphAnalysisServiceForTest(
	repo interfaces.GraphAnalysisRepository,
	acl interfaces.KnowledgeACLRepository,
	users interfaces.UserService,
) interfaces.GraphAnalysisService {
	svc := NewGraphAnalysisService(repo, acl, nil, users, nil).(*graphAnalysisService)
	svc.now = func() time.Time {
		return time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	}
	return svc
}

// analysisCallerCtx carries the caller identity the ACL gate needs.
func analysisCallerCtx() context.Context {
	return types.WithCaller(context.Background(), types.Caller{
		TenantID: 7, UserID: "reader", Role: types.TenantRoleViewer,
	})
}

// TestCentralityDropsDeniedNodesAndTheirEdges is the §11.3 endpoint rule at the
// service level: a hidden document must vanish from the ranking AND its edges
// must vanish, or the ranking itself names it indirectly.
func TestCentralityDropsDeniedNodesAndTheirEdges(t *testing.T) {
	repo := &stubGraphAnalysisRepo{
		nodes: []types.GraphAnalysisNode{
			{KnowledgeID: "a", KnowledgeBaseID: "kb-1", Title: "A", LifecycleStatus: "valid"},
			{KnowledgeID: "b", KnowledgeBaseID: "kb-1", Title: "B", LifecycleStatus: "valid"},
			{KnowledgeID: "secret", KnowledgeBaseID: "kb-1", Title: "Secret", LifecycleStatus: "valid"},
		},
		edges: []types.KnowledgeMapEdge{
			{From: "a", To: "b", RelationType: "based_on"},
			{From: "a", To: "secret", RelationType: "based_on"},
		},
	}
	acl := &stubGraphAnalysisACL{denied: []string{"secret"}}
	svc := newGraphAnalysisServiceForTest(repo, acl, nil)

	report, err := svc.Centrality(analysisCallerCtx(), 7, "kb-1", 0)
	require.NoError(t, err)
	require.Len(t, report.Nodes, 2)
	for _, entry := range report.Nodes {
		assert.NotEqual(t, "secret", entry.KnowledgeID, "a denied document must not be ranked")
	}
	assert.NotContains(t, report.Titles, "secret")
	assert.Equal(t, 1, report.EdgeCount, "the edge to the denied node must be gone")
	assert.Equal(t, []string{"a", "b"}, repo.requestedIDs,
		"edges are looked up only for the visible set")
}

// TestCentralityWeightsLifecycleStatus proves a valid document's dependency
// counts more than a stale one's.
func TestCentralityWeightsLifecycleStatus(t *testing.T) {
	// Both a and b are depended on exactly once: by a valid and a stale
	// document respectively. a must rank first.
	repo := &stubGraphAnalysisRepo{
		nodes: []types.GraphAnalysisNode{
			{KnowledgeID: "a", KnowledgeBaseID: "kb-1", LifecycleStatus: "valid"},
			{KnowledgeID: "b", KnowledgeBaseID: "kb-1", LifecycleStatus: "valid"},
			{KnowledgeID: "from-valid", KnowledgeBaseID: "kb-1", LifecycleStatus: "valid"},
			{KnowledgeID: "from-stale", KnowledgeBaseID: "kb-1", LifecycleStatus: "stale"},
		},
		edges: []types.KnowledgeMapEdge{
			{From: "from-valid", To: "a", RelationType: "based_on"},
			{From: "from-stale", To: "b", RelationType: "based_on"},
		},
	}
	svc := newGraphAnalysisServiceForTest(repo, nil, nil)
	report, err := svc.Centrality(analysisCallerCtx(), 7, "kb-1", 0)
	require.NoError(t, err)

	scores := make(map[string]float64, len(report.Nodes))
	for _, entry := range report.Nodes {
		scores[entry.KnowledgeID] = entry.Score
	}
	assert.Greater(t, scores["a"], scores["b"],
		"a dependency from a valid document outweighs one from a stale document")
}

// TestCentralityFailsClosedOnACLError is the fail-closed rule: an ACL lookup
// failure must not degrade into an unfiltered graph.
func TestCentralityFailsClosedOnACLError(t *testing.T) {
	repo := &stubGraphAnalysisRepo{
		nodes: []types.GraphAnalysisNode{
			{KnowledgeID: "a", KnowledgeBaseID: "kb-1", LifecycleStatus: "valid"},
		},
	}
	acl := &stubGraphAnalysisACL{err: errors.New("acl backend down")}
	svc := newGraphAnalysisServiceForTest(repo, acl, nil)

	report, err := svc.Centrality(analysisCallerCtx(), 7, "kb-1", 0)
	require.Error(t, err)
	assert.Nil(t, report)
}

// TestCentralityRequiresIdentity refuses anonymous callers, who would otherwise
// get a complete inventory of the space.
func TestCentralityRequiresIdentity(t *testing.T) {
	repo := &stubGraphAnalysisRepo{
		nodes: []types.GraphAnalysisNode{
			{KnowledgeID: "a", KnowledgeBaseID: "kb-1", LifecycleStatus: "valid"},
		},
	}
	svc := newGraphAnalysisServiceForTest(repo, nil, nil)
	_, err := svc.Centrality(context.Background(), 7, "kb-1", 0)
	require.Error(t, err)
}

// TestCentralityRequiresKnowledgeBase guards the request shape.
func TestCentralityRequiresKnowledgeBase(t *testing.T) {
	svc := newGraphAnalysisServiceForTest(&stubGraphAnalysisRepo{}, nil, nil)
	_, err := svc.Centrality(analysisCallerCtx(), 7, "   ", 0)
	require.Error(t, err)
}

// TestCommunitiesReportsIslands keeps the §11.1 盲区 signal.
func TestCommunitiesReportsIslands(t *testing.T) {
	repo := &stubGraphAnalysisRepo{
		nodes: []types.GraphAnalysisNode{
			{KnowledgeID: "a", KnowledgeBaseID: "kb-1", LifecycleStatus: "valid"},
			{KnowledgeID: "b", KnowledgeBaseID: "kb-1", LifecycleStatus: "valid"},
			{KnowledgeID: "lonely", KnowledgeBaseID: "kb-1", LifecycleStatus: "valid"},
		},
		edges: []types.KnowledgeMapEdge{{From: "a", To: "b", RelationType: "based_on"}},
	}
	svc := newGraphAnalysisServiceForTest(repo, nil, nil)
	report, err := svc.Communities(analysisCallerCtx(), 7, "kb-1", 0)
	require.NoError(t, err)
	assert.Equal(t, []string{"lonely"}, report.IsolatedNodeIDs)
	require.Len(t, report.Communities, 1)
	assert.Equal(t, []string{"a", "b"}, report.Communities[0].NodeIDs)
}

// TestExpertsAggregatesThreeSignals covers the scoring inputs end to end.
func TestExpertsAggregatesThreeSignals(t *testing.T) {
	repo := &stubGraphAnalysisRepo{
		nodes: []types.GraphAnalysisNode{
			{KnowledgeID: "d1", KnowledgeBaseID: "kb-1", LifecycleStatus: "valid"},
			{KnowledgeID: "d2", KnowledgeBaseID: "kb-1", LifecycleStatus: "valid"},
			{KnowledgeID: "d3", KnowledgeBaseID: "kb-1", LifecycleStatus: "valid"},
			{KnowledgeID: "d4", KnowledgeBaseID: "kb-1", LifecycleStatus: "valid"},
		},
		authorOf: map[string]string{
			"d1": "ana", "d2": "ana", "d3": "bo", "d4": "bo",
		},
		citations: map[string]int{"d1": 2, "d3": 1},
		counters: []types.ExpertAnswerCounters{
			{UserID: "ana", AcceptedAnswers: 2, UsefulRatings: 1},
		},
	}
	users := &stubGraphAnalysisUsers{users: map[string]*types.User{
		"ana": {ID: "ana", Username: "Ana"},
		"bo":  {ID: "bo", Username: "Bo"},
	}}
	svc := newGraphAnalysisServiceForTest(repo, nil, users)
	report, err := svc.Experts(analysisCallerCtx(), 7, "kb-1", 0, 0)
	require.NoError(t, err)
	require.Len(t, report.Experts, 2)

	byUser := make(map[string]types.AnalysisExpertEntry, 2)
	for _, entry := range report.Experts {
		byUser[entry.UserID] = entry
	}
	// ana: 2 authored + 2 accepted (10) + 1 useful (2) + 2 citations (6) = 20
	assert.InDelta(t, 20.0, byUser["ana"].Score, 0.01)
	// bo: 2 authored + 1 citation (3) = 5
	assert.InDelta(t, 5.0, byUser["bo"].Score, 0.01)
	assert.Equal(t, "ana", report.Experts[0].UserID)
	assert.Equal(t, "Ana", report.Experts[0].DisplayName)
	// The weights are echoed so the score can be recomputed.
	assert.InDelta(t, 5.0, report.Weights.AcceptedAnswers, 0.001)
	assert.Equal(t, types.MinExpertEvidence, report.MinEvidence)
}

// TestExpertsHidesPeopleBelowEvidenceFloor is the §11.3 anti-inference rule:
// listing someone as "insufficient evidence" is itself a disclosure, so they
// are omitted entirely.
func TestExpertsHidesPeopleBelowEvidenceFloor(t *testing.T) {
	repo := &stubGraphAnalysisRepo{
		nodes: []types.GraphAnalysisNode{
			{KnowledgeID: "d1", KnowledgeBaseID: "kb-1", LifecycleStatus: "valid"},
		},
		authorOf: map[string]string{"d1": "quiet"},
	}
	svc := newGraphAnalysisServiceForTest(repo, nil, nil)
	report, err := svc.Experts(analysisCallerCtx(), 7, "kb-1", 0, 0)
	require.NoError(t, err)
	assert.Empty(t, report.Experts, "one document is not enough to be called an expert")
}

// TestExpertsReranksAfterFiltering keeps ranks contiguous: the floored users
// are removed, so leaving the original ranks would show gaps.
func TestExpertsReranksAfterFiltering(t *testing.T) {
	repo := &stubGraphAnalysisRepo{
		nodes: []types.GraphAnalysisNode{
			{KnowledgeID: "d1", KnowledgeBaseID: "kb-1", LifecycleStatus: "valid"},
			{KnowledgeID: "d2", KnowledgeBaseID: "kb-1", LifecycleStatus: "valid"},
			{KnowledgeID: "d3", KnowledgeBaseID: "kb-1", LifecycleStatus: "valid"},
		},
		authorOf: map[string]string{
			"d1": "small", "d2": "big", "d3": "big",
		},
		citations: map[string]int{"d2": 5, "d3": 5},
	}
	svc := newGraphAnalysisServiceForTest(repo, nil, nil)
	report, err := svc.Experts(analysisCallerCtx(), 7, "kb-1", 0, 0)
	require.NoError(t, err)
	require.Len(t, report.Experts, 1)
	assert.Equal(t, "big", report.Experts[0].UserID)
	assert.Equal(t, 1, report.Experts[0].Rank, "ranks are contiguous after filtering")
}

// TestExpertsCountsOnlyVisibleDocuments proves deny-wins reaches the ranking:
// a denied document neither credits its author nor leaks their name.
func TestExpertsCountsOnlyVisibleDocuments(t *testing.T) {
	repo := &stubGraphAnalysisRepo{
		nodes: []types.GraphAnalysisNode{
			{KnowledgeID: "visible", KnowledgeBaseID: "kb-1", LifecycleStatus: "valid"},
			{KnowledgeID: "hidden", KnowledgeBaseID: "kb-1", LifecycleStatus: "valid"},
			{KnowledgeID: "v2", KnowledgeBaseID: "kb-1", LifecycleStatus: "valid"},
			{KnowledgeID: "v3", KnowledgeBaseID: "kb-1", LifecycleStatus: "valid"},
		},
		authorOf: map[string]string{
			"visible": "reader", "hidden": "insider",
			"v2": "reader", "v3": "reader",
		},
	}
	acl := &stubGraphAnalysisACL{denied: []string{"hidden"}}
	svc := newGraphAnalysisServiceForTest(repo, acl, nil)
	report, err := svc.Experts(analysisCallerCtx(), 7, "kb-1", 0, 0)
	require.NoError(t, err)
	for _, entry := range report.Experts {
		assert.NotEqual(t, "insider", entry.UserID, "a denied document must not credit its author")
	}
}

// TestExpertsWindowIsEchoed makes the look-back window auditable.
func TestExpertsWindowIsEchoed(t *testing.T) {
	repo := &stubGraphAnalysisRepo{}
	svc := newGraphAnalysisServiceForTest(repo, nil, nil)
	report, err := svc.Experts(analysisCallerCtx(), 7, "kb-1", 0, 30)
	require.NoError(t, err)
	assert.Equal(t, 30, report.WindowDays)
	assert.Equal(t, report.GeneratedAt.AddDate(0, 0, -30).UTC(), report.Since.UTC())
}

// TestAnalysisAuthorityWeight pins the weight table.
func TestAnalysisAuthorityWeight(t *testing.T) {
	assert.InDelta(t, 1.0, types.AnalysisAuthorityWeight("valid"), 0.001)
	assert.InDelta(t, 1.0, types.AnalysisAuthorityWeight(""), 0.001,
		"legacy rows without a status keep full weight")
	assert.InDelta(t, 0.5, types.AnalysisAuthorityWeight("stale"), 0.001)
	assert.InDelta(t, 0.5, types.AnalysisAuthorityWeight("pending_review_cycle"), 0.001)
	assert.Zero(t, types.AnalysisAuthorityWeight("draft"))
}
