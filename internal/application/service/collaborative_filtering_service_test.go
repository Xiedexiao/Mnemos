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

// stubCFRepo serves interaction signals.
type stubCFRepo struct {
	interfaces.CollaborativeFilteringRepository

	interactions []types.CFInteraction
	history      []string
	historyErr   error
	interErr     error
}

func (s *stubCFRepo) Interactions(
	_ context.Context, _ uint64, _ time.Time, _ int, _ types.CFInteractionWeights,
) ([]types.CFInteraction, error) {
	return s.interactions, s.interErr
}

func (s *stubCFRepo) UserHistory(
	_ context.Context, _ uint64, _ string, _ time.Time, _ int,
) ([]string, error) {
	return s.history, s.historyErr
}

// stubCFACL denies a fixed set.
type stubCFACL struct {
	interfaces.KnowledgeACLRepository

	denied []string
	err    error
	// gotKBs records the knowledge bases the ACL was asked about.
	gotKBs []string
}

func (s *stubCFACL) DeniedKnowledgeIDsForSubjects(
	_ context.Context, kbIDs []string, _ []types.ACLSubject,
	_ types.PermissionAction, _ time.Time,
) ([]string, error) {
	s.gotKBs = kbIDs
	if s.err != nil {
		return nil, s.err
	}
	return s.denied, nil
}

func newCFServiceForTest(
	repo interfaces.CollaborativeFilteringRepository, acl interfaces.KnowledgeACLRepository,
) *collaborativeFilteringService {
	svc := NewCollaborativeFilteringService(repo, acl, nil).(*collaborativeFilteringService)
	svc.now = func() time.Time { return time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC) }
	return svc
}

// cfCallerCtx carries a viewer identity.
func cfCallerCtx() context.Context {
	return types.WithCaller(context.Background(), types.Caller{
		TenantID: 7, UserID: "reader", Role: types.TenantRoleViewer,
	})
}

// cfFixture builds co-read items "a" and "b" in kb-1.
func cfFixture() *stubCFRepo {
	return &stubCFRepo{
		interactions: []types.CFInteraction{
			{UserID: "u1", ItemID: "a", KnowledgeBaseID: "kb-1", Weight: 1},
			{UserID: "u1", ItemID: "b", KnowledgeBaseID: "kb-1", Weight: 1},
			{UserID: "u2", ItemID: "a", KnowledgeBaseID: "kb-1", Weight: 1},
			{UserID: "u2", ItemID: "b", KnowledgeBaseID: "kb-1", Weight: 1},
		},
		history: []string{"a"},
	}
}

// TestRecommendForUserSuggestsCoReadItems covers the happy path.
func TestRecommendForUserSuggestsCoReadItems(t *testing.T) {
	svc := newCFServiceForTest(cfFixture(), nil)
	recommendations, err := svc.RecommendForUser(cfCallerCtx(), 7, "reader", nil, 5)
	require.NoError(t, err)
	require.NotEmpty(t, recommendations)
	assert.Equal(t, "b", recommendations[0].ItemID)
	assert.Contains(t, recommendations[0].FromItemIDs, "a",
		"the recommendation says which history item produced it")
}

// TestRecommendForUserWithoutHistoryIsEmpty covers cold start.
func TestRecommendForUserWithoutHistoryIsEmpty(t *testing.T) {
	repo := cfFixture()
	repo.history = nil
	svc := newCFServiceForTest(repo, nil)

	recommendations, err := svc.RecommendForUser(cfCallerCtx(), 7, "reader", nil, 5)
	require.NoError(t, err)
	assert.Empty(t, recommendations)
}

// TestRecommendForUserSurvivesSignalFailure is the availability rule: CF is a
// supplement, so a signal outage must not fail the recommendation surface.
func TestRecommendForUserSurvivesSignalFailure(t *testing.T) {
	repo := cfFixture()
	repo.interErr = errors.New("interaction table locked")
	svc := newCFServiceForTest(repo, nil)

	recommendations, err := svc.RecommendForUser(cfCallerCtx(), 7, "reader", nil, 5)
	require.NoError(t, err)
	assert.Empty(t, recommendations)
}

// TestRecommendForUserDropsDeniedItems is the §2.10 rule.
func TestRecommendForUserDropsDeniedItems(t *testing.T) {
	acl := &stubCFACL{denied: []string{"b"}}
	svc := newCFServiceForTest(cfFixture(), acl)

	recommendations, err := svc.RecommendForUser(cfCallerCtx(), 7, "reader", nil, 5)
	require.NoError(t, err)
	assert.Empty(t, recommendations, "a denied item must not be recommended")
	assert.Equal(t, []string{"kb-1"}, acl.gotKBs,
		"the ACL is asked about the candidates' actual knowledge bases")
}

// TestRecommendForUserFailsClosedOnACLError is the security core.
func TestRecommendForUserFailsClosedOnACLError(t *testing.T) {
	acl := &stubCFACL{err: errors.New("acl backend down")}
	svc := newCFServiceForTest(cfFixture(), acl)

	recommendations, err := svc.RecommendForUser(cfCallerCtx(), 7, "reader", nil, 5)
	require.NoError(t, err, "the surface degrades rather than erroring")
	assert.Empty(t, recommendations, "an ACL failure withholds recommendations")
}

// TestRecommendForUserWithholdsCandidatesWithoutKnowledgeBase is fail-closed for
// rows whose kb is unknown: visibility cannot be decided, so nothing is shown.
func TestRecommendForUserWithholdsCandidatesWithoutKnowledgeBase(t *testing.T) {
	repo := &stubCFRepo{
		interactions: []types.CFInteraction{
			{UserID: "u1", ItemID: "a", Weight: 1},
			{UserID: "u1", ItemID: "b", Weight: 1},
		},
		history: []string{"a"},
	}
	acl := &stubCFACL{}
	svc := newCFServiceForTest(repo, acl)

	recommendations, err := svc.RecommendForUser(cfCallerCtx(), 7, "reader", nil, 5)
	require.NoError(t, err)
	assert.Empty(t, recommendations)
	assert.Empty(t, acl.gotKBs, "no ACL query is attempted without a scope")
}

// TestRecommendForUserRequiresIdentity keeps an anonymous caller from getting
// a behaviour-derived list.
func TestRecommendForUserRequiresIdentity(t *testing.T) {
	svc := newCFServiceForTest(cfFixture(), nil)
	recommendations, err := svc.RecommendForUser(cfCallerCtx(), 7, "  ", nil, 5)
	require.NoError(t, err)
	assert.Empty(t, recommendations)

	_, err = svc.RecommendForUser(context.Background(), 0, "reader", nil, 5)
	require.Error(t, err)
}

// TestSimilarItemsReturnsNeighbours covers the item-to-item path.
func TestSimilarItemsReturnsNeighbours(t *testing.T) {
	svc := newCFServiceForTest(cfFixture(), nil)
	neighbours, err := svc.SimilarItems(cfCallerCtx(), 7, "a", 5)
	require.NoError(t, err)
	require.NotEmpty(t, neighbours)
	assert.Equal(t, "b", neighbours[0].ItemID)
}

// TestSimilarItemsRequiresAnItem covers the payload check.
func TestSimilarItemsRequiresAnItem(t *testing.T) {
	svc := newCFServiceForTest(cfFixture(), nil)
	_, err := svc.SimilarItems(cfCallerCtx(), 7, "   ", 5)
	require.Error(t, err)
}

// TestSimilarItemsDropsDeniedNeighbours covers the ACL gate on the item path.
func TestSimilarItemsDropsDeniedNeighbours(t *testing.T) {
	acl := &stubCFACL{denied: []string{"b"}}
	svc := newCFServiceForTest(cfFixture(), acl)

	neighbours, err := svc.SimilarItems(cfCallerCtx(), 7, "a", 5)
	require.NoError(t, err)
	assert.Empty(t, neighbours)
}
