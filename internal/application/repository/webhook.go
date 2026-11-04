package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// ErrWebhookEndpointNotFound is returned when an endpoint lookup misses.
var ErrWebhookEndpointNotFound = errors.New("webhook endpoint not found")

// ErrWebhookDeliveryNotFound is returned when a delivery lookup misses.
var ErrWebhookDeliveryNotFound = errors.New("webhook delivery not found")

// webhookRepository implements interfaces.WebhookRepository
// (KnowAxis V3.0 §16.4).
type webhookRepository struct {
	db *gorm.DB
}

// NewWebhookRepository constructs the repository.
func NewWebhookRepository(db *gorm.DB) interfaces.WebhookRepository {
	return &webhookRepository{db: db}
}

// CreateEndpoint inserts one subscriber.
func (r *webhookRepository) CreateEndpoint(
	ctx context.Context, endpoint *types.WebhookEndpoint,
) error {
	return r.db.WithContext(ctx).Create(endpoint).Error
}

// GetEndpoint returns one endpoint in the tenant.
func (r *webhookRepository) GetEndpoint(
	ctx context.Context, tenantID uint64, endpointID string,
) (*types.WebhookEndpoint, error) {
	var endpoint types.WebhookEndpoint
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, endpointID).
		First(&endpoint).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrWebhookEndpointNotFound
	}
	if err != nil {
		return nil, err
	}
	return &endpoint, nil
}

// ListEndpoints returns the tenant's endpoints.
func (r *webhookRepository) ListEndpoints(
	ctx context.Context, tenantID uint64,
) ([]*types.WebhookEndpoint, error) {
	var rows []*types.WebhookEndpoint
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ?", tenantID).
		Order("created_at ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// ListSubscribedEndpoints returns the enabled endpoints subscribed to an event.
//
// 事件类型存在 JSON 数组里，这里在 Go 侧过滤而不是写 JSON 查询：不同数据库
// 的 JSON 数组成员查询语法不同（jsonb ? vs JSON_CONTAINS），而端点数量是
// 个位数，扫一遍没有成本。
func (r *webhookRepository) ListSubscribedEndpoints(
	ctx context.Context, tenantID uint64, eventType types.WebhookEventType,
) ([]*types.WebhookEndpoint, error) {
	rows, err := r.ListEndpoints(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	out := make([]*types.WebhookEndpoint, 0, len(rows))
	for _, endpoint := range rows {
		if endpoint == nil || !endpoint.Enabled {
			continue
		}
		if !endpoint.Subscribes(eventType) {
			continue
		}
		out = append(out, endpoint)
	}
	return out, nil
}

// UpdateEndpoint persists the mutable fields.
func (r *webhookRepository) UpdateEndpoint(
	ctx context.Context, endpoint *types.WebhookEndpoint,
) error {
	result := r.db.WithContext(ctx).Model(&types.WebhookEndpoint{}).
		Where("tenant_id = ? AND id = ?", endpoint.TenantID, endpoint.ID).
		Updates(map[string]interface{}{
			"url":         endpoint.URL,
			"description": endpoint.Description,
			"events":      endpoint.Events,
			"enabled":     endpoint.Enabled,
			"secret_enc":  endpoint.SecretEnc,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrWebhookEndpointNotFound
	}
	return nil
}

// DeleteEndpoint removes one endpoint and its undelivered rows.
func (r *webhookRepository) DeleteEndpoint(
	ctx context.Context, tenantID uint64, endpointID string,
) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("tenant_id = ? AND endpoint_id = ? AND state = ?",
			tenantID, endpointID, string(types.WebhookDeliveryPending)).
			Delete(&types.WebhookDelivery{}).Error; err != nil {
			return err
		}
		result := tx.Where("tenant_id = ? AND id = ?", tenantID, endpointID).
			Delete(&types.WebhookEndpoint{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrWebhookEndpointNotFound
		}
		return nil
	})
}

// RecordEndpointOutcome updates the failure streak and last attempt result.
func (r *webhookRepository) RecordEndpointOutcome(
	ctx context.Context, tenantID uint64, endpointID string,
	success bool, statusCode int, failure string, at time.Time,
) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var endpoint types.WebhookEndpoint
		if err := tx.Where("tenant_id = ? AND id = ?", tenantID, endpointID).
			First(&endpoint).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrWebhookEndpointNotFound
			}
			return err
		}
		updates := map[string]interface{}{}
		if success {
			updates["failure_streak"] = 0
			updates["last_error"] = ""
			updates["last_success_at"] = at
		} else {
			streak := endpoint.FailureStreak + 1
			updates["failure_streak"] = streak
			updates["last_error"] = failure
			// 连续失败到阈值就自动停用：一个已经下线的接收方不应该继续消耗
			// 重试预算，也不应该让运维在日志里持续看到同一条错误。
			if streak >= types.WebhookFailureStreakLimit {
				updates["enabled"] = false
			}
		}
		return tx.Model(&types.WebhookEndpoint{}).
			Where("tenant_id = ? AND id = ?", tenantID, endpointID).
			Updates(updates).Error
	})
}

// Enqueue inserts one outbox row.
func (r *webhookRepository) Enqueue(ctx context.Context, delivery *types.WebhookDelivery) error {
	return r.db.WithContext(ctx).Create(delivery).Error
}

// ListDueDeliveries returns pending rows whose next attempt is due.
func (r *webhookRepository) ListDueDeliveries(
	ctx context.Context, now time.Time, limit int,
) ([]*types.WebhookDelivery, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var rows []*types.WebhookDelivery
	if err := r.db.WithContext(ctx).
		Where("state = ? AND next_attempt_at <= ?", string(types.WebhookDeliveryPending), now.UTC()).
		Order("next_attempt_at ASC").
		Limit(limit).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// MarkDelivered closes one row.
func (r *webhookRepository) MarkDelivered(
	ctx context.Context, deliveryID string, statusCode int, at time.Time,
) error {
	return r.db.WithContext(ctx).Model(&types.WebhookDelivery{}).
		Where("id = ?", deliveryID).
		Updates(map[string]interface{}{
			"state":            string(types.WebhookDeliveryDelivered),
			"delivered_at":     at,
			"last_status_code": statusCode,
			"last_error":       "",
			"attempts":         gorm.Expr("attempts + 1"),
		}).Error
}

// MarkAttemptFailed records a failed attempt.
func (r *webhookRepository) MarkAttemptFailed(
	ctx context.Context, deliveryID string, attempts int,
	statusCode int, failure string, nextAttemptAt time.Time, dead bool,
) error {
	state := types.WebhookDeliveryPending
	if dead {
		state = types.WebhookDeliveryDead
	}
	return r.db.WithContext(ctx).Model(&types.WebhookDelivery{}).
		Where("id = ?", deliveryID).
		Updates(map[string]interface{}{
			"state":            string(state),
			"attempts":         attempts,
			"last_status_code": statusCode,
			"last_error":       failure,
			"next_attempt_at":  nextAttemptAt.UTC(),
		}).Error
}

// ListDeliveries returns the tenant's outbox rows.
func (r *webhookRepository) ListDeliveries(
	ctx context.Context, tenantID uint64, endpointID string,
	state types.WebhookDeliveryState, limit int,
) ([]*types.WebhookDelivery, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := r.db.WithContext(ctx).Where("tenant_id = ?", tenantID)
	if endpointID != "" {
		query = query.Where("endpoint_id = ?", endpointID)
	}
	if state != "" {
		query = query.Where("state = ?", string(state))
	}
	var rows []*types.WebhookDelivery
	if err := query.Order("created_at DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// Requeue revives a dead row for one more attempt.
func (r *webhookRepository) Requeue(
	ctx context.Context, tenantID uint64, deliveryID string, at time.Time,
) error {
	result := r.db.WithContext(ctx).Model(&types.WebhookDelivery{}).
		Where("tenant_id = ? AND id = ? AND state = ?",
			tenantID, deliveryID, string(types.WebhookDeliveryDead)).
		Updates(map[string]interface{}{
			"state":            string(types.WebhookDeliveryPending),
			"attempts":         0,
			"next_attempt_at":  at.UTC(),
			"last_error":       "",
			"last_status_code": 0,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrWebhookDeliveryNotFound
	}
	return nil
}
