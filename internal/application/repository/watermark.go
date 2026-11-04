package repository

import (
	"context"

	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// watermarkRepository implements interfaces.WatermarkRepository
// (KnowAxis V3.0 §16.3 水印留痕).
type watermarkRepository struct {
	db *gorm.DB
}

// NewWatermarkRepository constructs the repository.
func NewWatermarkRepository(db *gorm.DB) interfaces.WatermarkRepository {
	return &watermarkRepository{db: db}
}

// Create appends one record.
func (r *watermarkRepository) Create(
	ctx context.Context, record *types.WatermarkRecord,
) error {
	return r.db.WithContext(ctx).Create(record).Error
}

// List returns the tenant's records, newest first.
func (r *watermarkRepository) List(
	ctx context.Context, tenantID uint64, knowledgeID, userID string, limit int,
) ([]*types.WatermarkRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := r.db.WithContext(ctx).Where("tenant_id = ?", tenantID)
	if knowledgeID != "" {
		query = query.Where("knowledge_id = ?", knowledgeID)
	}
	if userID != "" {
		query = query.Where("user_id = ?", userID)
	}
	var rows []*types.WatermarkRecord
	if err := query.Order("created_at DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}
