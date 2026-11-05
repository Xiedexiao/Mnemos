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

// promptRolloutRepository implements interfaces.PromptRolloutRepository
// (KnowAxis V3.0 §16.5 灰度与回滚).
type promptRolloutRepository struct {
	db *gorm.DB
}

// NewPromptRolloutRepository constructs the repository.
func NewPromptRolloutRepository(db *gorm.DB) interfaces.PromptRolloutRepository {
	return &promptRolloutRepository{db: db}
}

// GetPointer returns one template's pointer.
func (r *promptRolloutRepository) GetPointer(
	ctx context.Context, tenantID uint64, templateKey string,
) (*types.PromptVersionPointer, error) {
	var pointer types.PromptVersionPointer
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND template_key = ?", tenantID, templateKey).
		First(&pointer).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &pointer, nil
}

// SavePointer upserts the pointer.
func (r *promptRolloutRepository) SavePointer(
	ctx context.Context, pointer *types.PromptVersionPointer,
) error {
	pointer.UpdatedAt = time.Now().UTC()
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "tenant_id"}, {Name: "template_key"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"stable_version": pointer.StableVersion,
			"canary_version": pointer.CanaryVersion,
			"canary_percent": pointer.CanaryPercent,
			"updated_by":     pointer.UpdatedBy,
			"updated_at":     pointer.UpdatedAt,
		}),
	}).Create(pointer).Error
}

// GetVersion returns one exact version row.
func (r *promptRolloutRepository) GetVersion(
	ctx context.Context, tenantID uint64, templateKey string, version int,
) (*types.PromptVersion, error) {
	var row types.PromptVersion
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND template_key = ? AND version = ?",
			tenantID, templateKey, version).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}
