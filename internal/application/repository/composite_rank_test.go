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

// compositeRankTestDDL is the minimal schema the composite query needs.
const compositeRankTestDDL = `
CREATE TABLE knowledges (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL,
    title VARCHAR(255) NOT NULL DEFAULT '',
    file_name VARCHAR(255) NOT NULL DEFAULT '',
    current_version INTEGER NOT NULL DEFAULT 1,
    lifecycle_status VARCHAR(32) NOT NULL DEFAULT 'valid',
    parse_status VARCHAR(50) NOT NULL DEFAULT 'completed',
    source VARCHAR(2048) NOT NULL DEFAULT '',
    classification VARCHAR(16) NOT NULL DEFAULT 'internal',
    expires_at DATETIME,
    last_reviewed_at DATETIME,
    review_cycle_days INTEGER NOT NULL DEFAULT 0,
    custom_metadata TEXT NOT NULL DEFAULT '{}',
    created_at DATETIME,
    updated_at DATETIME,
    deleted_at DATETIME
);
CREATE TABLE knowledge_view_events (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    user_id VARCHAR(36) NOT NULL DEFAULT '',
    knowledge_id VARCHAR(36) NOT NULL,
    created_at DATETIME
);
CREATE TABLE user_resource_favorites (
    user_id VARCHAR(36) NOT NULL,
    tenant_id INTEGER NOT NULL,
    resource_type VARCHAR(16) NOT NULL,
    resource_id VARCHAR(64) NOT NULL,
    created_at DATETIME
);
CREATE TABLE evidence_packs (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    refusal VARCHAR(32) NOT NULL DEFAULT '',
    citations TEXT NOT NULL DEFAULT '[]',
    created_at DATETIME
);
`

func setupCompositeDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(
		sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(compositeRankTestDDL).Error)
	return db
}

// seedCompositeKnowledge inserts one document.
func seedCompositeKnowledge(
	t *testing.T, db *gorm.DB, id, title, level, lifecycle string, updatedAt time.Time,
) {
	t.Helper()
	metadata := "{}"
	if level != "" {
		metadata = `{"knowaxis_level":"` + level + `"}`
	}
	require.NoError(t, db.Exec(`
		INSERT INTO knowledges (id, tenant_id, knowledge_base_id, title, file_name,
			current_version, lifecycle_status, classification, custom_metadata,
			created_at, updated_at)
		VALUES (?, 7, 'kb-1', ?, ?, 1, ?, 'internal', ?, ?, ?)`,
		id, title, title+".md", lifecycle, metadata, updatedAt, updatedAt).Error)
}

// TestCompositeRankPageScoresAndOrders covers the end-to-end path: the L3
// document outranks a plain one with identical heat.
func TestCompositeRankPageScoresAndOrders(t *testing.T) {
	db := setupCompositeDB(t)
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	seedCompositeKnowledge(t, db, "plain", "闲聊记录", "", "valid", now)
	seedCompositeKnowledge(t, db, "auth", "报警处理规程", "L3", "valid", now)

	repo := NewKnowledgeRepository(db)
	page := &types.Pagination{Page: 1, PageSize: 10}
	result, scored, err := repo.CompositeRankPage(
		context.Background(), 7, "kb-1", page, types.KnowledgeListFilter{}, "", now)
	require.NoError(t, err)
	require.Len(t, result.Rows, 2)
	assert.EqualValues(t, 2, scored)
	assert.Equal(t, 2, result.ScoredCount)
	assert.Equal(t, "auth", result.Rows[0].KnowledgeID, "L3 outranks a plain document")
	assert.InDelta(t, 1.0, result.Rows[0].Breakdown.Authority.Value, 0.001)
	assert.Equal(t, "L3 权威", result.Rows[0].Breakdown.Authority.Raw)
}

// TestCompositeRankPageCountsHotness proves the three counters reach the score.
func TestCompositeRankPageCountsHotness(t *testing.T) {
	db := setupCompositeDB(t)
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	seedCompositeKnowledge(t, db, "cold", "无热度", "", "valid", now)
	seedCompositeKnowledge(t, db, "hot", "很热", "", "valid", now)

	for i := 0; i < 5; i++ {
		require.NoError(t, db.Exec(`
			INSERT INTO knowledge_view_events (id, tenant_id, user_id, knowledge_id, created_at)
			VALUES (?, 7, 'ana', 'hot', ?)`, "v"+string(rune('a'+i)), now).Error)
	}
	require.NoError(t, db.Exec(`
		INSERT INTO user_resource_favorites (user_id, tenant_id, resource_type, resource_id, created_at)
		VALUES ('ana', 7, 'knowledge', 'hot', ?)`, now).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO evidence_packs (id, tenant_id, refusal, citations, created_at)
		VALUES ('p1', 7, '', '[{"knowledge_id":"hot"}]', ?)`, now).Error)

	repo := NewKnowledgeRepository(db)
	page := &types.Pagination{Page: 1, PageSize: 10}
	result, _, err := repo.CompositeRankPage(
		context.Background(), 7, "kb-1", page, types.KnowledgeListFilter{}, "", now)
	require.NoError(t, err)
	require.Len(t, result.Rows, 2)

	byID := map[string]types.CompositeRankBreakdown{}
	for _, row := range result.Rows {
		byID[row.KnowledgeID] = row.Breakdown
	}
	assert.InDelta(t, 1.0, byID["hot"].Hotness.Value, 0.001)
	assert.InDelta(t, 0.0, byID["cold"].Hotness.Value, 0.001)
	assert.Contains(t, byID["hot"].Hotness.Raw, "views=5")
	assert.Contains(t, byID["hot"].Hotness.Raw, "citations=1")
	assert.Contains(t, byID["cold"].Hotness.Raw, "无浏览")
}

// TestCompositeRankPageRespectsKeyword proves relevance enters the query path.
func TestCompositeRankPageRespectsKeyword(t *testing.T) {
	db := setupCompositeDB(t)
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	seedCompositeKnowledge(t, db, "off", "备件清单", "L3", "valid", now)
	seedCompositeKnowledge(t, db, "on", "报警处理规程", "", "valid", now)

	repo := NewKnowledgeRepository(db)
	page := &types.Pagination{Page: 1, PageSize: 10}
	result, _, err := repo.CompositeRankPage(
		context.Background(), 7, "kb-1", page, types.KnowledgeListFilter{},
		"报警处理规程", now)
	require.NoError(t, err)
	require.Len(t, result.Rows, 2)
	assert.Equal(t, "on", result.Rows[0].KnowledgeID)
	assert.Equal(t, "报警处理规程", result.Keyword)
}

// TestCompositeRankPageAppliesFilters proves the §2.10 denial set still applies:
// composite ordering must not become a way around the list filter.
func TestCompositeRankPageAppliesFilters(t *testing.T) {
	db := setupCompositeDB(t)
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	seedCompositeKnowledge(t, db, "visible", "可见条目", "", "valid", now)
	seedCompositeKnowledge(t, db, "denied", "被拒条目", "L3", "valid", now)

	repo := NewKnowledgeRepository(db)
	page := &types.Pagination{Page: 1, PageSize: 10}
	result, _, err := repo.CompositeRankPage(
		context.Background(), 7, "kb-1", page,
		types.KnowledgeListFilter{ExcludeKnowledgeIDs: []string{"denied"}}, "", now)
	require.NoError(t, err)
	require.Len(t, result.Rows, 1)
	assert.Equal(t, "visible", result.Rows[0].KnowledgeID)
}

// TestCompositeRankPageCapsCandidates bounds the window.
func TestCompositeRankPageCapsCandidates(t *testing.T) {
	db := setupCompositeDB(t)
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 12; i++ {
		seedCompositeKnowledge(t, db, "doc-"+string(rune('a'+i)),
			"文档 "+string(rune('a'+i)), "", "valid", now.Add(-time.Duration(i)*time.Hour))
	}
	repo := NewKnowledgeRepository(db)
	page := &types.Pagination{Page: 1, PageSize: 2}
	result, _, err := repo.CompositeRankPage(
		context.Background(), 7, "kb-1", page, types.KnowledgeListFilter{}, "", now)
	require.NoError(t, err)
	// 2 rows × factor 5 = 10 candidates.
	assert.Equal(t, 10, result.ScoredCount)
}

// TestCompositeRankPageMarksUnlabelledLevels keeps a plain document at the
// baseline rather than guessing a level.
func TestCompositeRankPageMarksUnlabelledLevels(t *testing.T) {
	db := setupCompositeDB(t)
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	seedCompositeKnowledge(t, db, "plain", "普通文档", "", "valid", now)

	repo := NewKnowledgeRepository(db)
	result, _, err := repo.CompositeRankPage(
		context.Background(), 7, "kb-1", &types.Pagination{Page: 1, PageSize: 5},
		types.KnowledgeListFilter{}, "", now)
	require.NoError(t, err)
	require.Len(t, result.Rows, 1)
	assert.Equal(t, "有效且未降级", result.Rows[0].Breakdown.Authority.Raw)
}

// TestKnowledgeLevelFromMetadata covers the parser's edges.
func TestKnowledgeLevelFromMetadata(t *testing.T) {
	assert.Equal(t, "L3", knowledgeLevelFromMetadata(types.JSON(`{"knowaxis_level":"L3"}`)))
	assert.Equal(t, "L1", knowledgeLevelFromMetadata(types.JSON(`{"knowaxis_level":"L1"}`)))
	// Unknown / malformed / missing values mean "unlabelled".
	assert.Equal(t, "", knowledgeLevelFromMetadata(types.JSON(`{"knowaxis_level":"L9"}`)))
	assert.Equal(t, "", knowledgeLevelFromMetadata(types.JSON(`{"other":"x"}`)))
	assert.Equal(t, "", knowledgeLevelFromMetadata(types.JSON(`{not json`)))
	assert.Equal(t, "", knowledgeLevelFromMetadata(nil))
	assert.Equal(t, "", knowledgeLevelFromMetadata(types.JSON(`{}`)))
}
