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

// indexCheckTestDDL builds a table with and without an index on purpose, so the
// self-check has something to catch.
const indexCheckTestDDL = `
CREATE TABLE indexed_docs (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL,
    deleted_at DATETIME,
    created_at DATETIME
);
CREATE INDEX idx_indexed_docs_tenant_kb ON indexed_docs (tenant_id, knowledge_base_id);
CREATE TABLE unindexed_docs (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL,
    deleted_at DATETIME,
    created_at DATETIME
);
`

func setupIndexCheckDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(
		sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(indexCheckTestDDL).Error)
	return db
}

// TestExplainReturnsAPlanForAnIndexedQuery is the healthy path.
func TestExplainReturnsAPlanForAnIndexedQuery(t *testing.T) {
	repo := NewIndexCheckRepository(setupIndexCheckDB(t))
	require.Equal(t, "sqlite", repo.Dialect())

	plan, err := repo.Explain(context.Background(), types.IndexCheckQuery{
		Name: "indexed",
		SQL:  "SELECT id FROM indexed_docs WHERE tenant_id = ? AND knowledge_base_id = ?",
		Args: []interface{}{1, "kb-1"},
	})
	require.NoError(t, err)
	require.NotEmpty(t, plan)

	result := types.ParseIndexPlan(repo.Dialect(), plan)
	assert.Equal(t, types.IndexCheckOK, result.Verdict,
		"the plan must show the index being used: %s", plan)
	assert.Contains(t, plan, "idx_indexed_docs_tenant_kb")
}

// TestExplainDetectsAFullScan is the check earning its keep: a query with no
// usable index is reported as a full scan.
func TestExplainDetectsAFullScan(t *testing.T) {
	repo := NewIndexCheckRepository(setupIndexCheckDB(t))

	plan, err := repo.Explain(context.Background(), types.IndexCheckQuery{
		Name: "unindexed",
		SQL:  "SELECT id FROM unindexed_docs WHERE tenant_id = ? AND knowledge_base_id = ?",
		Args: []interface{}{1, "kb-1"},
	})
	require.NoError(t, err)

	result := types.ParseIndexPlan(repo.Dialect(), plan)
	assert.Equal(t, types.IndexCheckFullScan, result.Verdict,
		"a table with no index must be reported as a full scan: %s", plan)
}

// TestExplainSurfacesAMissingTable lets the service mark that check unknown.
func TestExplainSurfacesAMissingTable(t *testing.T) {
	repo := NewIndexCheckRepository(setupIndexCheckDB(t))
	_, err := repo.Explain(context.Background(), types.IndexCheckQuery{
		Name: "missing", SQL: "SELECT id FROM definitely_missing WHERE tenant_id = ?",
		Args: []interface{}{1},
	})
	require.Error(t, err, "a missing table is an error, not an empty plan")
}
