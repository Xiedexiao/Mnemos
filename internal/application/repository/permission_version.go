package repository

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// permissionVersionRepository implements interfaces.PermissionVersionRepository.
type permissionVersionRepository struct {
	db *gorm.DB
}

// NewPermissionVersionRepository constructs the permission-version repository.
func NewPermissionVersionRepository(db *gorm.DB) interfaces.PermissionVersionRepository {
	return &permissionVersionRepository{db: db}
}

// Bump advances the epoch of one scope and returns the new value.
//
// The upsert is the atomic "read the old value and add one" step: two
// concurrent revocations must not both write the same version, so the
// increment happens in SQL rather than in Go.
func (r *permissionVersionRepository) Bump(
	ctx context.Context, tenantID uint64, scopeType types.PermissionScopeType,
	scopeID, reason string,
) (int64, error) {
	row := &types.PermissionVersion{
		TenantID:   tenantID,
		ScopeType:  string(scopeType),
		ScopeID:    scopeID,
		Version:    1,
		LastReason: reason,
	}
	err := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "tenant_id"}, {Name: "scope_type"}, {Name: "scope_id"},
			},
			DoUpdates: clause.Assignments(map[string]interface{}{
				"version":     gorm.Expr("permission_versions.version + 1"),
				"last_reason": reason,
				"updated_at":  gorm.Expr("NOW()"),
			}),
		}).
		Create(row).Error
	if err != nil {
		return 0, err
	}
	return r.Get(ctx, tenantID, scopeType, scopeID)
}

// Get returns the current epoch of one scope; 0 when the scope was never
// bumped (which is a valid, stable cache key).
func (r *permissionVersionRepository) Get(
	ctx context.Context, tenantID uint64, scopeType types.PermissionScopeType, scopeID string,
) (int64, error) {
	var version int64
	err := r.db.WithContext(ctx).
		Model(&types.PermissionVersion{}).
		Where("tenant_id = ? AND scope_type = ? AND scope_id = ?", tenantID, string(scopeType), scopeID).
		Select("COALESCE(version, 0)").
		Scan(&version).Error
	if err != nil {
		return 0, err
	}
	return version, nil
}

// RecordPropagation appends one outlet outcome row.
func (r *permissionVersionRepository) RecordPropagation(
	ctx context.Context, row *types.RevocationPropagation,
) error {
	return r.db.WithContext(ctx).Create(row).Error
}

// ListPropagationsByKnowledge returns the propagation ledger of one document.
func (r *permissionVersionRepository) ListPropagationsByKnowledge(
	ctx context.Context, tenantID uint64, knowledgeID string, limit int,
) ([]*types.RevocationPropagation, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var rows []*types.RevocationPropagation
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_id = ?", tenantID, knowledgeID).
		Order("created_at DESC").
		Limit(limit).
		Find(&rows).Error
	return rows, err
}
