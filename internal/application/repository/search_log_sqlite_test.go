package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSearchLogOverviewOnSQLite guards the dashboard's portability.
//
// 背景更正：COUNT(...) FILTER (WHERE ...) 自 SQLite 3.30 起就受支持，而
// gorm.io/driver/sqlite 底层是 mattn/go-sqlite3（版本足够新），所以原来的
// 写法在 Lite 模式下同样可用——我最初以为它是 Postgres 专有语法，这个判断
// 是错的。这里的 CASE WHEN 写法与 FILTER 等价，保留它是因为它不依赖引擎
// 版本，并且这条测试现在确实在 SQLite 上跑通了聚合路径。
func TestSearchLogOverviewOnSQLite(t *testing.T) {
	db := setupKnowledgeTagTestDB(t)
	require.NoError(t, db.Exec(`
		CREATE TABLE IF NOT EXISTS search_logs (
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
			clicked BOOLEAN NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`).Error)

	now := time.Now()
	rows := []struct {
		query  string
		result int
		filter int
		click  bool
		dur    int
	}{
		{"e45", 0, 3, false, 120},
		{"sop", 2, 0, true, 2400},
		{"stack", 0, 1, false, 80},
	}
	for i, row := range rows {
		require.NoError(t, db.Exec(`
			INSERT INTO search_logs (id, tenant_id, query, query_normalized, result_count,
				permission_filtered_count, duration_ms, clicked, created_at)
			VALUES (?, 7, ?, ?, ?, ?, ?, ?, ?)
		`, "log-"+string(rune('a'+i)), row.query, row.query, row.result, row.filter, row.dur, row.click, now).Error)
	}

	repo := NewSearchLogRepository(db)
	overview, err := repo.Overview(context.Background(), 7, now.Add(-time.Hour))
	require.NoError(t, err, "the overview query must be portable to SQLite")
	assert.EqualValues(t, 3, overview.TotalQueries)
	assert.EqualValues(t, 2, overview.ZeroResultQueries)
	assert.EqualValues(t, 4, overview.PermissionFilteredTotal)
	assert.EqualValues(t, 1, overview.ClickedQueries)
	assert.InDelta(t, 0.6667, overview.ZeroResultRate, 0.01)
}

// TestFrequentQueriesOnSQLite covers the hot-query aggregation path.
func TestFrequentQueriesOnSQLite(t *testing.T) {
	db := setupKnowledgeTagTestDB(t)
	require.NoError(t, db.Exec(`
		CREATE TABLE IF NOT EXISTS search_logs (
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
			clicked BOOLEAN NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`).Error)
	now := time.Now()
	for i, query := range []string{"e45", "e45", "sop"} {
		require.NoError(t, db.Exec(`
			INSERT INTO search_logs (id, tenant_id, query, query_normalized, result_count, created_at)
			VALUES (?, 7, ?, ?, 0, ?)
		`, "q-"+string(rune('a'+i)), query, query, now).Error)
	}

	repo := NewSearchLogRepository(db)
	frequent, err := repo.FrequentQueries(context.Background(), 7, now.Add(-time.Hour), true, 10)
	require.NoError(t, err)
	require.NotEmpty(t, frequent)
	assert.Equal(t, "e45", frequent[0].Query)
	assert.EqualValues(t, 2, frequent[0].Occurrences)
}
