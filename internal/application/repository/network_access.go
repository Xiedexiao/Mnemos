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

// networkAccessRepository stores the tenant IP allow-list (§16.3) in its own
// table rather than tenant KV: it has a typed shape, it is read on every
// request, and a KV blob would have to be parsed and validated on that path.
type networkAccessRepository struct {
	db *gorm.DB
}

// NewNetworkAccessRepository constructs the repository.
func NewNetworkAccessRepository(db *gorm.DB) interfaces.NetworkAccessRepository {
	return &networkAccessRepository{db: db}
}

// Get returns the stored row, or nil when the tenant never configured one.
func (r *networkAccessRepository) Get(
	ctx context.Context, tenantID uint64,
) (*types.NetworkAccessPolicyRow, error) {
	var row types.NetworkAccessPolicyRow
	err := r.db.WithContext(ctx).
		Where("tenant_id = ?", tenantID).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// Upsert writes the row.
func (r *networkAccessRepository) Upsert(
	ctx context.Context, row *types.NetworkAccessPolicyRow,
) error {
	row.UpdatedAt = time.Now().UTC()
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "tenant_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"allow_entries": row.AllowEntries,
			"enabled":       row.Enabled,
			"updated_by":    row.UpdatedBy,
			"updated_at":    row.UpdatedAt,
		}),
	}).Create(row).Error
}
