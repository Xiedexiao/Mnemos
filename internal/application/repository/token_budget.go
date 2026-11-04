package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// tokenBudgetRepository implements interfaces.TokenBudgetRepository
// (KnowAxis V3.0 §16.5 成本控制).
type tokenBudgetRepository struct {
	db *gorm.DB
}

// NewTokenBudgetRepository constructs the repository.
func NewTokenBudgetRepository(db *gorm.DB) interfaces.TokenBudgetRepository {
	return &tokenBudgetRepository{db: db}
}

// GetBudget returns one tenant's budget.
func (r *tokenBudgetRepository) GetBudget(
	ctx context.Context, tenantID uint64,
) (*types.TokenBudget, error) {
	var budget types.TokenBudget
	err := r.db.WithContext(ctx).
		Where("tenant_id = ?", tenantID).
		First(&budget).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &budget, nil
}

// SaveBudget upserts the tenant's budget.
//
// 租户内只有一行（唯一键 tenant_id）：预算是一个租户口径的结论，允许多行会
// 让"到底哪个生效"无法回答。
func (r *tokenBudgetRepository) SaveBudget(
	ctx context.Context, budget *types.TokenBudget,
) error {
	budget.UpdatedAt = time.Now().UTC()
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "tenant_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"period":       budget.Period,
			"limit_tokens": budget.LimitTokens,
			"warn_percent": budget.WarnPercent,
			"updated_at":   budget.UpdatedAt,
		}),
	}).Create(budget).Error
}

// DeleteBudget removes the budget.
func (r *tokenBudgetRepository) DeleteBudget(
	ctx context.Context, tenantID uint64,
) error {
	return r.db.WithContext(ctx).
		Where("tenant_id = ?", tenantID).
		Delete(&types.TokenBudget{}).Error
}

// SumUsageTokens totals the tokens spent since `since`.
//
// 用 SUM 在库里算而不是把行拉回来加：一个月的按日/模型/场景/空间行可能是
// 几百行，而这里只需要一个数。
func (r *tokenBudgetRepository) SumUsageTokens(
	ctx context.Context, tenantID uint64, since time.Time,
) (int64, error) {
	var total int64
	err := r.db.WithContext(ctx).Table("model_usage_daily").
		Select("COALESCE(SUM(prompt_tokens + completion_tokens), 0)").
		Where("tenant_id = ? AND day >= ?", tenantID, since).
		Scan(&total).Error
	return total, err
}

// GetFallbackChain returns one scene's chain.
func (r *tokenBudgetRepository) GetFallbackChain(
	ctx context.Context, tenantID uint64, scene string,
) (*types.ModelFallbackChain, error) {
	var chain types.ModelFallbackChain
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND scene = ?", tenantID, scene).
		First(&chain).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &chain, nil
}

// SaveFallbackChain upserts one scene's chain.
func (r *tokenBudgetRepository) SaveFallbackChain(
	ctx context.Context, chain *types.ModelFallbackChain,
) error {
	chain.UpdatedAt = time.Now().UTC()
	// Assignments bypasses the model's serializer, so the slice must be
	// encoded here; passing it raw makes the driver treat it as a row value.
	encoded, err := json.Marshal(chain.ModelIDs)
	if err != nil {
		return err
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "tenant_id"}, {Name: "scene"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"model_ids":   types.JSON(encoded),
			"description": chain.Description,
			"updated_at":  chain.UpdatedAt,
		}),
	}).Create(chain).Error
}

// DeleteFallbackChain removes one scene's chain.
func (r *tokenBudgetRepository) DeleteFallbackChain(
	ctx context.Context, tenantID uint64, scene string,
) error {
	return r.db.WithContext(ctx).
		Where("tenant_id = ? AND scene = ?", tenantID, scene).
		Delete(&types.ModelFallbackChain{}).Error
}

// ListFallbackChains returns every chain of the tenant.
func (r *tokenBudgetRepository) ListFallbackChains(
	ctx context.Context, tenantID uint64,
) ([]*types.ModelFallbackChain, error) {
	var chains []*types.ModelFallbackChain
	err := r.db.WithContext(ctx).
		Where("tenant_id = ?", tenantID).
		Order("scene ASC").
		Find(&chains).Error
	return chains, err
}

// LoadNotifiedStatus returns the last reported band for one window.
func (r *tokenBudgetRepository) LoadNotifiedStatus(
	ctx context.Context, tenantID uint64, period types.TokenBudgetPeriod,
	windowStart time.Time,
) (types.TokenBudgetStatus, error) {
	var row types.TokenBudgetNotice
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND period = ? AND window_start = ?",
			tenantID, string(period), windowStart.UTC()).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return types.TokenBudgetStatus(row.Status), nil
}

// SaveNotifiedStatus records the band just reported.
func (r *tokenBudgetRepository) SaveNotifiedStatus(
	ctx context.Context, tenantID uint64, period types.TokenBudgetPeriod,
	windowStart time.Time, status types.TokenBudgetStatus,
) error {
	row := &types.TokenBudgetNotice{
		TenantID:    tenantID,
		Period:      string(period),
		WindowStart: windowStart.UTC(),
		Status:      string(status),
		NotifiedAt:  time.Now().UTC(),
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "tenant_id"}, {Name: "period"}, {Name: "window_start"},
		},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"status":      row.Status,
			"notified_at": row.NotifiedAt,
		}),
	}).Create(row).Error
}
