package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// ErrSubscriptionNotFound is returned when a subscription lookup misses.
var ErrSubscriptionNotFound = errors.New("subscription not found")

// subscriptionRepository implements interfaces.SubscriptionRepository
// (KnowAxis V3.0 §15.3 我的关注).
type subscriptionRepository struct {
	db *gorm.DB
}

// NewSubscriptionRepository constructs the repository.
func NewSubscriptionRepository(db *gorm.DB) interfaces.SubscriptionRepository {
	return &subscriptionRepository{db: db}
}

// Upsert inserts or re-enables one subscription.
func (r *subscriptionRepository) Upsert(
	ctx context.Context, subscription *types.Subscription,
) (bool, error) {
	var created bool
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing types.Subscription
		err := tx.Where("tenant_id = ? AND user_id = ? AND subject_type = ? AND subject_id = ?",
			subscription.TenantID, subscription.UserID,
			subscription.SubjectType, subscription.SubjectID).
			First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			created = true
			return tx.Create(subscription).Error
		}
		if err != nil {
			return err
		}
		// 重复订阅=重新启用，并把标签/空间信息刷新到最新。
		subscription.ID = existing.ID
		return tx.Model(&types.Subscription{}).
			Where("id = ?", existing.ID).
			Updates(map[string]interface{}{
				"enabled":    true,
				"label":      subscription.Label,
				"space_id":   subscription.SpaceID,
				"updated_at": time.Now().UTC(),
			}).Error
	})
	return created, err
}

// List returns the user's subscriptions.
func (r *subscriptionRepository) List(
	ctx context.Context, tenantID uint64, userID string, enabledOnly bool,
) ([]*types.Subscription, error) {
	query := r.db.WithContext(ctx).
		Where("tenant_id = ? AND user_id = ?", tenantID, userID)
	if enabledOnly {
		query = query.Where("enabled = ?", true)
	}
	var subs []*types.Subscription
	if err := query.Order("created_at DESC").Find(&subs).Error; err != nil {
		return nil, err
	}
	return subs, nil
}

// Get returns one subscription.
func (r *subscriptionRepository) Get(
	ctx context.Context, tenantID uint64, userID, subscriptionID string,
) (*types.Subscription, error) {
	var subscription types.Subscription
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND user_id = ? AND id = ?", tenantID, userID, subscriptionID).
		First(&subscription).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrSubscriptionNotFound
	}
	if err != nil {
		return nil, err
	}
	return &subscription, nil
}

// SetEnabled toggles one subscription.
func (r *subscriptionRepository) SetEnabled(
	ctx context.Context, tenantID uint64, userID, subscriptionID string, enabled bool,
) error {
	result := r.db.WithContext(ctx).Model(&types.Subscription{}).
		Where("tenant_id = ? AND user_id = ? AND id = ?", tenantID, userID, subscriptionID).
		Updates(map[string]interface{}{"enabled": enabled, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrSubscriptionNotFound
	}
	return nil
}

// Delete removes one subscription.
func (r *subscriptionRepository) Delete(
	ctx context.Context, tenantID uint64, userID, subscriptionID string,
) error {
	result := r.db.WithContext(ctx).
		Where("tenant_id = ? AND user_id = ? AND id = ?", tenantID, userID, subscriptionID).
		Delete(&types.Subscription{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrSubscriptionNotFound
	}
	return nil
}

// ListDigestSources returns every enabled subscription in the tenant.
func (r *subscriptionRepository) ListDigestSources(
	ctx context.Context, tenantID uint64,
) ([]*types.Subscription, error) {
	var subs []*types.Subscription
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND enabled = ?", tenantID, true).
		Find(&subs).Error; err != nil {
		return nil, err
	}
	return subs, nil
}

// digestColumns is the shared projection of a change row.
const digestColumns = "k.id AS knowledge_id, k.knowledge_base_id, k.title, " +
	"COALESCE(NULLIF(k.lifecycle_status, ''), 'valid') AS lifecycle_status, k.updated_at AS changed_at"

// digestQuery applies the shared visibility + window filters.
func digestQuery(query *gorm.DB, tenantID uint64, since time.Time) *gorm.DB {
	return query.Where("k.tenant_id = ? AND k.deleted_at IS NULL", tenantID).
		Where("k.updated_at > ?", since)
}

// ChangesSince returns documents that changed in one space.
func (r *subscriptionRepository) ChangesSince(
	ctx context.Context, tenantID uint64, spaceID string, since time.Time, limit int,
) ([]types.DigestItem, error) {
	if spaceID == "" {
		return nil, nil
	}
	if limit <= 0 || limit > types.DigestMaxItems {
		limit = types.DigestMaxItems
	}
	var items []types.DigestItem
	query := r.db.WithContext(ctx).Table("knowledges AS k").
		Select(digestColumns).
		Where("k.knowledge_base_id = ?", spaceID)
	if err := digestQuery(query, tenantID, since).
		Order("k.updated_at DESC").Limit(limit).Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// TagChangesSince returns documents carrying one tag.
func (r *subscriptionRepository) TagChangesSince(
	ctx context.Context, tenantID uint64, tagName string, since time.Time, limit int,
) ([]types.DigestItem, error) {
	if tagName == "" {
		return nil, nil
	}
	if limit <= 0 || limit > types.DigestMaxItems {
		limit = types.DigestMaxItems
	}
	var items []types.DigestItem
	query := r.db.WithContext(ctx).Table("knowledges AS k").
		Select(digestColumns).
		Joins("JOIN knowledge_tag_relations r ON r.knowledge_id = k.id").
		Joins("JOIN knowledge_tags t ON t.id = r.tag_id").
		Where("t.tenant_id = ? AND LOWER(t.name) = ?", tenantID, tagName)
	if err := digestQuery(query, tenantID, since).
		Group(digestColumns).Order("k.updated_at DESC").Limit(limit).Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// AuthorChangesSince returns documents published by one user.
//
// 作者信息来自 knowledge_versions.published_by：知识主表不存作者，"谁发布
// 的这一版"才是可订阅的事实。没有版本行的知识（从未发布）不属于任何作者
// 的更新。
func (r *subscriptionRepository) AuthorChangesSince(
	ctx context.Context, tenantID uint64, authorID string, since time.Time, limit int,
) ([]types.DigestItem, error) {
	if authorID == "" {
		return nil, nil
	}
	if limit <= 0 || limit > types.DigestMaxItems {
		limit = types.DigestMaxItems
	}
	var items []types.DigestItem
	query := r.db.WithContext(ctx).Table("knowledges AS k").
		Select(digestColumns).
		Joins("JOIN knowledge_versions v ON v.knowledge_id = k.id").
		Where("v.tenant_id = ? AND v.published_by = ?", tenantID, authorID)
	if err := digestQuery(query, tenantID, since).
		Group(digestColumns).Order("k.updated_at DESC").Limit(limit).Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// KnowledgeChangesSince returns the change rows of one document.
//
// 单篇订阅关心的是"这一篇又变了"：只要它在本窗口内更新过就产生一条，
// 没有更新则为空，而不是把文档本身当成一次更新。
func (r *subscriptionRepository) KnowledgeChangesSince(
	ctx context.Context, tenantID uint64, knowledgeID string, since time.Time,
) ([]types.DigestItem, error) {
	if knowledgeID == "" {
		return nil, nil
	}
	var items []types.DigestItem
	query := r.db.WithContext(ctx).Table("knowledges AS k").
		Select(digestColumns).
		Where("k.id = ?", knowledgeID)
	if err := digestQuery(query, tenantID, since).Limit(1).Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// FolderPaths returns id → folder_path for the given documents.
func (r *subscriptionRepository) FolderPaths(
	ctx context.Context, tenantID uint64, knowledgeIDs []string,
) (map[string]string, error) {
	out := make(map[string]string, len(knowledgeIDs))
	cleaned := types.NormalizeKnowledgeIDs(knowledgeIDs)
	if len(cleaned) == 0 {
		return out, nil
	}
	type row struct {
		ID         string
		FolderPath string
	}
	var rows []row
	if err := r.db.WithContext(ctx).Table("knowledges").
		Select("id, folder_path").
		Where("tenant_id = ? AND id IN ?", tenantID, cleaned).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, entry := range rows {
		out[entry.ID] = entry.FolderPath
	}
	return out, nil
}

// MarkDigested stamps the subscriptions with the digest time.
func (r *subscriptionRepository) MarkDigested(
	ctx context.Context, tenantID uint64, subscriptionIDs []string, at time.Time,
) error {
	if len(subscriptionIDs) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Model(&types.Subscription{}).
		Where("tenant_id = ? AND id IN ?", tenantID, subscriptionIDs).
		Update("last_digest_at", at).Error
}

// compile-time guard.
var _ interfaces.SubscriptionRepository = (*subscriptionRepository)(nil)
