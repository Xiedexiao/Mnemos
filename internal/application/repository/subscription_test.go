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

// subscriptionTestDDL is the schema the digest queries run against.
const subscriptionTestDDL = `
CREATE TABLE knowledges (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL,
    title VARCHAR(255) NOT NULL DEFAULT '',
    folder_path VARCHAR(1024) NOT NULL DEFAULT '',
    lifecycle_status VARCHAR(32) NOT NULL DEFAULT 'valid',
    classification VARCHAR(16) NOT NULL DEFAULT 'internal',
    deleted_at DATETIME,
    updated_at DATETIME
);
CREATE TABLE knowledge_versions (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    knowledge_id VARCHAR(36) NOT NULL,
    published_by VARCHAR(36) NOT NULL DEFAULT ''
);
CREATE TABLE knowledge_tags (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL DEFAULT '',
    name VARCHAR(128) NOT NULL
);
CREATE TABLE knowledge_tag_relations (
    knowledge_id VARCHAR(36) NOT NULL,
    tag_id VARCHAR(36) NOT NULL,
    PRIMARY KEY (knowledge_id, tag_id)
);
CREATE TABLE subscriptions (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    user_id VARCHAR(36) NOT NULL,
    subject_type VARCHAR(16) NOT NULL,
    subject_id VARCHAR(191) NOT NULL,
    space_id VARCHAR(36) NOT NULL DEFAULT '',
    label VARCHAR(191) NOT NULL DEFAULT '',
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    last_digest_at DATETIME,
    created_at DATETIME,
    updated_at DATETIME
);
CREATE UNIQUE INDEX uq_subscriptions_user_subject
    ON subscriptions (tenant_id, user_id, subject_type, subject_id);
`

func setupSubscriptionDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(subscriptionTestDDL).Error)
	return db
}

func seedDigestKnowledge(t *testing.T, db *gorm.DB, id, kbID string, updatedAt time.Time) {
	t.Helper()
	require.NoError(t, db.Exec(`
		INSERT INTO knowledges (id, tenant_id, knowledge_base_id, title, folder_path,
			lifecycle_status, classification, updated_at)
		VALUES (?, 7, ?, ?, 'sop', 'valid', 'internal', ?)`,
		id, kbID, id, updatedAt).Error)
}

// TestDigestQueriesExecuteOnRealSQL is the regression guard for a bug the
// service-level stubs could not catch: the digest queries grouped by a
// projection string that carried `AS` aliases, which SQLite rejects outright
// ("near AS: syntax error"). Every digest source is exercised here against a
// real database so a broken query fails the build rather than only failing in
// production.
func TestDigestQueriesExecuteOnRealSQL(t *testing.T) {
	db := setupSubscriptionDB(t)
	now := time.Now().UTC()
	since := now.Add(-time.Hour)

	seedDigestKnowledge(t, db, "k1", "kb-1", now.Add(-10*time.Minute))
	seedDigestKnowledge(t, db, "k2", "kb-1", now.Add(-2*time.Hour)) // outside window
	require.NoError(t, db.Exec(`
		INSERT INTO knowledge_tags (id, tenant_id, knowledge_base_id, name)
		VALUES ('t1', 7, 'kb-1', '工艺')`).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO knowledge_tag_relations (knowledge_id, tag_id) VALUES ('k1', 't1')`).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO knowledge_versions (id, tenant_id, knowledge_id, published_by)
		VALUES ('v1', 7, 'k1', 'author-1')`).Error)

	repo := NewSubscriptionRepository(db)
	ctx := context.Background()

	space, err := repo.ChangesSince(ctx, 7, "kb-1", since, 100)
	require.NoError(t, err)
	require.Len(t, space, 1, "only the document inside the window")
	assert.Equal(t, "k1", space[0].KnowledgeID)
	assert.Equal(t, "kb-1", space[0].KnowledgeBaseID)

	tagged, err := repo.TagChangesSince(ctx, 7, "工艺", since, 100)
	require.NoError(t, err)
	require.Len(t, tagged, 1)
	assert.Equal(t, "k1", tagged[0].KnowledgeID)

	authored, err := repo.AuthorChangesSince(ctx, 7, "author-1", since, 100)
	require.NoError(t, err)
	require.Len(t, authored, 1)
	assert.Equal(t, "k1", authored[0].KnowledgeID)

	single, err := repo.KnowledgeChangesSince(ctx, 7, "k1", since)
	require.NoError(t, err)
	require.Len(t, single, 1)

	// Outside the window every source is empty, not an error.
	empty, err := repo.TagChangesSince(ctx, 7, "工艺", now.Add(time.Hour), 100)
	require.NoError(t, err)
	assert.Empty(t, empty)
}

// TestSubscriptionUpsertReEnablesInsteadOfDuplicating covers the follow contract.
func TestSubscriptionUpsertReEnablesInsteadOfDuplicating(t *testing.T) {
	db := setupSubscriptionDB(t)
	repo := NewSubscriptionRepository(db)
	ctx := context.Background()

	first := &types.Subscription{
		TenantID: 7, UserID: "u-1", SubjectType: "space", SubjectID: "kb-1",
		SpaceID: "kb-1", Enabled: true,
	}
	created, err := repo.Upsert(ctx, first)
	require.NoError(t, err)
	assert.True(t, created)
	require.NoError(t, repo.SetEnabled(ctx, 7, "u-1", first.ID, false))

	again := &types.Subscription{
		TenantID: 7, UserID: "u-1", SubjectType: "space", SubjectID: "kb-1",
		SpaceID: "kb-1", Label: "工艺空间", Enabled: true,
	}
	created, err = repo.Upsert(ctx, again)
	require.NoError(t, err)
	assert.False(t, created, "a second subscribe resumes the original row")
	assert.Equal(t, first.ID, again.ID)

	subs, err := repo.List(ctx, 7, "u-1", true)
	require.NoError(t, err)
	require.Len(t, subs, 1)
	assert.Equal(t, "工艺空间", subs[0].Label, "the label is refreshed")
}

// TestFolderPathsLookup covers the folder-subscription helper.
func TestFolderPathsLookup(t *testing.T) {
	db := setupSubscriptionDB(t)
	seedDigestKnowledge(t, db, "k1", "kb-1", time.Now().UTC())
	repo := NewSubscriptionRepository(db)

	paths, err := repo.FolderPaths(context.Background(), 7, []string{"k1", "missing"})
	require.NoError(t, err)
	assert.Equal(t, "sop", paths["k1"])
	assert.NotContains(t, paths, "missing")
}
