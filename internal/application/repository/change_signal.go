package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// ErrChangeSignalNotFound is returned when a signal lookup misses.
var ErrChangeSignalNotFound = errors.New("change signal not found")

// changeSignalRepository implements interfaces.ChangeSignalRepository
// (KnowAxis V3.0 §7.3).
type changeSignalRepository struct {
	db *gorm.DB
}

// NewChangeSignalRepository constructs the repository.
func NewChangeSignalRepository(db *gorm.DB) interfaces.ChangeSignalRepository {
	return &changeSignalRepository{db: db}
}

// Create inserts one signal, silently ignoring a duplicate pending signal.
//
// ON CONFLICT DO NOTHING against the partial unique index is what keeps a
// recurring connector failure from appending a row per sync: the collector can
// re-report freely, the queue stays one row per real issue.
func (r *changeSignalRepository) Create(
	ctx context.Context, signal *types.ChangeSignal,
) (bool, error) {
	result := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(signal)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// List returns signals of one tenant, optionally filtered.
func (r *changeSignalRepository) List(
	ctx context.Context, tenantID uint64, status types.ChangeSignalStatus,
	signalType types.ChangeSignalType, limit int,
) ([]*types.ChangeSignal, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	query := r.db.WithContext(ctx).Where("tenant_id = ?", tenantID)
	if status != "" {
		query = query.Where("status = ?", string(status))
	}
	if signalType != "" {
		query = query.Where("signal_type = ?", string(signalType))
	}
	var rows []*types.ChangeSignal
	err := query.Order("created_at DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

// Get returns one signal within the tenant.
func (r *changeSignalRepository) Get(
	ctx context.Context, tenantID uint64, signalID string,
) (*types.ChangeSignal, error) {
	var signal types.ChangeSignal
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, signalID).
		First(&signal).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrChangeSignalNotFound
	}
	if err != nil {
		return nil, err
	}
	return &signal, nil
}

// MarkConverted records that a signal produced a change request.
func (r *changeSignalRepository) MarkConverted(
	ctx context.Context, tenantID uint64, signalID, changeRequestID, actorID string,
) (*types.ChangeSignal, error) {
	if err := r.db.WithContext(ctx).
		Model(&types.ChangeSignal{}).
		Where("tenant_id = ? AND id = ?", tenantID, signalID).
		Updates(map[string]interface{}{
			"status":            string(types.ChangeSignalConverted),
			"change_request_id": changeRequestID,
			"handled_by":        actorID,
			"updated_at":        time.Now(),
		}).Error; err != nil {
		return nil, err
	}
	return r.Get(ctx, tenantID, signalID)
}

// Dismiss marks a signal as a false positive.
func (r *changeSignalRepository) Dismiss(
	ctx context.Context, tenantID uint64, signalID, actorID string,
) (*types.ChangeSignal, error) {
	if err := r.db.WithContext(ctx).
		Model(&types.ChangeSignal{}).
		Where("tenant_id = ? AND id = ?", tenantID, signalID).
		Updates(map[string]interface{}{
			"status":     string(types.ChangeSignalDismissed),
			"handled_by": actorID,
			"updated_at": time.Now(),
		}).Error; err != nil {
		return nil, err
	}
	return r.Get(ctx, tenantID, signalID)
}

// CountPending returns how many signals still need triage.
func (r *changeSignalRepository) CountPending(
	ctx context.Context, tenantID uint64,
) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&types.ChangeSignal{}).
		Where("tenant_id = ? AND status = ?", tenantID, string(types.ChangeSignalPending)).
		Count(&count).Error
	return count, err
}
