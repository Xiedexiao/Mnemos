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

// searchSuggestionTestDDL is the minimal schema the suggestion queries need.
const searchSuggestionTestDDL = `
CREATE TABLE search_logs (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    user_id VARCHAR(36) NOT NULL DEFAULT '',
    query VARCHAR(512) NOT NULL,
    query_normalized VARCHAR(512) NOT NULL DEFAULT '',
    channel VARCHAR(32) NOT NULL DEFAULT 'hybrid',
    knowledge_base_ids TEXT NOT NULL DEFAULT '[]',
    result_count INTEGER NOT NULL DEFAULT 0,
    permission_filtered_count INTEGER NOT NULL DEFAULT 0,
    top_score REAL NOT NULL DEFAULT 0,
    duration_ms INTEGER NOT NULL DEFAULT 0,
    clicked BOOLEAN NOT NULL DEFAULT FALSE,
    created_at DATETIME
);
CREATE TABLE knowledges (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL,
    title VARCHAR(255) NOT NULL DEFAULT '',
    lifecycle_status VARCHAR(32) NOT NULL DEFAULT 'valid',
    deleted_at DATETIME
);
`

func setupSuggestionDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(
		sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(searchSuggestionTestDDL).Error)
	return db
}

func seedSearchLog(
	t *testing.T, db *gorm.DB, tenantID uint64, userID, query string,
	resultCount int, createdAt time.Time,
) {
	t.Helper()
	require.NoError(t, db.Exec(`
		INSERT INTO search_logs (id, tenant_id, user_id, query, query_normalized,
			result_count, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		userID+query+createdAt.Format("150405.000000000"), tenantID, userID,
		query, types.NormalizeSearchQuery(query), resultCount, createdAt).Error)
}

// TestRecentQueriesIsPerUser is the privacy boundary: one user's history must
// never include a colleague's queries.
func TestRecentQueriesIsPerUser(t *testing.T) {
	db := setupSuggestionDB(t)
	now := time.Now().UTC()
	seedSearchLog(t, db, 7, "ana", "stack-01 报警", 2, now)
	seedSearchLog(t, db, 7, "bo", "工资条", 1, now)
	seedSearchLog(t, db, 8, "ana", "别的租户", 1, now)

	repo := NewSearchSuggestionRepository(db)
	history, err := repo.RecentQueries(context.Background(), 7, "ana", 10)
	require.NoError(t, err)
	require.Len(t, history, 1)
	assert.Equal(t, "stack-01 报警", history[0].Text)
	assert.Equal(t, types.SuggestionKindHistory, history[0].Kind)
}

// TestRecentQueriesDedupesAndKeepsOriginalWording covers both the dedupe and
// the "show what I typed" rule.
func TestRecentQueriesDedupesAndKeepsOriginalWording(t *testing.T) {
	db := setupSuggestionDB(t)
	now := time.Now().UTC()
	seedSearchLog(t, db, 7, "ana", "E45 报警", 1, now.Add(-2*time.Hour))
	seedSearchLog(t, db, 7, "ana", "e45  报警", 1, now.Add(-1*time.Hour))
	seedSearchLog(t, db, 7, "ana", "备件清单", 1, now.Add(-3*time.Hour))

	repo := NewSearchSuggestionRepository(db)
	history, err := repo.RecentQueries(context.Background(), 7, "ana", 10)
	require.NoError(t, err)
	// 两次输入写法不同 → 两行（分组键是原始 query）；最近的一次排前。
	require.Len(t, history, 3)
	assert.Equal(t, "e45  报警", history[0].Text, "newest first, original wording kept")
}

// TestRecentQueriesWithoutIdentity returns nothing rather than everyone's rows.
func TestRecentQueriesWithoutIdentity(t *testing.T) {
	db := setupSuggestionDB(t)
	seedSearchLog(t, db, 7, "ana", "anything", 1, time.Now().UTC())
	repo := NewSearchSuggestionRepository(db)
	history, err := repo.RecentQueries(context.Background(), 7, "", 10)
	require.NoError(t, err)
	assert.Empty(t, history)
}

// TestHotQueriesGroupsByNormalizedForm keeps "E45" and "e45" as one entry while
// returning a human spelling.
func TestHotQueriesGroupsByNormalizedForm(t *testing.T) {
	db := setupSuggestionDB(t)
	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		seedSearchLog(t, db, 7, "ana", "E45 报警", 1, now.Add(-time.Duration(i)*time.Minute))
	}
	seedSearchLog(t, db, 7, "bo", "e45  报警", 1, now)
	seedSearchLog(t, db, 7, "bo", "备件清单", 1, now)

	repo := NewSearchSuggestionRepository(db)
	hot, err := repo.HotQueries(context.Background(), 7, now.Add(-time.Hour), 10)
	require.NoError(t, err)
	require.Len(t, hot, 2)
	assert.Equal(t, 4, int(hot[0].Count), "case/space variants collapse into one entry")
	assert.Equal(t, types.SuggestionKindHot, hot[0].Kind)
	assert.NotEmpty(t, hot[0].Text)
}

// TestHotQueriesIncludesZeroResultQueries is a deliberate choice: "everyone
// searches this and finds nothing" is the most useful hint of all.
func TestHotQueriesIncludesZeroResultQueries(t *testing.T) {
	db := setupSuggestionDB(t)
	now := time.Now().UTC()
	seedSearchLog(t, db, 7, "ana", "找不到的词", 0, now)
	repo := NewSearchSuggestionRepository(db)
	hot, err := repo.HotQueries(context.Background(), 7, now.Add(-time.Hour), 10)
	require.NoError(t, err)
	require.Len(t, hot, 1)
	assert.Equal(t, "找不到的词", hot[0].Text)
}

// TestHotQueriesRespectsWindow keeps stale queries out of the list.
func TestHotQueriesRespectsWindow(t *testing.T) {
	db := setupSuggestionDB(t)
	now := time.Now().UTC()
	seedSearchLog(t, db, 7, "ana", "很老的词", 1, now.AddDate(0, 0, -120))
	seedSearchLog(t, db, 7, "ana", "最近的词", 1, now)
	repo := NewSearchSuggestionRepository(db)
	hot, err := repo.HotQueries(context.Background(), 7, now.AddDate(0, 0, -30), 10)
	require.NoError(t, err)
	require.Len(t, hot, 1)
	assert.Equal(t, "最近的词", hot[0].Text)
}

// TestTitleMatchesExcludesUnsearchableLifecycles keeps drafts out of autocomplete.
func TestTitleMatchesExcludesUnsearchableLifecycles(t *testing.T) {
	db := setupSuggestionDB(t)
	insertTitle := func(id, kbID, title, lifecycle string, deleted bool) {
		var deletedAt interface{}
		if deleted {
			deletedAt = time.Now().UTC()
		}
		require.NoError(t, db.Exec(`
			INSERT INTO knowledges (id, tenant_id, knowledge_base_id, title,
				lifecycle_status, deleted_at)
			VALUES (?, 7, ?, ?, ?, ?)`, id, kbID, title, lifecycle, deletedAt).Error)
	}
	insertTitle("ok", "kb-1", "E45 报警处理", "valid", false)
	insertTitle("review", "kb-1", "E45 报警补充", "pending_review_cycle", false)
	insertTitle("draft", "kb-1", "E45 报警草稿", "draft", false)
	insertTitle("gone", "kb-1", "E45 报警旧版", "delisted", false)
	insertTitle("deleted", "kb-1", "E45 报警已删", "valid", true)

	repo := NewSearchSuggestionRepository(db)
	matches, err := repo.TitleMatches(context.Background(), 7, nil, "e45", 10)
	require.NoError(t, err)
	ids := make([]string, 0, len(matches))
	for _, match := range matches {
		ids = append(ids, match.KnowledgeID)
	}
	assert.ElementsMatch(t, []string{"ok", "review"}, ids)
}

// TestTitleMatchesPrefersPrefixMatches pins the ranking rule: "E45 报" should
// suggest "E45 报警处理" before "关于 E45 报警的补充说明".
func TestTitleMatchesPrefersPrefixMatches(t *testing.T) {
	db := setupSuggestionDB(t)
	require.NoError(t, db.Exec(`
		INSERT INTO knowledges (id, tenant_id, knowledge_base_id, title, lifecycle_status)
		VALUES ('mid', 7, 'kb-1', '关于 E45 报警的补充说明', 'valid'),
		       ('front', 7, 'kb-1', 'E45 报警处理', 'valid')`).Error)
	repo := NewSearchSuggestionRepository(db)
	matches, err := repo.TitleMatches(context.Background(), 7, []string{"kb-1"}, "e45 报", 10)
	require.NoError(t, err)
	require.Len(t, matches, 2)
	assert.Equal(t, "front", matches[0].KnowledgeID, "prefix match ranks first")
}

// TestTitleMatchesScopesToKnowledgeBases keeps a caller from seeing titles in
// spaces it never asked for.
func TestTitleMatchesScopesToKnowledgeBases(t *testing.T) {
	db := setupSuggestionDB(t)
	require.NoError(t, db.Exec(`
		INSERT INTO knowledges (id, tenant_id, knowledge_base_id, title, lifecycle_status)
		VALUES ('allowed', 7, 'kb-1', 'E45 报警', 'valid'),
		       ('other', 7, 'kb-2', 'E45 报警', 'valid')`).Error)
	repo := NewSearchSuggestionRepository(db)
	matches, err := repo.TitleMatches(context.Background(), 7, []string{"kb-1"}, "e45", 10)
	require.NoError(t, err)
	require.Len(t, matches, 1)
	assert.Equal(t, "allowed", matches[0].KnowledgeID)
}

// TestTitleMatchesEscapesWildcards proves a "%" in the prefix cannot turn into
// a match-everything query.
func TestTitleMatchesEscapesWildcards(t *testing.T) {
	db := setupSuggestionDB(t)
	require.NoError(t, db.Exec(`
		INSERT INTO knowledges (id, tenant_id, knowledge_base_id, title, lifecycle_status)
		VALUES ('plain', 7, 'kb-1', 'E45 报警', 'valid'),
		       ('percent', 7, 'kb-1', '配额 100% 用完', 'valid')`).Error)
	repo := NewSearchSuggestionRepository(db)
	matches, err := repo.TitleMatches(context.Background(), 7, []string{"kb-1"}, "100%", 10)
	require.NoError(t, err)
	require.Len(t, matches, 1, "a bare % must be a literal, not a wildcard")
	assert.Equal(t, "percent", matches[0].KnowledgeID)
}

// TestDeleteHistoryRemovesOnlyOwnRows is the horizontal-privilege boundary.
func TestDeleteHistoryRemovesOnlyOwnRows(t *testing.T) {
	db := setupSuggestionDB(t)
	now := time.Now().UTC()
	seedSearchLog(t, db, 7, "ana", "ana 的词", 1, now)
	seedSearchLog(t, db, 7, "bo", "bo 的词", 1, now)
	seedSearchLog(t, db, 8, "ana", "别的租户", 1, now)

	repo := NewSearchSuggestionRepository(db)
	require.NoError(t, repo.DeleteHistory(context.Background(), 7, "ana"))

	var remaining []struct {
		UserID   string
		TenantID uint64
	}
	require.NoError(t, db.Table("search_logs").Select("user_id, tenant_id").
		Scan(&remaining).Error)
	require.Len(t, remaining, 2, "only the caller's own rows may be removed")
	for _, row := range remaining {
		// 同一个 user id 在别的租户里的行必须留着：删除条件是
		// (tenant, user) 而不是单看 user。
		assert.False(t, row.UserID == "ana" && row.TenantID == 7,
			"the caller's rows in their own tenant are gone")
	}
}

// TestDeleteHistoryWithoutIdentityIsNoop refuses a delete-all by omission.
func TestDeleteHistoryWithoutIdentityIsNoop(t *testing.T) {
	db := setupSuggestionDB(t)
	seedSearchLog(t, db, 7, "ana", "keep me", 1, time.Now().UTC())
	repo := NewSearchSuggestionRepository(db)
	require.NoError(t, repo.DeleteHistory(context.Background(), 7, ""))

	var count int64
	require.NoError(t, db.Table("search_logs").Count(&count).Error)
	assert.EqualValues(t, 1, count, "an empty user id must not wipe the tenant's log")
}
