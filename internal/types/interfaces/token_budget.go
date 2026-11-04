package interfaces

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// TokenBudgetRepository persists budgets, fallback chains and the last
// notified band (KnowAxis V3.0 §16.5).
//
// 保存"上次通知到哪一档"是这套设计的关键：没有它，每次检查都会重新通知，
// 而重复的通知等于没有通知。
type TokenBudgetRepository interface {
	// GetBudget returns one tenant's budget; (nil, nil) when none is set.
	GetBudget(ctx context.Context, tenantID uint64) (*types.TokenBudget, error)
	// SaveBudget creates or replaces the tenant's budget.
	SaveBudget(ctx context.Context, budget *types.TokenBudget) error
	// DeleteBudget removes the budget (a tenant with no budget is not gated).
	DeleteBudget(ctx context.Context, tenantID uint64) error
	// SumUsageTokens returns the total tokens (prompt + completion) a tenant
	// spent since `since` (inclusive).
	SumUsageTokens(ctx context.Context, tenantID uint64, since time.Time) (int64, error)

	// GetFallbackChain returns the chain for one scene; (nil, nil) when none.
	GetFallbackChain(
		ctx context.Context, tenantID uint64, scene string,
	) (*types.ModelFallbackChain, error)
	// SaveFallbackChain creates or replaces one scene's chain.
	SaveFallbackChain(ctx context.Context, chain *types.ModelFallbackChain) error
	// DeleteFallbackChain removes one scene's chain.
	DeleteFallbackChain(ctx context.Context, tenantID uint64, scene string) error
	// ListFallbackChains returns every chain of the tenant.
	ListFallbackChains(ctx context.Context, tenantID uint64) ([]*types.ModelFallbackChain, error)

	// LoadNotifiedStatus returns the last band reported for (tenant, period,
	// window start); "" when nothing has been reported in this window.
	LoadNotifiedStatus(
		ctx context.Context, tenantID uint64, period types.TokenBudgetPeriod,
		windowStart time.Time,
	) (types.TokenBudgetStatus, error)
	// SaveNotifiedStatus records the band just reported.
	SaveNotifiedStatus(
		ctx context.Context, tenantID uint64, period types.TokenBudgetPeriod,
		windowStart time.Time, status types.TokenBudgetStatus,
	) error
}

// TokenBudgetService is the §16.5 budget and degradation surface.
type TokenBudgetService interface {
	// GetBudget returns the tenant's budget (a zero-value one when unset).
	GetBudget(ctx context.Context, tenantID uint64) (*types.TokenBudget, error)
	// SaveBudget validates and stores the budget.
	SaveBudget(ctx context.Context, tenantID uint64, budget types.TokenBudget) (*types.TokenBudget, error)
	// Check assesses current spend against the budget and advances the
	// notified band when it crosses one. Callers decide whether to notify.
	Check(ctx context.Context, tenantID uint64) (*types.TokenBudgetAssessment, error)

	// ListFallbackChains returns every configured chain.
	ListFallbackChains(ctx context.Context, tenantID uint64) ([]*types.ModelFallbackChain, error)
	// SaveFallbackChain validates and stores one scene's chain.
	SaveFallbackChain(
		ctx context.Context, tenantID uint64, chain types.ModelFallbackChain,
	) (*types.ModelFallbackChain, error)
	// DeleteFallbackChain removes one scene's chain.
	DeleteFallbackChain(ctx context.Context, tenantID uint64, scene string) error
	// Candidates returns the models to try for one scene, primary first.
	// An unconfigured scene yields just the given primary.
	Candidates(ctx context.Context, tenantID uint64, scene, primaryModelID string) []string
}
