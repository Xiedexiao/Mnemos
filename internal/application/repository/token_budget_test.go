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

// tokenBudgetTestDDL is the minimal schema the budget queries need.
const tokenBudgetTestDDL = `
CREATE TABLE token_budgets (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    period VARCHAR(16) NOT NULL DEFAULT 'monthly',
    limit_tokens BIGINT NOT NULL DEFAULT 0,
    warn_percent INTEGER NOT NULL DEFAULT 80,
    updated_at DATETIME
);
CREATE UNIQUE INDEX uq_token_budgets_tenant ON token_budgets (tenant_id);
CREATE TABLE model_fallback_chains (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    scene VARCHAR(32) NOT NULL,
    model_ids TEXT NOT NULL DEFAULT '[]',
    description VARCHAR(255) NOT NULL DEFAULT '',
    updated_at DATETIME
);
CREATE UNIQUE INDEX uq_model_fallback_chains_tenant_scene
    ON model_fallback_chains (tenant_id, scene);
CREATE TABLE token_budget_notices (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    period VARCHAR(16) NOT NULL,
    window_start DATETIME NOT NULL,
    status VARCHAR(16) NOT NULL,
    notified_at DATETIME
);
CREATE UNIQUE INDEX uq_token_budget_notices
    ON token_budget_notices (tenant_id, period, window_start);
CREATE TABLE model_usage_daily (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    day DATE NOT NULL,
    model_id VARCHAR(64) NOT NULL,
    scene VARCHAR(32) NOT NULL DEFAULT 'other',
    knowledge_base_id VARCHAR(36) NOT NULL DEFAULT '',
    prompt_tokens BIGINT NOT NULL DEFAULT 0,
    completion_tokens BIGINT NOT NULL DEFAULT 0,
    call_count BIGINT NOT NULL DEFAULT 0
);
`

func setupTokenBudgetDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(
		sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(tokenBudgetTestDDL).Error)
	return db
}

// TestTokenBudgetUpsertKeepsOneRowPerTenant proves the uniqueness rule: a
// second save replaces, it does not add.
func TestTokenBudgetUpsertKeepsOneRowPerTenant(t *testing.T) {
	db := setupTokenBudgetDB(t)
	repo := NewTokenBudgetRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.SaveBudget(ctx, &types.TokenBudget{
		TenantID: 7, Period: types.TokenBudgetDaily, LimitTokens: 100, WarnPercent: 80,
	}))
	require.NoError(t, repo.SaveBudget(ctx, &types.TokenBudget{
		TenantID: 7, Period: types.TokenBudgetMonthly, LimitTokens: 5000, WarnPercent: 90,
	}))

	var count int64
	require.NoError(t, db.Table("token_budgets").Count(&count).Error)
	assert.EqualValues(t, 1, count)

	stored, err := repo.GetBudget(ctx, 7)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, types.TokenBudgetMonthly, stored.Period)
	assert.EqualValues(t, 5000, stored.LimitTokens)
	assert.Equal(t, 90, stored.WarnPercent)
}

// TestTokenBudgetMissingIsNotAnError keeps "unconfigured" out of the error path.
func TestTokenBudgetMissingIsNotAnError(t *testing.T) {
	repo := NewTokenBudgetRepository(setupTokenBudgetDB(t))
	budget, err := repo.GetBudget(context.Background(), 7)
	require.NoError(t, err)
	assert.Nil(t, budget, "nil means unset, not a failure")
}

// TestSumUsageTokensCountsPromptPlusCompletion covers both halves and the window.
func TestSumUsageTokensCountsPromptPlusCompletion(t *testing.T) {
	db := setupTokenBudgetDB(t)
	repo := NewTokenBudgetRepository(db)
	ctx := context.Background()

	insert := func(day time.Time, prompt, completion int64) {
		require.NoError(t, db.Exec(`
			INSERT INTO model_usage_daily (id, tenant_id, day, model_id, scene,
				prompt_tokens, completion_tokens, call_count)
			VALUES (?, 7, ?, 'm1', 'chat', ?, ?, 1)`,
			day.Format("150405.000")+"-"+day.Format("20060102")+"-"+string(rune('a'+prompt%26)),
			day, prompt, completion).Error)
	}
	insert(time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC), 100, 50)
	insert(time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC), 200, 25)
	insert(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), 999, 999) // outside the window

	total, err := repo.SumUsageTokens(ctx, 7,
		time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.EqualValues(t, 375, total, "prompt+completion inside the window only")
}

// TestSumUsageTokensWithoutRowsIsZero keeps an empty window measurable.
func TestSumUsageTokensWithoutRowsIsZero(t *testing.T) {
	repo := NewTokenBudgetRepository(setupTokenBudgetDB(t))
	total, err := repo.SumUsageTokens(context.Background(), 7, time.Now().UTC())
	require.NoError(t, err)
	assert.Zero(t, total)
}

// TestFallbackChainUpsertAndList covers the chain CRUD.
func TestFallbackChainUpsertAndList(t *testing.T) {
	db := setupTokenBudgetDB(t)
	repo := NewTokenBudgetRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.SaveFallbackChain(ctx, &types.ModelFallbackChain{
		TenantID: 7, Scene: "chat", ModelIDs: []string{"m1", "m2"},
	}))
	require.NoError(t, repo.SaveFallbackChain(ctx, &types.ModelFallbackChain{
		TenantID: 7, Scene: "chat", ModelIDs: []string{"m3"},
	}))
	require.NoError(t, repo.SaveFallbackChain(ctx, &types.ModelFallbackChain{
		TenantID: 7, Scene: "summary", ModelIDs: []string{"m4"},
	}))

	chain, err := repo.GetFallbackChain(ctx, 7, "chat")
	require.NoError(t, err)
	require.NotNil(t, chain)
	assert.Equal(t, []string{"m3"}, chain.ModelIDs, "the second save replaced the first")

	chains, err := repo.ListFallbackChains(ctx, 7)
	require.NoError(t, err)
	require.Len(t, chains, 2)
	assert.Equal(t, "chat", chains[0].Scene, "ordered by scene")
	assert.Equal(t, "summary", chains[1].Scene)
}

// TestFallbackChainMissingIsNotAnError keeps the default path simple.
func TestFallbackChainMissingIsNotAnError(t *testing.T) {
	repo := NewTokenBudgetRepository(setupTokenBudgetDB(t))
	chain, err := repo.GetFallbackChain(context.Background(), 7, "chat")
	require.NoError(t, err)
	assert.Nil(t, chain)
}

// TestFallbackChainDeleteScopesToScene keeps other scenes' chains.
func TestFallbackChainDeleteScopesToScene(t *testing.T) {
	db := setupTokenBudgetDB(t)
	repo := NewTokenBudgetRepository(db)
	ctx := context.Background()
	require.NoError(t, repo.SaveFallbackChain(ctx, &types.ModelFallbackChain{
		TenantID: 7, Scene: "chat", ModelIDs: []string{"m1"},
	}))
	require.NoError(t, repo.SaveFallbackChain(ctx, &types.ModelFallbackChain{
		TenantID: 7, Scene: "summary", ModelIDs: []string{"m2"},
	}))

	require.NoError(t, repo.DeleteFallbackChain(ctx, 7, "chat"))

	gone, err := repo.GetFallbackChain(ctx, 7, "chat")
	require.NoError(t, err)
	assert.Nil(t, gone)
	kept, err := repo.GetFallbackChain(ctx, 7, "summary")
	require.NoError(t, err)
	assert.NotNil(t, kept)
}

// TestNotifiedStatusRoundTrip covers the notice ledger.
func TestNotifiedStatusRoundTrip(t *testing.T) {
	db := setupTokenBudgetDB(t)
	repo := NewTokenBudgetRepository(db)
	ctx := context.Background()
	window := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

	// Nothing recorded yet.
	status, err := repo.LoadNotifiedStatus(ctx, 7, types.TokenBudgetMonthly, window)
	require.NoError(t, err)
	assert.Empty(t, status)

	require.NoError(t, repo.SaveNotifiedStatus(
		ctx, 7, types.TokenBudgetMonthly, window, types.TokenBudgetWarn))
	status, err = repo.LoadNotifiedStatus(ctx, 7, types.TokenBudgetMonthly, window)
	require.NoError(t, err)
	assert.Equal(t, types.TokenBudgetWarn, status)

	// Advancing the band updates the same row rather than adding one.
	require.NoError(t, repo.SaveNotifiedStatus(
		ctx, 7, types.TokenBudgetMonthly, window, types.TokenBudgetCritical))
	status, err = repo.LoadNotifiedStatus(ctx, 7, types.TokenBudgetMonthly, window)
	require.NoError(t, err)
	assert.Equal(t, types.TokenBudgetCritical, status)

	var count int64
	require.NoError(t, db.Table("token_budget_notices").Count(&count).Error)
	assert.EqualValues(t, 1, count)
}

// TestNotifiedStatusIsPerWindow is what makes the alert reset each period.
func TestNotifiedStatusIsPerWindow(t *testing.T) {
	repo := NewTokenBudgetRepository(setupTokenBudgetDB(t))
	ctx := context.Background()
	november := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	december := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)

	require.NoError(t, repo.SaveNotifiedStatus(
		ctx, 7, types.TokenBudgetMonthly, november, types.TokenBudgetCritical))

	// A new window starts with no notice, so the first crossing alerts again.
	status, err := repo.LoadNotifiedStatus(ctx, 7, types.TokenBudgetMonthly, december)
	require.NoError(t, err)
	assert.Empty(t, status)
}

// TestTokenBudgetIsPerTenant keeps two workspaces apart.
func TestTokenBudgetIsPerTenant(t *testing.T) {
	db := setupTokenBudgetDB(t)
	repo := NewTokenBudgetRepository(db)
	ctx := context.Background()
	require.NoError(t, repo.SaveBudget(ctx, &types.TokenBudget{
		TenantID: 7, Period: types.TokenBudgetDaily, LimitTokens: 100,
	}))
	require.NoError(t, repo.SaveBudget(ctx, &types.TokenBudget{
		TenantID: 8, Period: types.TokenBudgetDaily, LimitTokens: 900,
	}))

	seven, err := repo.GetBudget(ctx, 7)
	require.NoError(t, err)
	require.NotNil(t, seven)
	assert.EqualValues(t, 100, seven.LimitTokens)

	eight, err := repo.GetBudget(ctx, 8)
	require.NoError(t, err)
	require.NotNil(t, eight)
	assert.EqualValues(t, 900, eight.LimitTokens)
}
