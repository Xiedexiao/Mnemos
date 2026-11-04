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

// stubRecommendationRepo serves recommendation queries from fixtures.
type stubRecommendationRepo struct {
	interfaces.RecommendationRepository

	related  []types.RecommendationItem
	sameTag  []types.RecommendationItem
	sameKw   []types.RecommendationItem
	interest []string
	forTags  []types.RecommendationItem
	latest   []types.RecommendationItem
	hot      []types.RecommendationItem
	summary  map[string]types.RecommendationItem
	history  []*types.KnowledgeViewEvent
	views    []string
}

func (s *stubRecommendationRepo) RecordView(
	_ context.Context, _ uint64, _ string, knowledgeID, _ string, _ time.Time,
) (bool, error) {
	s.views = append(s.views, knowledgeID)
	return true, nil
}

func (s *stubRecommendationRepo) ListHistory(
	_ context.Context, _ uint64, _ string, _ int,
) ([]*types.KnowledgeViewEvent, error) {
	return s.history, nil
}

func (s *stubRecommendationRepo) RelatedKnowledge(
	context.Context, uint64, string, int,
) ([]types.RecommendationItem, error) {
	return s.related, nil
}

func (s *stubRecommendationRepo) SameTagKnowledge(
	context.Context, uint64, string, int,
) ([]types.RecommendationItem, error) {
	return s.sameTag, nil
}

func (s *stubRecommendationRepo) SameSpaceKnowledge(
	context.Context, uint64, string, string, int,
) ([]types.RecommendationItem, error) {
	return s.sameKw, nil
}

func (s *stubRecommendationRepo) InterestTags(
	context.Context, uint64, string, time.Time, int,
) ([]string, error) {
	return s.interest, nil
}

func (s *stubRecommendationRepo) KnowledgeForTags(
	context.Context, uint64, []string, string, int,
) ([]types.RecommendationItem, error) {
	return s.forTags, nil
}

func (s *stubRecommendationRepo) LatestKnowledge(
	context.Context, uint64, string, int,
) ([]types.RecommendationItem, error) {
	return s.latest, nil
}

func (s *stubRecommendationRepo) HotKnowledge(
	context.Context, uint64, string, time.Time, int,
) ([]types.RecommendationItem, error) {
	return s.hot, nil
}

func (s *stubRecommendationRepo) KnowledgeSummary(
	_ context.Context, _ uint64, ids []string,
) (map[string]types.RecommendationItem, error) {
	out := make(map[string]types.RecommendationItem, len(ids))
	for _, id := range ids {
		if item, ok := s.summary[id]; ok {
			out[id] = item
		}
	}
	return out, nil
}

// stubRecommendationACL denies a fixed set.
type stubRecommendationACL struct {
	interfaces.KnowledgeACLRepository
	denied []string
	err    error
}

func (s *stubRecommendationACL) DeniedKnowledgeIDsForSubjects(
	context.Context, []string, []types.ACLSubject, types.PermissionAction, time.Time,
) ([]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.denied, nil
}

// stubKnowledgeRepo serves the anchor lookup.
type stubRecommendationKnowledge struct {
	interfaces.KnowledgeRepository
	rows map[string]*types.Knowledge
}

func (s *stubRecommendationKnowledge) GetKnowledgeByID(
	_ context.Context, _ uint64, id string,
) (*types.Knowledge, error) {
	if knowledge, ok := s.rows[id]; ok {
		return knowledge, nil
	}
	return nil, errors.New("not found")
}

func newRecommendationFixture() (*recommendationService, *stubRecommendationRepo) {
	repo := &stubRecommendationRepo{
		summary: map[string]types.RecommendationItem{
			"k1": {KnowledgeID: "k1", KnowledgeBaseID: "kb1", Title: "E45 换型 SOP"},
			"k2": {KnowledgeID: "k2", KnowledgeBaseID: "kb1", Title: "锁具规程"},
			"k3": {KnowledgeID: "k3", KnowledgeBaseID: "kb1", Title: "机密工艺卡"},
		},
	}
	svc := &recommendationService{
		repo: repo,
		knowledges: &stubRecommendationKnowledge{rows: map[string]*types.Knowledge{
			"anchor": {ID: "anchor", KnowledgeBaseID: "kb1", Title: "锚点"},
		}},
		acl:      &stubRecommendationACL{denied: []string{"k3"}},
		weights:  types.DefaultRecommendationWeights(),
		now:      func() time.Time { return time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC) },
		subjects: newACLSubjectResolver(nil),
	}
	return svc, repo
}

func recommendationCaller() context.Context {
	return types.WithCaller(context.Background(), types.Caller{
		TenantID: 7, UserID: "u-1", Role: types.TenantRoleViewer,
	})
}

// TestRecommendationBoardFiltersDenied is the security property: a document
// the caller may not open must not appear as a recommendation at all.
func TestRecommendationBoardFiltersDenied(t *testing.T) {
	svc, repo := newRecommendationFixture()
	repo.related = []types.RecommendationItem{
		{KnowledgeID: "k1", KnowledgeBaseID: "kb1", UpdatedAt: time.Now()},
		{KnowledgeID: "k3", KnowledgeBaseID: "kb1", UpdatedAt: time.Now()},
	}

	board, err := svc.Board(recommendationCaller(), 7, "anchor", 5)
	require.NoError(t, err)
	require.Len(t, board.Related, 1)
	assert.Equal(t, "k1", board.Related[0].KnowledgeID)
	for _, item := range board.Related {
		assert.NotEqual(t, "k3", item.KnowledgeID)
	}
}

// TestRecommendationFailsClosedOnACLError keeps a broken ACL query from
// degrading into "show everything".
func TestRecommendationFailsClosedOnACLError(t *testing.T) {
	svc, repo := newRecommendationFixture()
	repo.related = []types.RecommendationItem{
		{KnowledgeID: "k1", KnowledgeBaseID: "kb1", UpdatedAt: time.Now()},
	}
	svc.acl = &stubRecommendationACL{err: errors.New("acl db down")}

	board, err := svc.Board(recommendationCaller(), 7, "anchor", 5)
	require.NoError(t, err)
	assert.Empty(t, board.Related, "no verdict means no recommendations")
}

// TestRecommendationRelatedIncludesSameTagFallback covers the merge: when
// typed relations are thin, same-tag documents fill the list.
func TestRecommendationRelatedIncludesSameTagFallback(t *testing.T) {
	svc, repo := newRecommendationFixture()
	repo.related = []types.RecommendationItem{
		{KnowledgeID: "k1", KnowledgeBaseID: "kb1", UpdatedAt: time.Now()},
	}
	repo.sameTag = []types.RecommendationItem{
		{KnowledgeID: "k2", KnowledgeBaseID: "kb1", UpdatedAt: time.Now(), MatchedTag: "工艺"},
	}

	board, err := svc.Board(recommendationCaller(), 7, "anchor", 5)
	require.NoError(t, err)
	ids := make([]string, 0, len(board.Related))
	for _, item := range board.Related {
		ids = append(ids, item.KnowledgeID)
	}
	assert.Contains(t, ids, "k1")
	assert.Contains(t, ids, "k2")
}

// TestRecommendationRankPutsRelationsFirst covers ordering: a relation edge
// outranks a mere same-space document at the same freshness.
func TestRecommendationRankPutsRelationsFirst(t *testing.T) {
	svc, repo := newRecommendationFixture()
	same := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	repo.related = []types.RecommendationItem{
		{KnowledgeID: "k2", KnowledgeBaseID: "kb1", Reason: types.RecommendReasonSameSpace, UpdatedAt: same},
		{KnowledgeID: "k1", KnowledgeBaseID: "kb1", Reason: types.RecommendReasonRelated, UpdatedAt: same},
	}

	board, err := svc.Board(recommendationCaller(), 7, "anchor", 5)
	require.NoError(t, err)
	require.Len(t, board.Related, 2)
	assert.Equal(t, "k1", board.Related[0].KnowledgeID)
}

// TestRecommendationAnonymousGetsNothing keeps the boards from being a public
// discovery channel.
func TestRecommendationAnonymousGetsNothing(t *testing.T) {
	svc, repo := newRecommendationFixture()
	repo.related = []types.RecommendationItem{
		{KnowledgeID: "k1", KnowledgeBaseID: "kb1", UpdatedAt: time.Now()},
	}
	board, err := svc.Board(context.Background(), 7, "anchor", 5)
	require.NoError(t, err)
	assert.Empty(t, board.Related)
}

// TestRecordViewDedupeIsDelegated covers the call path.
func TestRecordViewIsRecorded(t *testing.T) {
	svc, repo := newRecommendationFixture()
	require.NoError(t, svc.RecordView(recommendationCaller(), 7, "u-1", "k1"))
	assert.Equal(t, []string{"k1"}, repo.views)
}

// TestHistoryBlanksInvisibleTitles covers §2.10 in the history list.
func TestHistoryBlanksInvisibleTitles(t *testing.T) {
	svc, repo := newRecommendationFixture()
	now := time.Now()
	repo.history = []*types.KnowledgeViewEvent{
		{KnowledgeID: "k1", KnowledgeBaseID: "kb1", CreatedAt: now},
		{KnowledgeID: "gone", KnowledgeBaseID: "kb1", CreatedAt: now.Add(-time.Hour)},
	}

	items, err := svc.History(recommendationCaller(), 7, "u-1", 50)
	require.NoError(t, err)
	require.Len(t, items, 2)
	assert.True(t, items[0].Visible)
	assert.Equal(t, "E45 换型 SOP", items[0].Title)
	assert.False(t, items[1].Visible)
	assert.Empty(t, items[1].Title, "an unavailable document must not leak its title")
}

// TestHistoryDedupesRepeatedViews keeps one row per document.
func TestHistoryDedupesRepeatedViews(t *testing.T) {
	svc, repo := newRecommendationFixture()
	now := time.Now()
	repo.history = []*types.KnowledgeViewEvent{
		{KnowledgeID: "k1", KnowledgeBaseID: "kb1", CreatedAt: now},
		{KnowledgeID: "k1", KnowledgeBaseID: "kb1", CreatedAt: now.Add(-time.Hour)},
	}
	items, err := svc.History(recommendationCaller(), 7, "u-1", 50)
	require.NoError(t, err)
	assert.Len(t, items, 1)
}

// TestBoardAttachesExplanations is the §15.3 requirement end to end: every
// returned item carries a "why" panel whose primary signal matches its reason.
func TestBoardAttachesExplanations(t *testing.T) {
	svc, repo := newRecommendationFixture()
	repo.related = []types.RecommendationItem{{
		KnowledgeID: "k1", KnowledgeBaseID: "kb1", Title: "E45 换型 SOP",
		Reason:    types.RecommendReasonRelated,
		UpdatedAt: time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC),
	}}
	repo.hot = []types.RecommendationItem{{
		KnowledgeID: "k2", KnowledgeBaseID: "kb1", Title: "锁具规程",
		Reason: types.RecommendReasonHot, Score: 9,
		UpdatedAt: time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC),
	}, {
		// The denied document must not reach the board at all.
		KnowledgeID: "k3", KnowledgeBaseID: "kb1", Title: "机密工艺卡",
		Reason: types.RecommendReasonHot, Score: 100,
	}}

	ctx := types.WithCaller(context.Background(), types.Caller{
		TenantID: 7, UserID: "reader", Role: types.TenantRoleViewer,
	})
	board, err := svc.Board(ctx, 7, "anchor", 5)
	require.NoError(t, err)

	require.Len(t, board.Related, 1)
	require.NotNil(t, board.Related[0].Explanation,
		"a related item explains itself")
	assert.True(t, board.Related[0].Explanation.Signals[0].Primary)
	assert.NotEmpty(t, board.Related[0].Explanation.Summary)

	require.Len(t, board.Hot, 1, "the denied document is filtered out")
	hot := board.Hot[0]
	require.NotNil(t, hot.Explanation)
	assert.Equal(t, types.RecommendationSignalHot, hot.Explanation.Signals[0].Type)
	assert.Contains(t, hot.Explanation.Signals[0].Evidence, "9",
		"the view count that drove the rank is reported")
	assert.NotEmpty(t, hot.Explanation.Summary)
}

// TestHomeAttachesExplanations covers the personal board path.
func TestHomeAttachesExplanations(t *testing.T) {
	svc, repo := newRecommendationFixture()
	repo.latest = []types.RecommendationItem{{
		KnowledgeID: "k1", KnowledgeBaseID: "kb1", Title: "E45 换型 SOP",
		Reason:    types.RecommendReasonLatest,
		UpdatedAt: time.Date(2026, 11, 4, 8, 0, 0, 0, time.UTC),
	}}

	ctx := types.WithCaller(context.Background(), types.Caller{
		TenantID: 7, UserID: "reader", Role: types.TenantRoleViewer,
	})
	board, err := svc.Home(ctx, 7, "reader", 5)
	require.NoError(t, err)
	require.Len(t, board.Latest, 1)
	require.NotNil(t, board.Latest[0].Explanation)
	assert.Equal(t, types.RecommendationSignalLatest,
		board.Latest[0].Explanation.Signals[0].Type)
}
