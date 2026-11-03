package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
)

// costAllocationTestDDL is a minimal schema for the allocation report: the usage
// accumulator plus the storage/index sources the report joins display names
// from.
const costAllocationTestDDL = `
CREATE TABLE model_usage_daily (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    day DATE NOT NULL,
    model_id VARCHAR(64) NOT NULL,
    scene VARCHAR(32) NOT NULL DEFAULT 'other',
    knowledge_base_id VARCHAR(36) NOT NULL DEFAULT '',
    prompt_tokens INTEGER NOT NULL DEFAULT 0,
    completion_tokens INTEGER NOT NULL DEFAULT 0,
    call_count INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX uq_model_usage_daily_tenant_day_model_scene_space
    ON model_usage_daily (tenant_id, day, model_id, scene, knowledge_base_id);

CREATE TABLE knowledge_bases (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    name VARCHAR(255) NOT NULL DEFAULT '',
    deleted_at DATETIME
);
CREATE TABLE knowledges (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL DEFAULT '',
    storage_size INTEGER NOT NULL DEFAULT 0,
    deleted_at DATETIME
);
CREATE TABLE chunks (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL DEFAULT '',
    deleted_at DATETIME
);
`

func setupCostAllocationDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(costAllocationTestDDL).Error)
	return db
}

// TestFillCostAllocatesBySpaceAndScene is the §16.1 requirement: the cost
// block must answer "which space, which scenario", not just a tenant total.
func TestFillCostAllocatesBySpaceAndScene(t *testing.T) {
	db := setupCostAllocationDB(t)
	repo := repository.NewModelGatewayRepository(db)
	ctx := context.Background()
	day := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

	// Same model/day, two spaces and two scenes.
	require.NoError(t, repo.UpsertUsage(ctx, day, 7, "m1", types.ModelUsageSceneChat, "kb1", 100, 50))
	require.NoError(t, repo.UpsertUsage(ctx, day, 7, "m1", types.ModelUsageSceneChat, "kb1", 100, 50))
	require.NoError(t, repo.UpsertUsage(ctx, day, 7, "m1", types.ModelUsageSceneSummary, "kb1", 20, 10))
	require.NoError(t, repo.UpsertUsage(ctx, day, 7, "m1", types.ModelUsageSceneChat, "", 7, 3))
	// Another tenant must not leak into the report.
	require.NoError(t, repo.UpsertUsage(ctx, day, 8, "m1", types.ModelUsageSceneChat, "kb1", 999, 999))

	require.NoError(t, db.Exec(`INSERT INTO knowledge_bases (id, tenant_id, name) VALUES ('kb1', 7, '工艺部')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO knowledges (id, tenant_id, knowledge_base_id, storage_size)
		VALUES ('k1', 7, 'kb1', 1000), ('k2', 7, 'kb1', 2000), ('k3', 7, 'kb1', 500)`).Error)
	require.NoError(t, db.Exec(`UPDATE knowledges SET deleted_at = CURRENT_TIMESTAMP WHERE id = 'k3'`).Error)
	require.NoError(t, db.Exec(`INSERT INTO chunks (id, tenant_id, knowledge_base_id)
		VALUES ('c1', 7, 'kb1'), ('c2', 7, 'kb1'), ('c3', 7, 'kb1')`).Error)

	svc := &operationsDashboardService{db: db, now: func() time.Time { return day.Add(time.Hour) }}
	dashboard := &types.OperationsDashboard{}
	svc.fillCost(ctx, 7, day.AddDate(0, 0, -7), dashboard)

	cost := dashboard.Cost
	assert.EqualValues(t, 227, cost.PromptTokens)
	assert.EqualValues(t, 113, cost.CompletionTokens)
	assert.EqualValues(t, 340, cost.TotalTokens)
	assert.EqualValues(t, 4, cost.CallCount)
	assert.EqualValues(t, 340, cost.ByModel["m1"])
	assert.EqualValues(t, 310, cost.ByScene[types.ModelUsageSceneChat])
	assert.EqualValues(t, 30, cost.ByScene[types.ModelUsageSceneSummary])
	assert.EqualValues(t, 10, cost.UnallocatedTokens,
		"a call with no single space is reported, not guessed onto one")

	byKey := map[string]types.DashboardCostAllocation{}
	for _, allocation := range cost.Allocations {
		byKey[allocation.KnowledgeBaseID+"/"+allocation.Scene] = allocation
	}
	require.Contains(t, byKey, "kb1/chat")
	assert.EqualValues(t, 300, byKey["kb1/chat"].TotalTokens)
	assert.EqualValues(t, 2, byKey["kb1/chat"].CallCount)
	assert.Equal(t, "工艺部", byKey["kb1/chat"].KnowledgeBaseName)
	require.Contains(t, byKey, "kb1/summary")
	assert.EqualValues(t, 30, byKey["kb1/summary"].TotalTokens)
	require.Contains(t, byKey, "/chat")

	require.Len(t, cost.StorageBySpace, 1)
	space := cost.StorageBySpace[0]
	assert.Equal(t, "kb1", space.KnowledgeBaseID)
	assert.Equal(t, "工艺部", space.KnowledgeBaseName)
	assert.EqualValues(t, 3000, space.StorageBytes, "deleted knowledge does not count")
	assert.EqualValues(t, 2, space.KnowledgeCount)
	assert.EqualValues(t, 3, space.ChunkCount)
	assert.EqualValues(t, 3000, cost.TotalStorageBytes)
	assert.EqualValues(t, 3, cost.TotalChunks)
}

// TestUpsertUsageKeepsDimensionsSeparate is the schema-level guarantee: the
// five-column conflict target must not collapse a summary call into the chat
// row for the same model and day.
func TestUpsertUsageKeepsDimensionsSeparate(t *testing.T) {
	db := setupCostAllocationDB(t)
	repo := repository.NewModelGatewayRepository(db)
	ctx := context.Background()
	day := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

	require.NoError(t, repo.UpsertUsage(ctx, day, 7, "m1", types.ModelUsageSceneChat, "kb1", 10, 5))
	require.NoError(t, repo.UpsertUsage(ctx, day, 7, "m1", types.ModelUsageSceneSummary, "kb1", 20, 10))
	require.NoError(t, repo.UpsertUsage(ctx, day, 7, "m1", types.ModelUsageSceneChat, "kb2", 30, 15))

	rows, err := repo.ListUsage(ctx, 7, day.AddDate(0, 0, -1))
	require.NoError(t, err)
	require.Len(t, rows, 3, "each scene/space pair gets its own accumulator row")

	// Same key accumulates instead of inserting a fourth row.
	require.NoError(t, repo.UpsertUsage(ctx, day, 7, "m1", types.ModelUsageSceneChat, "kb1", 1, 1))
	rows, err = repo.ListUsage(ctx, 7, day.AddDate(0, 0, -1))
	require.NoError(t, err)
	require.Len(t, rows, 3)
	for _, row := range rows {
		if row.Scene == types.ModelUsageSceneChat && row.KnowledgeBaseID == "kb1" {
			assert.EqualValues(t, 11, row.PromptTokens)
			assert.EqualValues(t, 6, row.CompletionTokens)
			assert.EqualValues(t, 2, row.CallCount)
		}
	}
}

// TestNormalizeModelUsageScene covers the defaulting rule.
func TestNormalizeModelUsageScene(t *testing.T) {
	assert.Equal(t, types.ModelUsageSceneOther, types.NormalizeModelUsageScene(""))
	assert.Equal(t, types.ModelUsageSceneChat, types.NormalizeModelUsageScene(" CHAT "))
	assert.Equal(t, "custom", types.NormalizeModelUsageScene("custom"),
		"an unknown scene is kept so the row is still recorded")
}
