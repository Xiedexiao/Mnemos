package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// stubSubscriptionRepo is an in-memory follow store.
type stubSubscriptionRepo struct {
	interfaces.SubscriptionRepository

	subs        []*types.Subscription
	spaceRows   []types.DigestItem
	tagRows     []types.DigestItem
	authorRows  []types.DigestItem
	knowledge   []types.DigestItem
	folders     map[string]string
	digestedIDs []string
}

func (s *stubSubscriptionRepo) Upsert(
	_ context.Context, subscription *types.Subscription,
) (bool, error) {
	for _, existing := range s.subs {
		if existing.UserID == subscription.UserID &&
			existing.SubjectType == subscription.SubjectType &&
			existing.SubjectID == subscription.SubjectID {
			existing.Enabled = true
			subscription.ID = existing.ID
			return false, nil
		}
	}
	subscription.ID = "sub-" + itoaSmall(len(s.subs)+1)
	s.subs = append(s.subs, subscription)
	return true, nil
}

func (s *stubSubscriptionRepo) List(
	_ context.Context, _ uint64, userID string, enabledOnly bool,
) ([]*types.Subscription, error) {
	out := make([]*types.Subscription, 0, len(s.subs))
	for _, sub := range s.subs {
		if sub.UserID != userID {
			continue
		}
		if enabledOnly && !sub.Enabled {
			continue
		}
		out = append(out, sub)
	}
	return out, nil
}

func (s *stubSubscriptionRepo) Get(
	_ context.Context, _ uint64, userID, subscriptionID string,
) (*types.Subscription, error) {
	for _, sub := range s.subs {
		if sub.ID == subscriptionID && sub.UserID == userID {
			return sub, nil
		}
	}
	return nil, errors.New("not found")
}

func (s *stubSubscriptionRepo) SetEnabled(
	_ context.Context, _ uint64, userID, subscriptionID string, enabled bool,
) error {
	for _, sub := range s.subs {
		if sub.ID == subscriptionID && sub.UserID == userID {
			sub.Enabled = enabled
			return nil
		}
	}
	return errors.New("not found")
}

func (s *stubSubscriptionRepo) Delete(_ context.Context, _ uint64, userID, id string) error {
	kept := s.subs[:0]
	found := false
	for _, sub := range s.subs {
		if sub.ID == id && sub.UserID == userID {
			found = true
			continue
		}
		kept = append(kept, sub)
	}
	s.subs = kept
	if !found {
		return repository.ErrSubscriptionNotFound
	}
	return nil
}

func (s *stubSubscriptionRepo) ListDigestSources(
	context.Context, uint64,
) ([]*types.Subscription, error) {
	return s.subs, nil
}

func (s *stubSubscriptionRepo) ChangesSince(
	context.Context, uint64, string, time.Time, int,
) ([]types.DigestItem, error) {
	return s.spaceRows, nil
}

func (s *stubSubscriptionRepo) TagChangesSince(
	context.Context, uint64, string, time.Time, int,
) ([]types.DigestItem, error) {
	return s.tagRows, nil
}

func (s *stubSubscriptionRepo) AuthorChangesSince(
	context.Context, uint64, string, time.Time, int,
) ([]types.DigestItem, error) {
	return s.authorRows, nil
}

func (s *stubSubscriptionRepo) KnowledgeChangesSince(
	context.Context, uint64, string, time.Time,
) ([]types.DigestItem, error) {
	return s.knowledge, nil
}

func (s *stubSubscriptionRepo) FolderPaths(
	_ context.Context, _ uint64, _ []string,
) (map[string]string, error) {
	return s.folders, nil
}

func (s *stubSubscriptionRepo) MarkDigested(
	_ context.Context, _ uint64, ids []string, _ time.Time,
) error {
	s.digestedIDs = append(s.digestedIDs, ids...)
	return nil
}

// stubSubscriptionACL denies a fixed set.
type stubSubscriptionACL struct {
	interfaces.KnowledgeACLRepository
	denied []string
	err    error
}

func (s *stubSubscriptionACL) DeniedKnowledgeIDsForSubjects(
	context.Context, []string, []types.ACLSubject, types.PermissionAction, time.Time,
) ([]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.denied, nil
}

func newSubscriptionFixture() (*subscriptionService, *stubSubscriptionRepo, *stubSubscriptionACL) {
	repo := &stubSubscriptionRepo{folders: map[string]string{}}
	acl := &stubSubscriptionACL{}
	svc := &subscriptionService{
		repo:     repo,
		acl:      acl,
		subjects: newACLSubjectResolver(nil),
		now:      func() time.Time { return time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC) },
	}
	return svc, repo, acl
}

// TestSubscribeNormalizesSubjects covers the per-type shape rules.
func TestSubscribeNormalizesSubjects(t *testing.T) {
	svc, repo, _ := newSubscriptionFixture()
	ctx := recommendationCaller()

	sub, err := svc.Subscribe(ctx, 7, "u-1", types.SubscriptionInput{
		SubjectType: "space", SubjectID: "kb-1",
	})
	require.NoError(t, err)
	assert.Equal(t, "kb-1", sub.SubjectID)
	assert.Equal(t, "kb-1", sub.SpaceID)

	_, err = svc.Subscribe(ctx, 7, "u-1", types.SubscriptionInput{
		SubjectType: "folder", SubjectID: "sop/e45",
	})
	require.Error(t, err, "a folder subscription needs a space")

	folderSub, err := svc.Subscribe(ctx, 7, "u-1", types.SubscriptionInput{
		SubjectType: "folder", SubjectID: "sop/e45", SpaceID: "kb-1",
	})
	require.NoError(t, err)
	assert.Equal(t, "kb-1\x00sop/e45", folderSub.SubjectID)

	_, err = svc.Subscribe(ctx, 7, "u-1", types.SubscriptionInput{
		SubjectType: "planet", SubjectID: "mars",
	})
	require.Error(t, err)

	// Subscribing twice re-enables rather than duplicating.
	require.NoError(t, svc.SetEnabled(ctx, 7, "u-1", sub.ID, false))
	again, err := svc.Subscribe(ctx, 7, "u-1", types.SubscriptionInput{
		SubjectType: "space", SubjectID: "kb-1",
	})
	require.NoError(t, err)
	assert.True(t, again.Enabled, "re-subscribing resumes the original row")
	assert.Len(t, repo.subs, 2, "no duplicate subscription rows")
}

// TestDigestFiltersDeniedAndCountsHidden is the security property: a follow
// does not grant access, and the digest reports only how many were withheld.
func TestDigestFiltersDeniedAndCountsHidden(t *testing.T) {
	svc, repo, acl := newSubscriptionFixture()
	ctx := recommendationCaller()
	_, err := svc.Subscribe(ctx, 7, "u-1", types.SubscriptionInput{
		SubjectType: "space", SubjectID: "kb-1", Label: "工艺空间",
	})
	require.NoError(t, err)

	now := time.Now()
	repo.spaceRows = []types.DigestItem{
		{KnowledgeID: "k1", KnowledgeBaseID: "kb-1", Title: "E45 换型 SOP", ChangedAt: now},
		{KnowledgeID: "k3", KnowledgeBaseID: "kb-1", Title: "机密工艺卡", ChangedAt: now},
	}
	acl.denied = []string{"k3"}

	digest, err := svc.Digest(ctx, 7, "u-1", now.Add(-24*time.Hour), 100)
	require.NoError(t, err)
	require.Len(t, digest.Items, 1)
	assert.Equal(t, "k1", digest.Items[0].KnowledgeID)
	assert.Equal(t, "工艺空间", digest.Items[0].SubscriptionLabel)
	assert.Equal(t, 1, digest.HiddenCount)
}

// TestDigestFailsClosedOnACLError keeps an ACL outage from exposing follows.
func TestDigestFailsClosedOnACLError(t *testing.T) {
	svc, repo, acl := newSubscriptionFixture()
	ctx := recommendationCaller()
	_, err := svc.Subscribe(ctx, 7, "u-1", types.SubscriptionInput{
		SubjectType: "space", SubjectID: "kb-1",
	})
	require.NoError(t, err)
	repo.spaceRows = []types.DigestItem{
		{KnowledgeID: "k1", KnowledgeBaseID: "kb-1", Title: "x", ChangedAt: time.Now()},
	}
	acl.err = errors.New("acl down")

	digest, err := svc.Digest(ctx, 7, "u-1", time.Now().Add(-time.Hour), 100)
	require.NoError(t, err)
	assert.Empty(t, digest.Items)
	assert.Equal(t, 1, digest.HiddenCount)
}

// TestDigestDedupesAcrossSubscriptions keeps one row per document even when
// several follows match it.
func TestDigestDedupesAcrossSubscriptions(t *testing.T) {
	svc, repo, _ := newSubscriptionFixture()
	ctx := recommendationCaller()
	_, err := svc.Subscribe(ctx, 7, "u-1", types.SubscriptionInput{
		SubjectType: "space", SubjectID: "kb-1",
	})
	require.NoError(t, err)
	_, err = svc.Subscribe(ctx, 7, "u-1", types.SubscriptionInput{
		SubjectType: "tag", SubjectID: "工艺",
	})
	require.NoError(t, err)
	now := time.Now()
	repo.spaceRows = []types.DigestItem{
		{KnowledgeID: "k1", KnowledgeBaseID: "kb-1", Title: "SOP", ChangedAt: now.Add(-time.Hour)},
	}
	repo.tagRows = []types.DigestItem{
		{KnowledgeID: "k1", KnowledgeBaseID: "kb-1", Title: "SOP", ChangedAt: now},
	}

	digest, err := svc.Digest(ctx, 7, "u-1", now.Add(-24*time.Hour), 100)
	require.NoError(t, err)
	require.Len(t, digest.Items, 1)
	assert.Equal(t, "tag", digest.Items[0].SubjectType, "the newest change wins")
}

// TestDigestFolderScopesToSubtree covers the folder subject: only documents
// inside the followed path (or below it) are included.
func TestDigestFolderScopesToSubtree(t *testing.T) {
	svc, repo, _ := newSubscriptionFixture()
	ctx := recommendationCaller()
	_, err := svc.Subscribe(ctx, 7, "u-1", types.SubscriptionInput{
		SubjectType: "folder", SubjectID: "sop", SpaceID: "kb-1",
	})
	require.NoError(t, err)
	now := time.Now()
	repo.spaceRows = []types.DigestItem{
		{KnowledgeID: "inside", KnowledgeBaseID: "kb-1", Title: "inside", ChangedAt: now},
		{KnowledgeID: "nested", KnowledgeBaseID: "kb-1", Title: "nested", ChangedAt: now},
		{KnowledgeID: "outside", KnowledgeBaseID: "kb-1", Title: "outside", ChangedAt: now},
	}
	repo.folders = map[string]string{
		"inside": "sop", "nested": "sop/e45", "outside": "manual",
	}

	digest, err := svc.Digest(ctx, 7, "u-1", now.Add(-time.Hour), 100)
	require.NoError(t, err)
	ids := make([]string, 0, len(digest.Items))
	for _, item := range digest.Items {
		ids = append(ids, item.KnowledgeID)
	}
	assert.Contains(t, ids, "inside")
	assert.Contains(t, ids, "nested")
	assert.NotContains(t, ids, "outside")
}

// TestDigestIsPerUser keeps one user's follows out of another's digest.
func TestDigestIsPerUser(t *testing.T) {
	svc, repo, _ := newSubscriptionFixture()
	ctx := recommendationCaller()
	_, err := svc.Subscribe(ctx, 7, "u-1", types.SubscriptionInput{
		SubjectType: "space", SubjectID: "kb-1",
	})
	require.NoError(t, err)
	repo.spaceRows = []types.DigestItem{
		{KnowledgeID: "k1", KnowledgeBaseID: "kb-1", Title: "x", ChangedAt: time.Now()},
	}

	digest, err := svc.Digest(ctx, 7, "u-2", time.Now().Add(-time.Hour), 100)
	require.NoError(t, err)
	assert.Empty(t, digest.Items, "another user's follows are not mine")
}

// TestDigestEmptyWithoutFollows covers the empty state.
func TestDigestEmptyWithoutFollows(t *testing.T) {
	svc, _, _ := newSubscriptionFixture()
	digest, err := svc.Digest(recommendationCaller(), 7, "u-1", time.Time{}, 100)
	require.NoError(t, err)
	assert.Empty(t, digest.Items)
	assert.Zero(t, digest.HiddenCount)
	assert.False(t, digest.Truncated)
}

// TestDigestTruncates covers the limit.
func TestDigestTruncates(t *testing.T) {
	svc, repo, _ := newSubscriptionFixture()
	ctx := recommendationCaller()
	_, err := svc.Subscribe(ctx, 7, "u-1", types.SubscriptionInput{
		SubjectType: "space", SubjectID: "kb-1",
	})
	require.NoError(t, err)
	now := time.Now()
	for i := 0; i < 5; i++ {
		repo.spaceRows = append(repo.spaceRows, types.DigestItem{
			KnowledgeID: "k" + itoaSmall(i), KnowledgeBaseID: "kb-1",
			Title: "doc", ChangedAt: now.Add(-time.Duration(i) * time.Minute),
		})
	}
	digest, err := svc.Digest(ctx, 7, "u-1", now.Add(-time.Hour), 2)
	require.NoError(t, err)
	assert.Len(t, digest.Items, 2)
	assert.True(t, digest.Truncated)
}

// TestUnsubscribeRejectsAnotherUsersSubscription covers ownership.
func TestUnsubscribeRejectsAnotherUsersSubscription(t *testing.T) {
	svc, _, _ := newSubscriptionFixture()
	ctx := recommendationCaller()
	sub, err := svc.Subscribe(ctx, 7, "u-1", types.SubscriptionInput{
		SubjectType: "space", SubjectID: "kb-1",
	})
	require.NoError(t, err)
	require.Error(t, svc.Unsubscribe(ctx, 7, "u-2", sub.ID))
	require.NoError(t, svc.Unsubscribe(ctx, 7, "u-1", sub.ID))
}
