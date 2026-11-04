package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
)

// savedViewTestDDL is the minimal schema the view queries need.
const savedViewTestDDL = `
CREATE TABLE saved_knowledge_views (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    user_id VARCHAR(36) NOT NULL,
    name VARCHAR(120) NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL DEFAULT '',
    filter TEXT NOT NULL DEFAULT '{}',
    is_default BOOLEAN NOT NULL DEFAULT FALSE,
    created_at DATETIME,
    updated_at DATETIME
);
CREATE UNIQUE INDEX uq_saved_view_user_name
    ON saved_knowledge_views (tenant_id, user_id, name);
`

func setupSavedViewDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(
		sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(savedViewTestDDL).Error)
	return db
}

// newView builds one saved view for (tenant, user).
func newView(t *testing.T, tenantID uint64, userID, name, kbID string) *types.SavedKnowledgeView {
	t.Helper()
	view := &types.SavedKnowledgeView{
		TenantID: tenantID, UserID: userID, Name: name, KnowledgeBaseID: kbID,
	}
	require.NoError(t, view.SetFilter(types.SavedViewFilter{Keyword: name}))
	return view
}

// TestSavedViewListIsPerUser is the privacy boundary.
func TestSavedViewListIsPerUser(t *testing.T) {
	db := setupSavedViewDB(t)
	repo := NewSavedViewRepository(db)
	require.NoError(t, repo.Create(context.Background(), newView(t, 7, "ana", "我的待复审", "kb-1")))
	require.NoError(t, repo.Create(context.Background(), newView(t, 7, "bo", "bo 的视图", "kb-1")))
	require.NoError(t, repo.Create(context.Background(), newView(t, 8, "ana", "别的租户", "kb-1")))

	views, err := repo.List(context.Background(), 7, "ana")
	require.NoError(t, err)
	require.Len(t, views, 1)
	assert.Equal(t, "我的待复审", views[0].Name)
}

// TestSavedViewGetHidesOtherUsersViews returns a not-found rather than someone
// else's row: telling "exists but not yours" is itself a disclosure.
func TestSavedViewGetHidesOtherUsersViews(t *testing.T) {
	db := setupSavedViewDB(t)
	repo := NewSavedViewRepository(db)
	other := newView(t, 7, "bo", "bo 的视图", "kb-1")
	require.NoError(t, repo.Create(context.Background(), other))

	_, err := repo.Get(context.Background(), 7, "ana", other.ID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)

	// The owner can read it.
	found, err := repo.Get(context.Background(), 7, "bo", other.ID)
	require.NoError(t, err)
	assert.Equal(t, other.ID, found.ID)
}

// TestSavedViewNameUniquenessPerUser pins the (tenant, user, name) key.
func TestSavedViewNameUniquenessPerUser(t *testing.T) {
	db := setupSavedViewDB(t)
	repo := NewSavedViewRepository(db)
	require.NoError(t, repo.Create(context.Background(), newView(t, 7, "ana", "重名", "kb-1")))

	err := repo.Create(context.Background(), newView(t, 7, "ana", "重名", "kb-2"))
	require.ErrorIs(t, err, ErrSavedViewNameTaken, "the same name is a conflict")

	// A different user may reuse the name: it is per-user, not per-tenant.
	require.NoError(t, repo.Create(context.Background(), newView(t, 7, "bo", "重名", "kb-1")))
	// And so may the same user id in another tenant.
	require.NoError(t, repo.Create(context.Background(), newView(t, 8, "ana", "重名", "kb-1")))
}

// TestSavedViewUpdateRejectsDuplicateName keeps a rename from colliding.
func TestSavedViewUpdateRejectsDuplicateName(t *testing.T) {
	db := setupSavedViewDB(t)
	repo := NewSavedViewRepository(db)
	first := newView(t, 7, "ana", "视图 A", "kb-1")
	second := newView(t, 7, "ana", "视图 B", "kb-1")
	require.NoError(t, repo.Create(context.Background(), first))
	require.NoError(t, repo.Create(context.Background(), second))

	second.Name = "视图 A"
	require.ErrorIs(t, repo.Update(context.Background(), second), ErrSavedViewNameTaken)
}

// TestSavedViewUpdatePersistsFilter covers the JSON column write.
func TestSavedViewUpdatePersistsFilter(t *testing.T) {
	db := setupSavedViewDB(t)
	repo := NewSavedViewRepository(db)
	view := newView(t, 7, "ana", "视图", "kb-1")
	require.NoError(t, repo.Create(context.Background(), view))

	view.Name = "改名后的视图"
	require.NoError(t, view.SetFilter(types.SavedViewFilter{
		Keyword:           "E45",
		LifecycleStatuses: []string{"stale"},
	}))
	require.NoError(t, repo.Update(context.Background(), view))

	reloaded, err := repo.Get(context.Background(), 7, "ana", view.ID)
	require.NoError(t, err)
	assert.Equal(t, "改名后的视图", reloaded.Name)
	filter := reloaded.SavedViewFilterOf()
	assert.Equal(t, "E45", filter.Keyword)
	assert.Equal(t, []string{"stale"}, filter.LifecycleStatuses)
}

// TestSavedViewUpdateCannotTouchAnotherUsersRow is the horizontal check.
func TestSavedViewUpdateCannotTouchAnotherUsersRow(t *testing.T) {
	db := setupSavedViewDB(t)
	repo := NewSavedViewRepository(db)
	victim := newView(t, 7, "bo", "bo 的视图", "kb-1")
	require.NoError(t, repo.Create(context.Background(), victim))

	// Ana tries to update Bo's row by ID.
	stolen := newView(t, 7, "ana", "被改的视图", "kb-1")
	stolen.ID = victim.ID
	err := repo.Update(context.Background(), stolen)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)

	stored, err := repo.Get(context.Background(), 7, "bo", victim.ID)
	require.NoError(t, err)
	assert.Equal(t, "bo 的视图", stored.Name, "the other user's row is untouched")
}

// TestSavedViewDeleteIsScoped covers the same rule for deletes.
func TestSavedViewDeleteIsScoped(t *testing.T) {
	db := setupSavedViewDB(t)
	repo := NewSavedViewRepository(db)
	victim := newView(t, 7, "bo", "bo 的视图", "kb-1")
	require.NoError(t, repo.Create(context.Background(), victim))

	err := repo.Delete(context.Background(), 7, "ana", victim.ID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)

	_, getErr := repo.Get(context.Background(), 7, "bo", victim.ID)
	require.NoError(t, getErr, "the row survived the cross-user delete")

	require.NoError(t, repo.Delete(context.Background(), 7, "bo", victim.ID))
	_, goneErr := repo.Get(context.Background(), 7, "bo", victim.ID)
	require.ErrorIs(t, goneErr, gorm.ErrRecordNotFound)
}

// TestSavedViewCountIsPerUser bounds the quota correctly.
func TestSavedViewCountIsPerUser(t *testing.T) {
	db := setupSavedViewDB(t)
	repo := NewSavedViewRepository(db)
	require.NoError(t, repo.Create(context.Background(), newView(t, 7, "ana", "a", "kb-1")))
	require.NoError(t, repo.Create(context.Background(), newView(t, 7, "ana", "b", "kb-1")))
	require.NoError(t, repo.Create(context.Background(), newView(t, 7, "bo", "c", "kb-1")))

	count, err := repo.Count(context.Background(), 7, "ana")
	require.NoError(t, err)
	assert.EqualValues(t, 2, count)
}

// TestClearDefaultScopesToOneKnowledgeBase keeps other spaces' defaults alone.
func TestClearDefaultScopesToOneKnowledgeBase(t *testing.T) {
	db := setupSavedViewDB(t)
	repo := NewSavedViewRepository(db)
	inKB1 := newView(t, 7, "ana", "kb1 默认", "kb-1")
	inKB1.IsDefault = true
	inKB2 := newView(t, 7, "ana", "kb2 默认", "kb-2")
	inKB2.IsDefault = true
	require.NoError(t, repo.Create(context.Background(), inKB1))
	require.NoError(t, repo.Create(context.Background(), inKB2))

	require.NoError(t, repo.ClearDefault(context.Background(), 7, "ana", "kb-1"))

	kb1, err := repo.Get(context.Background(), 7, "ana", inKB1.ID)
	require.NoError(t, err)
	assert.False(t, kb1.IsDefault)

	kb2, err := repo.Get(context.Background(), 7, "ana", inKB2.ID)
	require.NoError(t, err)
	assert.True(t, kb2.IsDefault, "another space's default is unaffected")
}

// TestSetDefaultMarksOneRow covers the promotion.
func TestSetDefaultMarksOneRow(t *testing.T) {
	db := setupSavedViewDB(t)
	repo := NewSavedViewRepository(db)
	view := newView(t, 7, "ana", "视图", "kb-1")
	require.NoError(t, repo.Create(context.Background(), view))

	require.NoError(t, repo.SetDefault(context.Background(), 7, "ana", view.ID))
	stored, err := repo.Get(context.Background(), 7, "ana", view.ID)
	require.NoError(t, err)
	assert.True(t, stored.IsDefault)

	// Promoting another user's row is not-found, not a silent no-op.
	err = repo.SetDefault(context.Background(), 7, "bo", view.ID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

// TestSavedViewListOrdersDefaultFirst keeps the auto-applied view on top.
func TestSavedViewListOrdersDefaultFirst(t *testing.T) {
	db := setupSavedViewDB(t)
	repo := NewSavedViewRepository(db)
	plain := newView(t, 7, "ana", "A 普通", "kb-1")
	promoted := newView(t, 7, "ana", "Z 默认", "kb-1")
	promoted.IsDefault = true
	require.NoError(t, repo.Create(context.Background(), plain))
	require.NoError(t, repo.Create(context.Background(), promoted))

	views, err := repo.List(context.Background(), 7, "ana")
	require.NoError(t, err)
	require.Len(t, views, 2)
	assert.Equal(t, "Z 默认", views[0].Name, "the default sorts first regardless of name")
}

// TestSavedViewCreateAssignsID proves the hook runs.
func TestSavedViewCreateAssignsID(t *testing.T) {
	db := setupSavedViewDB(t)
	repo := NewSavedViewRepository(db)
	view := newView(t, 7, "ana", "视图", "kb-1")
	require.NoError(t, repo.Create(context.Background(), view))
	assert.NotEmpty(t, view.ID, "BeforeCreate must assign an id")
}
