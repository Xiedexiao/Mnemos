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

// ErrExternalSubscriptionNotFound is returned when a feed lookup misses.
var ErrExternalSubscriptionNotFound = errors.New("external subscription: feed not found")

// externalFeedSweepScanLimit bounds how many enabled feeds one scheduler pass
// examines. Due-ness is evaluated in Go because interval arithmetic on
// timestamps differs between PostgreSQL, MySQL and SQLite; scanning a bounded
// set and filtering in memory keeps the query portable.
const externalFeedSweepScanLimit = 500

// externalSubscriptionRepository implements
// interfaces.ExternalSubscriptionRepository (KnowAxis V3.0 §7.3).
type externalSubscriptionRepository struct {
	db *gorm.DB
}

// NewExternalSubscriptionRepository constructs the repository.
func NewExternalSubscriptionRepository(db *gorm.DB) interfaces.ExternalSubscriptionRepository {
	return &externalSubscriptionRepository{db: db}
}

// CreateFeed inserts one subscription.
func (r *externalSubscriptionRepository) CreateFeed(
	ctx context.Context, feed *types.ExternalSubscriptionFeed,
) error {
	return r.db.WithContext(ctx).Create(feed).Error
}

// GetFeed returns one subscription in the tenant.
func (r *externalSubscriptionRepository) GetFeed(
	ctx context.Context, tenantID uint64, feedID string,
) (*types.ExternalSubscriptionFeed, error) {
	var feed types.ExternalSubscriptionFeed
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, feedID).
		First(&feed).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrExternalSubscriptionNotFound
	}
	if err != nil {
		return nil, err
	}
	return &feed, nil
}

// ListFeeds returns subscriptions, optionally only enabled ones.
func (r *externalSubscriptionRepository) ListFeeds(
	ctx context.Context, tenantID uint64, enabledOnly bool,
) ([]*types.ExternalSubscriptionFeed, error) {
	query := r.db.WithContext(ctx).Where("tenant_id = ?", tenantID)
	if enabledOnly {
		query = query.Where("enabled = ?", true)
	}
	var feeds []*types.ExternalSubscriptionFeed
	if err := query.Order("created_at ASC").Find(&feeds).Error; err != nil {
		return nil, err
	}
	return feeds, nil
}

// ListDueFeeds returns enabled feeds whose interval has elapsed.
func (r *externalSubscriptionRepository) ListDueFeeds(
	ctx context.Context, now time.Time, limit int,
) ([]*types.ExternalSubscriptionFeed, error) {
	if limit <= 0 || limit > externalFeedSweepScanLimit {
		limit = externalFeedSweepScanLimit
	}
	var feeds []*types.ExternalSubscriptionFeed
	// CASE 排序而不是 NULLS FIRST：后者在 MySQL 与 SQLite 上都不可用。
	err := r.db.WithContext(ctx).
		Where("enabled = ?", true).
		Order("CASE WHEN last_checked_at IS NULL THEN 0 ELSE 1 END, last_checked_at ASC").
		Limit(limit).
		Find(&feeds).Error
	if err != nil {
		return nil, err
	}
	due := make([]*types.ExternalSubscriptionFeed, 0, len(feeds))
	for _, feed := range feeds {
		if feed.Due(now) {
			due = append(due, feed)
		}
	}
	return due, nil
}

// UpdateFeed persists the editable fields and check state.
func (r *externalSubscriptionRepository) UpdateFeed(
	ctx context.Context, feed *types.ExternalSubscriptionFeed,
) error {
	result := r.db.WithContext(ctx).Model(&types.ExternalSubscriptionFeed{}).
		Where("tenant_id = ? AND id = ?", feed.TenantID, feed.ID).
		Updates(map[string]interface{}{
			"name":                 feed.Name,
			"feed_url":             feed.FeedURL,
			"format":               feed.Format,
			"domain_tags":          feed.DomainTags,
			"knowledge_base_ids":   feed.KnowledgeBaseIDs,
			"enabled":              feed.Enabled,
			"check_interval_hours": feed.CheckIntervalHours,
			"last_checked_at":      feed.LastCheckedAt,
			"last_success_at":      feed.LastSuccessAt,
			"last_error":           feed.LastError,
			"auth_header_enc":      feed.AuthHeaderEnc,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrExternalSubscriptionNotFound
	}
	return nil
}

// DeleteFeed removes a subscription and its entry history.
func (r *externalSubscriptionRepository) DeleteFeed(
	ctx context.Context, tenantID uint64, feedID string,
) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("tenant_id = ? AND feed_id = ?", tenantID, feedID).
			Delete(&types.ExternalSubscriptionEntry{}).Error; err != nil {
			return err
		}
		result := tx.Where("tenant_id = ? AND id = ?", tenantID, feedID).
			Delete(&types.ExternalSubscriptionFeed{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrExternalSubscriptionNotFound
		}
		return nil
	})
}

// RecordEntry inserts an entry, ignoring a duplicate fingerprint.
func (r *externalSubscriptionRepository) RecordEntry(
	ctx context.Context, entry *types.ExternalSubscriptionEntry,
) (bool, error) {
	result := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(entry)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// UpdateEntryOutcome records how many knowledge items and signals an entry
// produced.
func (r *externalSubscriptionRepository) UpdateEntryOutcome(
	ctx context.Context, entryID string, matchedKnowledge, signals int,
) error {
	return r.db.WithContext(ctx).Model(&types.ExternalSubscriptionEntry{}).
		Where("id = ?", entryID).
		Updates(map[string]interface{}{
			"matched_knowledge_count": matchedKnowledge,
			"signal_count":            signals,
		}).Error
}

// DeleteEntry removes an entry record.
func (r *externalSubscriptionRepository) DeleteEntry(ctx context.Context, entryID string) error {
	return r.db.WithContext(ctx).
		Where("id = ?", entryID).
		Delete(&types.ExternalSubscriptionEntry{}).Error
}

// ListEntries returns the newest entries of a feed.
func (r *externalSubscriptionRepository) ListEntries(
	ctx context.Context, tenantID uint64, feedID string, limit int,
) ([]*types.ExternalSubscriptionEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var entries []*types.ExternalSubscriptionEntry
	query := r.db.WithContext(ctx).Where("tenant_id = ?", tenantID)
	if strings.TrimSpace(feedID) != "" {
		query = query.Where("feed_id = ?", feedID)
	}
	if err := query.Order("created_at DESC").Limit(limit).Find(&entries).Error; err != nil {
		return nil, err
	}
	return entries, nil
}

// MarkFeedChecked records the outcome of one check.
func (r *externalSubscriptionRepository) MarkFeedChecked(
	ctx context.Context, tenantID uint64, feedID string, checkedAt time.Time, syncErr error,
) error {
	updates := map[string]interface{}{
		"last_checked_at": checkedAt,
	}
	if syncErr != nil {
		updates["last_error"] = syncErr.Error()
	} else {
		updates["last_error"] = ""
		updates["last_success_at"] = checkedAt
	}
	return r.db.WithContext(ctx).Model(&types.ExternalSubscriptionFeed{}).
		Where("tenant_id = ? AND id = ?", tenantID, feedID).
		Updates(updates).Error
}

// MatchKnowledgeByTags returns knowledge items carrying any of the tag names.
//
// 匹配规则写在 SQL 里而不是逐条加载：合规复审不能悄悄漏掉文档。标签名比较
// 不区分大小写；已删除、已下架的知识不参与匹配（它们不会进入问答，也就不
// 需要复审）。
func (r *externalSubscriptionRepository) MatchKnowledgeByTags(
	ctx context.Context, tenantID uint64, tagNames []string, knowledgeBaseIDs []string,
) ([]types.ExternalSubscriptionMatchedKnowledge, error) {
	cleaned := types.NormalizeTagNames(tagNames)
	if len(cleaned) == 0 {
		return nil, nil
	}
	lowered := make([]string, 0, len(cleaned))
	for _, name := range cleaned {
		lowered = append(lowered, strings.ToLower(name))
	}

	query := r.db.WithContext(ctx).
		Table("knowledges AS k").
		Select("k.id AS knowledge_id, k.knowledge_base_id, k.title, MIN(t.name) AS tag_name").
		Joins("JOIN knowledge_tag_relations r ON r.knowledge_id = k.id").
		Joins("JOIN knowledge_tags t ON t.id = r.tag_id").
		Where("k.tenant_id = ? AND k.deleted_at IS NULL", tenantID).
		Where("t.tenant_id = ?", tenantID).
		Where("LOWER(t.name) IN ?", lowered).
		Group("k.id, k.knowledge_base_id, k.title").
		Order("k.id ASC").
		Limit(2000)
	if len(knowledgeBaseIDs) > 0 {
		query = query.Where("k.knowledge_base_id IN ?", knowledgeBaseIDs)
	}
	var rows []types.ExternalSubscriptionMatchedKnowledge
	if err := query.Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}
