package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// modelGatewayRepository implements interfaces.ModelGatewayRepository.
type modelGatewayRepository struct {
	db *gorm.DB
}

// NewModelGatewayRepository constructs the model gateway repository.
func NewModelGatewayRepository(db *gorm.DB) interfaces.ModelGatewayRepository {
	return &modelGatewayRepository{db: db}
}

// CreateRule inserts one routing rule. The (tenant_id, scope, scope_value)
// uniqueness is enforced by the migration's constraint; the service
// pre-checks via FindRuleByScope and translates the duplicate into a 400.
func (r *modelGatewayRepository) CreateRule(ctx context.Context, rule *types.ModelRoutingRule) error {
	if rule == nil {
		return fmt.Errorf("model routing rule: nil row")
	}
	return r.db.WithContext(ctx).Create(rule).Error
}

// UpdateRule persists the full mutable state of a loaded rule (the service
// loads → mutates → saves, so audit-visible fields stay consistent).
func (r *modelGatewayRepository) UpdateRule(ctx context.Context, rule *types.ModelRoutingRule) error {
	if rule == nil {
		return fmt.Errorf("model routing rule: nil row")
	}
	return r.db.WithContext(ctx).Save(rule).Error
}

// DeleteRule physically removes one rule (rules are configuration, not
// lifecycle data — there is no soft-delete state machine). Zero rows
// affected means the rule is gone; surfaced as ErrRecordNotFound.
func (r *modelGatewayRepository) DeleteRule(ctx context.Context, tenantID uint64, ruleID string) error {
	result := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, ruleID).
		Delete(&types.ModelRoutingRule{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// GetRule resolves one rule by ID. Platform-level rules (tenant_id = 0) are
// shared configuration: any tenant may read them (the handler's write paths
// still refuse to mutate another tenant's rule — the save is tenant-scoped
// through the loaded row's own TenantID).
func (r *modelGatewayRepository) GetRule(ctx context.Context, tenantID uint64, ruleID string) (*types.ModelRoutingRule, error) {
	var rule types.ModelRoutingRule
	if err := r.db.WithContext(ctx).
		Where("(tenant_id = ? OR tenant_id = ?) AND id = ?", tenantID, types.PlatformLevelTenantID, ruleID).
		First(&rule).Error; err != nil {
		return nil, err
	}
	return &rule, nil
}

// ListRules returns the tenant's own rules plus the platform-level ones
// (tenant-first) so one list shows the full effective routing picture.
func (r *modelGatewayRepository) ListRules(ctx context.Context, tenantID uint64) ([]*types.ModelRoutingRule, error) {
	var rules []*types.ModelRoutingRule
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? OR tenant_id = ?", tenantID, types.PlatformLevelTenantID).
		Order("tenant_id DESC, scope ASC, scope_value ASC").
		Find(&rules).Error
	return rules, err
}

// findRuleByScope loads the rule for one (tenant, scope, scopeValue) key,
// optionally restricted to enabled rows. Shared by FindEnabledRule and
// FindRuleByScope; not-found is (nil, nil) — the normal miss path.
func (r *modelGatewayRepository) findRuleByScope(
	ctx context.Context, tenantID uint64, scope, scopeValue string, enabledOnly bool,
) (*types.ModelRoutingRule, error) {
	query := r.db.WithContext(ctx).
		Where("tenant_id = ? AND scope = ? AND scope_value = ?", tenantID, scope, scopeValue)
	if enabledOnly {
		query = query.Where("enabled = ?", true)
	}
	var rule types.ModelRoutingRule
	if err := query.First(&rule).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &rule, nil
}

// FindEnabledRule returns the enabled rule for one scope key, or (nil, nil).
func (r *modelGatewayRepository) FindEnabledRule(
	ctx context.Context, tenantID uint64, scope, scopeValue string,
) (*types.ModelRoutingRule, error) {
	return r.findRuleByScope(ctx, tenantID, scope, scopeValue, true)
}

// FindRuleByScope returns the rule for one scope key regardless of enabled
// state (the create-path duplicate check), or (nil, nil).
func (r *modelGatewayRepository) FindRuleByScope(
	ctx context.Context, tenantID uint64, scope, scopeValue string,
) (*types.ModelRoutingRule, error) {
	return r.findRuleByScope(ctx, tenantID, scope, scopeValue, false)
}

// CreatePromptVersion inserts one version row. Uniqueness of
// (tenant_id, template_key, version) is the migration's constraint; the
// service assigns versions and never rewrites history, so a constraint hit
// is a genuine race and surfaces as an error.
func (r *modelGatewayRepository) CreatePromptVersion(ctx context.Context, version *types.PromptVersion) error {
	if version == nil {
		return fmt.Errorf("prompt version: nil row")
	}
	return r.db.WithContext(ctx).Create(version).Error
}

// LatestPromptVersion returns the highest-version row for one template key,
// or (nil, nil) when the key has no versions yet — the loader's fall-back-to-
// YAML path, deliberately not an error.
func (r *modelGatewayRepository) LatestPromptVersion(
	ctx context.Context, tenantID uint64, templateKey string,
) (*types.PromptVersion, error) {
	var version types.PromptVersion
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND template_key = ?", tenantID, templateKey).
		Order("version DESC").
		First(&version).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &version, nil
}

// GetPromptVersion resolves one exact version.
func (r *modelGatewayRepository) GetPromptVersion(
	ctx context.Context, tenantID uint64, templateKey string, version int,
) (*types.PromptVersion, error) {
	var row types.PromptVersion
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND template_key = ? AND version = ?", tenantID, templateKey, version).
		First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// ListPromptVersions returns all versions of one key, newest first.
func (r *modelGatewayRepository) ListPromptVersions(
	ctx context.Context, tenantID uint64, templateKey string,
) ([]*types.PromptVersion, error) {
	var versions []*types.PromptVersion
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND template_key = ?", tenantID, templateKey).
		Order("version DESC").
		Find(&versions).Error
	return versions, err
}

// UpsertUsage adds one call's tokens to the (tenant, day, model) accumulator.
// The insert supplies fresh counters; on conflict the stored row's counters
// grow by the inserted amounts (EXCLUDED.*) while id/created semantics of the
// original row are kept. The conflict target is the migration's
// UNIQUE(tenant_id, day, model_id).
func (r *modelGatewayRepository) UpsertUsage(
	ctx context.Context, day time.Time, tenantID uint64, modelID string,
	promptTokens, completionTokens int64,
) error {
	row := &types.ModelUsageDaily{
		TenantID:         tenantID,
		Day:              day,
		ModelID:          modelID,
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		CallCount:        1,
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "tenant_id"}, {Name: "day"}, {Name: "model_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"prompt_tokens":     gorm.Expr("model_usage_daily.prompt_tokens + EXCLUDED.prompt_tokens"),
			"completion_tokens": gorm.Expr("model_usage_daily.completion_tokens + EXCLUDED.completion_tokens"),
			"call_count":        gorm.Expr("model_usage_daily.call_count + EXCLUDED.call_count"),
		}),
	}).Create(row).Error
}

// ListUsage returns the accumulator rows for one tenant since (and including)
// the given day, day ascending then model, ready for the summary pass.
func (r *modelGatewayRepository) ListUsage(
	ctx context.Context, tenantID uint64, sinceDay time.Time,
) ([]*types.ModelUsageDaily, error) {
	var rows []*types.ModelUsageDaily
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND day >= ?", tenantID, sinceDay).
		Order("day ASC, model_id ASC").
		Find(&rows).Error
	return rows, err
}
