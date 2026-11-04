package service

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// stubSavedViewRepo is an in-memory SavedViewRepository.
//
// 用内存实现而不是 mock 每个调用：服务层的行为（配额、默认互斥、名称冲突）
// 本质上是对存储状态的约束，逐调用断言会把测试写死成实现的样子。
type stubSavedViewRepo struct {
	interfaces.SavedViewRepository

	views     map[string]*types.SavedKnowledgeView
	seq       int
	createErr error
}

func newStubSavedViewRepo() *stubSavedViewRepo {
	return &stubSavedViewRepo{views: map[string]*types.SavedKnowledgeView{}}
}

func (s *stubSavedViewRepo) owns(view *types.SavedKnowledgeView, tenantID uint64, userID string) bool {
	return view != nil && view.TenantID == tenantID && view.UserID == userID
}

func (s *stubSavedViewRepo) List(
	_ context.Context, tenantID uint64, userID string,
) ([]*types.SavedKnowledgeView, error) {
	out := make([]*types.SavedKnowledgeView, 0, len(s.views))
	for _, view := range s.views {
		if s.owns(view, tenantID, userID) {
			copyOf := *view
			out = append(out, &copyOf)
		}
	}
	// default first then name, mirroring the repository ordering.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0; j-- {
			swap := false
			if out[j].IsDefault != out[j-1].IsDefault {
				swap = out[j].IsDefault
			} else {
				swap = out[j].Name < out[j-1].Name
			}
			if !swap {
				break
			}
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}

func (s *stubSavedViewRepo) Get(
	_ context.Context, tenantID uint64, userID, viewID string,
) (*types.SavedKnowledgeView, error) {
	view, ok := s.views[viewID]
	if !ok || !s.owns(view, tenantID, userID) {
		return nil, gorm.ErrRecordNotFound
	}
	copyOf := *view
	return &copyOf, nil
}

func (s *stubSavedViewRepo) Create(
	_ context.Context, view *types.SavedKnowledgeView,
) error {
	if s.createErr != nil {
		return s.createErr
	}
	for _, existing := range s.views {
		if existing.TenantID == view.TenantID && existing.UserID == view.UserID &&
			existing.Name == view.Name {
			return repository.ErrSavedViewNameTaken
		}
	}
	s.seq++
	if view.ID == "" {
		view.ID = "view-" + string(rune('a'+s.seq%26)) + view.Name
	}
	copyOf := *view
	s.views[view.ID] = &copyOf
	return nil
}

func (s *stubSavedViewRepo) Update(
	_ context.Context, view *types.SavedKnowledgeView,
) error {
	existing, ok := s.views[view.ID]
	if !ok || !s.owns(existing, view.TenantID, view.UserID) {
		return gorm.ErrRecordNotFound
	}
	for id, candidate := range s.views {
		if id == view.ID {
			continue
		}
		if candidate.TenantID == view.TenantID && candidate.UserID == view.UserID &&
			candidate.Name == view.Name {
			return repository.ErrSavedViewNameTaken
		}
	}
	existing.Name = view.Name
	existing.KnowledgeBaseID = view.KnowledgeBaseID
	existing.Filter = view.Filter
	return nil
}

func (s *stubSavedViewRepo) Delete(
	_ context.Context, tenantID uint64, userID, viewID string,
) error {
	view, ok := s.views[viewID]
	if !ok || !s.owns(view, tenantID, userID) {
		return gorm.ErrRecordNotFound
	}
	delete(s.views, viewID)
	return nil
}

func (s *stubSavedViewRepo) Count(
	_ context.Context, tenantID uint64, userID string,
) (int64, error) {
	var total int64
	for _, view := range s.views {
		if s.owns(view, tenantID, userID) {
			total++
		}
	}
	return total, nil
}

func (s *stubSavedViewRepo) ClearDefault(
	_ context.Context, tenantID uint64, userID, knowledgeBaseID string,
) error {
	for _, view := range s.views {
		if s.owns(view, tenantID, userID) && view.KnowledgeBaseID == knowledgeBaseID {
			view.IsDefault = false
		}
	}
	return nil
}

func (s *stubSavedViewRepo) SetDefault(
	_ context.Context, tenantID uint64, userID, viewID string,
) error {
	view, ok := s.views[viewID]
	if !ok || !s.owns(view, tenantID, userID) {
		return gorm.ErrRecordNotFound
	}
	view.IsDefault = true
	return nil
}

// defaultsIn counts the defaults inside one (user, knowledge base).
func (s *stubSavedViewRepo) defaultsIn(tenantID uint64, userID, kbID string) int {
	count := 0
	for _, view := range s.views {
		if s.owns(view, tenantID, userID) && view.KnowledgeBaseID == kbID && view.IsDefault {
			count++
		}
	}
	return count
}

func newSavedViewServiceForTest(repo interfaces.SavedViewRepository) interfaces.SavedViewService {
	svc := NewSavedViewService(repo).(*savedViewService)
	svc.now = func() time.Time {
		return time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	}
	return svc
}

// savedViewInput builds a valid payload.
func savedViewInput(name string) types.SavedViewInput {
	return types.SavedViewInput{
		Name:            name,
		KnowledgeBaseID: "kb-1",
		Filter: types.SavedViewFilter{
			Keyword:           name,
			LifecycleStatuses: []string{"valid"},
		},
	}
}

// TestCreateSavedViewStoresNormalisedFilter covers the happy path.
func TestCreateSavedViewStoresNormalisedFilter(t *testing.T) {
	repo := newStubSavedViewRepo()
	svc := newSavedViewServiceForTest(repo)

	input := types.SavedViewInput{
		Name:            "  我的待复审  ",
		KnowledgeBaseID: " kb-1 ",
		Filter: types.SavedViewFilter{
			Keyword:           "  E45  ",
			TagIDs:            []string{" tag-1 ", "tag-1"},
			LifecycleStatuses: []string{"valid", " valid "},
		},
	}
	view, err := svc.Create(context.Background(), 7, "ana", input)
	require.NoError(t, err)
	assert.Equal(t, "我的待复审", view.Name)
	assert.Equal(t, "kb-1", view.KnowledgeBaseID)

	filter := view.SavedViewFilterOf()
	assert.Equal(t, "E45", filter.Keyword)
	assert.Equal(t, []string{"tag-1"}, filter.TagIDs)
	assert.Equal(t, []string{"valid"}, filter.LifecycleStatuses)
}

// TestCreateSavedViewRejectsInvalidFilter is the "fail loudly" rule.
func TestCreateSavedViewRejectsInvalidFilter(t *testing.T) {
	svc := newSavedViewServiceForTest(newStubSavedViewRepo())
	input := savedViewInput("坏视图")
	input.Filter.LifecycleStatuses = []string{"not_a_status"}

	_, err := svc.Create(context.Background(), 7, "ana", input)
	require.Error(t, err, "an unknown enum must be rejected at save time")
}

// TestCreateSavedViewRejectsDuplicateName surfaces the conflict.
func TestCreateSavedViewRejectsDuplicateName(t *testing.T) {
	svc := newSavedViewServiceForTest(newStubSavedViewRepo())
	_, err := svc.Create(context.Background(), 7, "ana", savedViewInput("重名"))
	require.NoError(t, err)

	_, err = svc.Create(context.Background(), 7, "ana", savedViewInput("重名"))
	require.Error(t, err, "a duplicate name is a 409, not a second row")

	// Another user may reuse the name.
	_, err = svc.Create(context.Background(), 7, "bo", savedViewInput("重名"))
	require.NoError(t, err)
}

// TestCreateSavedViewEnforcesQuota bounds one user's list.
func TestCreateSavedViewEnforcesQuota(t *testing.T) {
	repo := newStubSavedViewRepo()
	svc := newSavedViewServiceForTest(repo)
	for i := 0; i < types.MaxSavedViewsPerUser; i++ {
		_, err := svc.Create(context.Background(), 7, "ana",
			savedViewInput("视图 "+string(rune('A'+i))))
		require.NoError(t, err)
	}
	_, err := svc.Create(context.Background(), 7, "ana", savedViewInput("再一个"))
	require.Error(t, err, "the cap holds")

	// A different user is unaffected.
	_, err = svc.Create(context.Background(), 7, "bo", savedViewInput("bo 的视图"))
	require.NoError(t, err)
}

// TestSetDefaultKeepsOnePerSpace is the "exactly one default" invariant.
func TestSetDefaultKeepsOnePerSpace(t *testing.T) {
	repo := newStubSavedViewRepo()
	svc := newSavedViewServiceForTest(repo)

	first, err := svc.Create(context.Background(), 7, "ana", savedViewInput("视图 A"))
	require.NoError(t, err)
	second, err := svc.Create(context.Background(), 7, "ana", savedViewInput("视图 B"))
	require.NoError(t, err)
	otherSpace := savedViewInput("别的空间")
	otherSpace.KnowledgeBaseID = "kb-2"
	third, err := svc.Create(context.Background(), 7, "ana", otherSpace)
	require.NoError(t, err)

	_, err = svc.SetDefault(context.Background(), 7, "ana", first.ID)
	require.NoError(t, err)
	_, err = svc.SetDefault(context.Background(), 7, "ana", second.ID)
	require.NoError(t, err)

	assert.Equal(t, 1, repo.defaultsIn(7, "ana", "kb-1"),
		"promoting the second view demotes the first")

	// A different space keeps its own default.
	_, err = svc.SetDefault(context.Background(), 7, "ana", third.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, repo.defaultsIn(7, "ana", "kb-2"))
	assert.Equal(t, 1, repo.defaultsIn(7, "ana", "kb-1"))
}

// TestCreateWithDefaultFlagDemotesPrevious covers the create-time promotion.
func TestCreateWithDefaultFlagDemotesPrevious(t *testing.T) {
	repo := newStubSavedViewRepo()
	svc := newSavedViewServiceForTest(repo)

	promoted := savedViewInput("第一个默认")
	promoted.IsDefault = true
	_, err := svc.Create(context.Background(), 7, "ana", promoted)
	require.NoError(t, err)
	assert.Equal(t, 1, repo.defaultsIn(7, "ana", "kb-1"))

	next := savedViewInput("第二个默认")
	next.IsDefault = true
	_, err = svc.Create(context.Background(), 7, "ana", next)
	require.NoError(t, err)
	assert.Equal(t, 1, repo.defaultsIn(7, "ana", "kb-1"),
		"creating a new default must not leave two")
}

// TestListReportsDefaultViewID is what the UI auto-applies.
func TestListReportsDefaultViewID(t *testing.T) {
	repo := newStubSavedViewRepo()
	svc := newSavedViewServiceForTest(repo)

	view, err := svc.Create(context.Background(), 7, "ana", savedViewInput("默认视图"))
	require.NoError(t, err)
	_, err = svc.SetDefault(context.Background(), 7, "ana", view.ID)
	require.NoError(t, err)

	list, err := svc.List(context.Background(), 7, "ana")
	require.NoError(t, err)
	assert.Equal(t, view.ID, list.DefaultViewID)
	require.Len(t, list.Views, 1)
}

// TestListWithoutDefaultActsAsEmpty covers the "nothing promoted yet" state.
func TestListWithoutDefaultActsAsEmpty(t *testing.T) {
	svc := newSavedViewServiceForTest(newStubSavedViewRepo())
	_, err := svc.Create(context.Background(), 7, "ana", savedViewInput("普通视图"))
	require.NoError(t, err)

	list, err := svc.List(context.Background(), 7, "ana")
	require.NoError(t, err)
	assert.Empty(t, list.DefaultViewID)
	require.Len(t, list.Views, 1)
}

// TestGetHidesOtherUsersView is the 404 rule.
func TestGetHidesOtherUsersView(t *testing.T) {
	repo := newStubSavedViewRepo()
	svc := newSavedViewServiceForTest(repo)
	boView, err := svc.Create(context.Background(), 7, "bo", savedViewInput("bo 的视图"))
	require.NoError(t, err)

	_, err = svc.Get(context.Background(), 7, "ana", boView.ID)
	require.Error(t, err)
}

// TestUpdateRenamesAndCannotStealAnotherRow covers both update risks.
func TestUpdateRenamesAndCannotStealAnotherRow(t *testing.T) {
	repo := newStubSavedViewRepo()
	svc := newSavedViewServiceForTest(repo)
	mine, err := svc.Create(context.Background(), 7, "ana", savedViewInput("我的"))
	require.NoError(t, err)
	theirs, err := svc.Create(context.Background(), 7, "bo", savedViewInput("博的"))
	require.NoError(t, err)

	renamed, err := svc.Update(context.Background(), 7, "ana", mine.ID, savedViewInput("改名"))
	require.NoError(t, err)
	assert.Equal(t, "改名", renamed.Name)

	// Renaming someone else's row is a not-found.
	_, err = svc.Update(context.Background(), 7, "ana", theirs.ID, savedViewInput("偷改"))
	require.Error(t, err)
	stored, err := svc.Get(context.Background(), 7, "bo", theirs.ID)
	require.NoError(t, err)
	assert.Equal(t, "博的", stored.Name)
}

// TestUpdateToDefaultPromotes covers the is_default flag on update.
func TestUpdateToDefaultPromotes(t *testing.T) {
	repo := newStubSavedViewRepo()
	svc := newSavedViewServiceForTest(repo)
	plain, err := svc.Create(context.Background(), 7, "ana", savedViewInput("普通"))
	require.NoError(t, err)

	input := savedViewInput("普通")
	input.IsDefault = true
	updated, err := svc.Update(context.Background(), 7, "ana", plain.ID, input)
	require.NoError(t, err)
	assert.True(t, updated.IsDefault)
	assert.Equal(t, 1, repo.defaultsIn(7, "ana", "kb-1"))
}

// TestDeleteIsScoped covers the delete rule.
func TestDeleteIsScoped(t *testing.T) {
	repo := newStubSavedViewRepo()
	svc := newSavedViewServiceForTest(repo)
	mine, err := svc.Create(context.Background(), 7, "ana", savedViewInput("我的"))
	require.NoError(t, err)
	theirs, err := svc.Create(context.Background(), 7, "bo", savedViewInput("博的"))
	require.NoError(t, err)

	require.Error(t, svc.Delete(context.Background(), 7, "ana", theirs.ID))
	require.NoError(t, svc.Delete(context.Background(), 7, "ana", mine.ID))
}

// TestSavedViewRequiresIdentity refuses anonymous access everywhere.
func TestSavedViewRequiresIdentity(t *testing.T) {
	svc := newSavedViewServiceForTest(newStubSavedViewRepo())
	ctx := context.Background()

	_, err := svc.List(ctx, 7, "")
	require.Error(t, err)
	_, err = svc.Create(ctx, 7, "", savedViewInput("视图"))
	require.Error(t, err)
	require.Error(t, svc.Delete(ctx, 7, "", "view"))
	_, err = svc.Get(ctx, 7, "", "view")
	require.Error(t, err)

	// Missing tenant is refused too.
	_, err = svc.List(ctx, 0, "ana")
	require.Error(t, err)
}

// TestSavedViewRequiresID keeps an empty path parameter from matching a row.
func TestSavedViewRequiresID(t *testing.T) {
	svc := newSavedViewServiceForTest(newStubSavedViewRepo())
	require.Error(t, svc.Delete(context.Background(), 7, "ana", ""))
	_, err := svc.Get(context.Background(), 7, "ana", "   ")
	require.Error(t, err)
}

// TestUpdateConvertsMalformedTimestampsEndToEnd proves an invalid bound is
// rejected at save time even though ToListFilter tolerates one.
func TestUpdateConvertsMalformedTimestampsEndToEnd(t *testing.T) {
	svc := newSavedViewServiceForTest(newStubSavedViewRepo())
	input := savedViewInput("坏时间")
	input.Filter.UpdatedFrom = "2026-01-01"
	_, err := svc.Create(context.Background(), 7, "ana", input)
	require.Error(t, err)
}
