package repository

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// ErrEmbedPushRuleNotFound is returned when a rule lookup misses.
var ErrEmbedPushRuleNotFound = errors.New("embed push rule not found")

// embedPushRepository implements interfaces.EmbedPushRepository
// (KnowAxis V3.0 §15.5 嵌入式推送).
type embedPushRepository struct {
	db *gorm.DB
}

// NewEmbedPushRepository constructs the repository.
func NewEmbedPushRepository(db *gorm.DB) interfaces.EmbedPushRepository {
	return &embedPushRepository{db: db}
}

// CreateRule inserts one push rule.
func (r *embedPushRepository) CreateRule(ctx context.Context, rule *types.EmbedPushRule) error {
	return r.db.WithContext(ctx).Create(rule).Error
}

// GetRule returns one rule in the tenant.
func (r *embedPushRepository) GetRule(
	ctx context.Context, tenantID uint64, ruleID string,
) (*types.EmbedPushRule, error) {
	var rule types.EmbedPushRule
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, ruleID).
		First(&rule).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrEmbedPushRuleNotFound
	}
	if err != nil {
		return nil, err
	}
	return &rule, nil
}

// ListRules returns the tenant's rules ordered by priority DESC.
func (r *embedPushRepository) ListRules(
	ctx context.Context, tenantID uint64, enabledOnly bool,
) ([]*types.EmbedPushRule, error) {
	query := r.db.WithContext(ctx).Where("tenant_id = ?", tenantID)
	if enabledOnly {
		query = query.Where("enabled = ?", true)
	}
	var rules []*types.EmbedPushRule
	if err := query.Order("priority DESC, created_at ASC").Find(&rules).Error; err != nil {
		return nil, err
	}
	return rules, nil
}

// UpdateRule persists the full mutable state of a loaded rule.
func (r *embedPushRepository) UpdateRule(ctx context.Context, rule *types.EmbedPushRule) error {
	result := r.db.WithContext(ctx).Model(&types.EmbedPushRule{}).
		Where("tenant_id = ? AND id = ?", rule.TenantID, rule.ID).
		Updates(map[string]interface{}{
			"name":          rule.Name,
			"system":        rule.System,
			"scene":         rule.Scene,
			"entity_type":   rule.EntityType,
			"entity_value":  rule.EntityValue,
			"attributes":    rule.Attributes,
			"tag_names":     rule.TagNames,
			"knowledge_ids": rule.KnowledgeIDs,
			"reason":        rule.Reason,
			"priority":      rule.Priority,
			"enabled":       rule.Enabled,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrEmbedPushRuleNotFound
	}
	return nil
}

// DeleteRule removes one rule.
func (r *embedPushRepository) DeleteRule(
	ctx context.Context, tenantID uint64, ruleID string,
) error {
	result := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, ruleID).
		Delete(&types.EmbedPushRule{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrEmbedPushRuleNotFound
	}
	return nil
}

// MatchKnowledgeByTags returns knowledge carrying any of the tag names.
func (r *embedPushRepository) MatchKnowledgeByTags(
	ctx context.Context, tenantID uint64, tagNames []string,
) ([]types.EmbedPushKnowledgeMatch, error) {
	cleaned := make([]string, 0, len(tagNames))
	for _, name := range tagNames {
		trimmed := strings.ToLower(strings.TrimSpace(name))
		if trimmed != "" {
			cleaned = append(cleaned, trimmed)
		}
	}
	if len(cleaned) == 0 {
		return nil, nil
	}
	var rows []types.EmbedPushKnowledgeMatch
	err := r.db.WithContext(ctx).
		Table("knowledges AS k").
		Select("k.id AS knowledge_id, k.knowledge_base_id, MIN(t.name) AS tag_name").
		Joins("JOIN knowledge_tag_relations r ON r.knowledge_id = k.id").
		Joins("JOIN knowledge_tags t ON t.id = r.tag_id").
		Where("k.tenant_id = ? AND k.deleted_at IS NULL", tenantID).
		Where("t.tenant_id = ?", tenantID).
		Where("LOWER(t.name) IN ?", cleaned).
		Group("k.id, k.knowledge_base_id").
		Order("k.id ASC").
		Limit(500).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// GetPreference returns the user's switch, or (nil, nil) when never set.
func (r *embedPushRepository) GetPreference(
	ctx context.Context, tenantID uint64, userID string,
) (*types.EmbedPushPreference, error) {
	var preference types.EmbedPushPreference
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND user_id = ?", tenantID, userID).
		First(&preference).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &preference, nil
}

// UpsertPreference writes the switch through (tenant, user) uniqueness.
func (r *embedPushRepository) UpsertPreference(
	ctx context.Context, preference *types.EmbedPushPreference,
) error {
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "tenant_id"}, {Name: "user_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"enabled":    preference.Enabled,
			"updated_at": time.Now().UTC(),
		}),
	}).Create(preference).Error
}

// CreateEvent appends one interaction row.
func (r *embedPushRepository) CreateEvent(ctx context.Context, event *types.EmbedPushEvent) error {
	return r.db.WithContext(ctx).Create(event).Error
}

// ListDismissals returns the cards the user dismissed since the given time.
func (r *embedPushRepository) ListDismissals(
	ctx context.Context, tenantID uint64, userID string, since time.Time,
) ([]types.EmbedPushDismissal, error) {
	var rows []types.EmbedPushDismissal
	err := r.db.WithContext(ctx).
		Model(&types.EmbedPushEvent{}).
		Select("rule_id, knowledge_id").
		Where("tenant_id = ? AND user_id = ? AND event_type = ? AND created_at >= ?",
			tenantID, userID, string(types.EmbedPushEventDismiss), since).
		Group("rule_id, knowledge_id").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// RuleStats aggregates impressions/clicks/dismissals per rule since the given
// time.
func (r *embedPushRepository) RuleStats(
	ctx context.Context, tenantID uint64, since time.Time,
) ([]types.EmbedPushRuleStats, error) {
	type row struct {
		RuleID    string
		EventType string
		Count     int64
	}
	var rows []row
	if err := r.db.WithContext(ctx).
		Model(&types.EmbedPushEvent{}).
		Select("rule_id, event_type, COUNT(*) AS count").
		Where("tenant_id = ? AND created_at >= ?", tenantID, since).
		Group("rule_id, event_type").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	stats := make(map[string]*types.EmbedPushRuleStats, len(rows))
	for _, entry := range rows {
		stat, ok := stats[entry.RuleID]
		if !ok {
			stat = &types.EmbedPushRuleStats{RuleID: entry.RuleID}
			stats[entry.RuleID] = stat
		}
		switch types.EmbedPushEventType(entry.EventType) {
		case types.EmbedPushEventImpression:
			stat.Impressions += entry.Count
		case types.EmbedPushEventClick:
			stat.Clicks += entry.Count
		case types.EmbedPushEventDismiss:
			stat.Dismissals += entry.Count
		}
	}
	rules, err := r.ListRules(ctx, tenantID, false)
	if err != nil {
		return nil, err
	}
	// 规则的展示顺序跟规则列表一致，没有事件的规则也要出现（0 值），否则
	// 运维会以为规则不存在。已删除规则留下的历史事件不再展示。
	out := make([]types.EmbedPushRuleStats, 0, len(rules))
	for _, rule := range rules {
		stat := types.EmbedPushRuleStats{RuleID: rule.ID, RuleName: rule.Name}
		if existing, ok := stats[rule.ID]; ok {
			stat = *existing
			stat.RuleName = rule.Name
		}
		out = append(out, stat)
	}
	return out, nil
}
