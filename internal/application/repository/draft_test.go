package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
)

const draftTestDDL = `
CREATE TABLE content_drafts (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    user_id VARCHAR(36) NOT NULL,
    kind VARCHAR(16) NOT NULL,
    scope_id VARCHAR(64) NOT NULL DEFAULT '',
    title VARCHAR(512) NOT NULL DEFAULT '',
    body TEXT NOT NULL DEFAULT '',
    anchor TEXT,
    mentions TEXT,
    revision INTEGER NOT NULL DEFAULT 1,
    updated_at DATETIME,
    created_at DATETIME
);
CREATE UNIQUE INDEX uq_drafts_owner ON content_drafts (tenant_id, user_id, kind, scope_id);
`

func setupDraftDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(draftTestDDL).Error)
	return db
}

// TestDraftSaveIncrementsRevision covers the autosave contract.
func TestDraftSaveIncrementsRevision(t *testing.T) {
	db := setupDraftDB(t)
	repo := NewDraftRepository(db)
	ctx := context.Background()

	first, conflicted, err := repo.Save(ctx, &types.Draft{
		TenantID: 7, UserID: "u-1", Kind: "knowledge", ScopeID: "k-1",
		Title: "草稿", Body: "第一版", UpdatedAt: time.Now().UTC(),
	}, 0)
	require.NoError(t, err)
	assert.False(t, conflicted)
	require.NotNil(t, first)
	assert.Equal(t, 1, first.Revision)

	second, conflicted, err := repo.Save(ctx, &types.Draft{
		TenantID: 7, UserID: "u-1", Kind: "knowledge", ScopeID: "k-1",
		Title: "草稿", Body: "第二版", UpdatedAt: time.Now().UTC(),
	}, 1)
	require.NoError(t, err)
	assert.False(t, conflicted)
	assert.Equal(t, 2, second.Revision)
	assert.Equal(t, "第二版", second.Body)
}

// TestDraftSaveReportsConflictAndReturnsCurrent is the merge contract: a stale
// expected_revision must not overwrite, and the client gets the stored row so
// it can merge.
func TestDraftSaveReportsConflictAndReturnsCurrent(t *testing.T) {
	db := setupDraftDB(t)
	repo := NewDraftRepository(db)
	ctx := context.Background()

	_, _, err := repo.Save(ctx, &types.Draft{
		TenantID: 7, UserID: "u-1", Kind: "knowledge", ScopeID: "k-1",
		Body: "服务端已有内容", UpdatedAt: time.Now().UTC(),
	}, 0)
	require.NoError(t, err)
	_, _, err = repo.Save(ctx, &types.Draft{
		TenantID: 7, UserID: "u-1", Kind: "knowledge", ScopeID: "k-1",
		Body: "服务端第二版", UpdatedAt: time.Now().UTC(),
	}, 1)
	require.NoError(t, err)

	// A client that still believes it is on revision 1 saves.
	current, conflicted, err := repo.Save(ctx, &types.Draft{
		TenantID: 7, UserID: "u-1", Kind: "knowledge", ScopeID: "k-1",
		Body: "客户端旧内容", UpdatedAt: time.Now().UTC(),
	}, 1)
	require.NoError(t, err)
	assert.True(t, conflicted, "a stale revision must not overwrite")
	require.NotNil(t, current)
	assert.Equal(t, "服务端第二版", current.Body, "the client needs the stored row to merge")
	assert.Equal(t, 2, current.Revision)

	// Nothing was written.
	stored, err := repo.Get(ctx, 7, "u-1", types.DraftKindKnowledge, "k-1")
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, "服务端第二版", stored.Body)
}

// TestDraftSaveWithoutBaselineOverwrites keeps "expected_revision omitted" from
// blocking a first autosave.
func TestDraftSaveWithoutBaselineOverwrites(t *testing.T) {
	db := setupDraftDB(t)
	repo := NewDraftRepository(db)
	ctx := context.Background()

	_, _, err := repo.Save(ctx, &types.Draft{
		TenantID: 7, UserID: "u-1", Kind: "knowledge", ScopeID: "k-1",
		Body: "一", UpdatedAt: time.Now().UTC(),
	}, 0)
	require.NoError(t, err)

	saved, conflicted, err := repo.Save(ctx, &types.Draft{
		TenantID: 7, UserID: "u-1", Kind: "knowledge", ScopeID: "k-1",
		Body: "二", UpdatedAt: time.Now().UTC(),
	}, 0)
	require.NoError(t, err)
	assert.False(t, conflicted)
	assert.Equal(t, "二", saved.Body)
}

// TestDraftIsPerUserAndPerScope keeps one user's draft out of another's slot.
func TestDraftIsPerUserAndPerScope(t *testing.T) {
	db := setupDraftDB(t)
	repo := NewDraftRepository(db)
	ctx := context.Background()

	_, _, err := repo.Save(ctx, &types.Draft{
		TenantID: 7, UserID: "u-1", Kind: "knowledge", ScopeID: "k-1",
		Body: "u1", UpdatedAt: time.Now().UTC(),
	}, 0)
	require.NoError(t, err)

	missing, err := repo.Get(ctx, 7, "u-2", types.DraftKindKnowledge, "k-1")
	require.NoError(t, err)
	assert.Nil(t, missing, "another user's draft is invisible")

	// Same user, different scope: a separate slot.
	other, err := repo.Get(ctx, 7, "u-1", types.DraftKindKnowledge, "k-2")
	require.NoError(t, err)
	assert.Nil(t, other)
}

// TestDraftListAndDelete covers listing and removal.
func TestDraftListAndDelete(t *testing.T) {
	db := setupDraftDB(t)
	repo := NewDraftRepository(db)
	ctx := context.Background()

	for _, scope := range []string{"k-1", "k-2"} {
		_, _, err := repo.Save(ctx, &types.Draft{
			TenantID: 7, UserID: "u-1", Kind: "knowledge", ScopeID: scope,
			Body: scope, UpdatedAt: time.Now().UTC(),
		}, 0)
		require.NoError(t, err)
	}
	_, _, err := repo.Save(ctx, &types.Draft{
		TenantID: 7, UserID: "u-1", Kind: "comment", ScopeID: "k-3",
		Body: "comment", UpdatedAt: time.Now().UTC(),
	}, 0)
	require.NoError(t, err)

	all, err := repo.List(ctx, 7, "u-1", "", 50)
	require.NoError(t, err)
	assert.Len(t, all, 3)

	comments, err := repo.List(ctx, 7, "u-1", "comment", 50)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.Equal(t, "comment", comments[0].Body)

	require.NoError(t, repo.Delete(ctx, 7, "u-1", all[0].ID))
	remaining, err := repo.List(ctx, 7, "u-1", "", 50)
	require.NoError(t, err)
	assert.Len(t, remaining, 2)

	// Deleting someone else's draft is a miss, not a silent success.
	require.ErrorIs(t, repo.Delete(ctx, 7, "u-2", remaining[0].ID), ErrDraftNotFound)
}
