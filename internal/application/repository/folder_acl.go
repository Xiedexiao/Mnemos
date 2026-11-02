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

// ErrFolderACLNotFound is returned when a folder grant lookup misses.
var ErrFolderACLNotFound = errors.New("folder acl not found")

// folderACLRepository implements interfaces.FolderACLRepository.
type folderACLRepository struct {
	db *gorm.DB
}

// NewFolderACLRepository constructs the folder ACL repository.
func NewFolderACLRepository(db *gorm.DB) interfaces.FolderACLRepository {
	return &folderACLRepository{db: db}
}

// UpsertGrant creates or updates one folder grant.
func (r *folderACLRepository) UpsertGrant(
	ctx context.Context, grant *types.FolderACL,
) error {
	if grant == nil {
		return errors.New("folder acl: grant is required")
	}
	return r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "knowledge_base_id"}, {Name: "folder_path"},
				{Name: "subject_type"}, {Name: "subject_id"}, {Name: "action"},
			},
			DoUpdates: clause.AssignmentColumns([]string{
				"effect", "inherit_children", "granted_by", "expires_at",
			}),
		}).
		Create(grant).Error
}

// ListByKnowledgeBase returns every folder grant of one space.
func (r *folderACLRepository) ListByKnowledgeBase(
	ctx context.Context, tenantID uint64, kbID string,
) ([]*types.FolderACL, error) {
	var grants []*types.FolderACL
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, kbID).
		Order("folder_path ASC, created_at ASC").
		Find(&grants).Error
	return grants, err
}

// ListForFolder returns the grants that apply to one folder path (its own plus
// ancestors), so the caller can fold them without loading the whole space.
func (r *folderACLRepository) ListForFolder(
	ctx context.Context, tenantID uint64, kbID, folderPath string,
) ([]*types.FolderACL, error) {
	// 祖先路径集合：""、"a"、"a/b" for "a/b/c"。用 IN 精确取，避免用 LIKE
	// 前缀把所有子目录授权也拉进来。
	paths := FolderAncestorPaths(folderPath)
	var grants []*types.FolderACL
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_base_id = ? AND folder_path IN ?",
			tenantID, kbID, paths).
		Find(&grants).Error
	return grants, err
}

// FolderAncestorPaths returns the folder and all its ancestors, root last.
func FolderAncestorPaths(folderPath string) []string {
	normalized := types.NormalizeKnowledgeFolderPath(folderPath)
	paths := []string{normalized}
	for normalized != "" {
		index := lastIndexByte(normalized, '/')
		if index < 0 {
			normalized = ""
		} else {
			normalized = normalized[:index]
		}
		paths = append(paths, normalized)
	}
	return paths
}

// lastIndexByte is strings.LastIndexByte without the import cycle risk.
func lastIndexByte(s string, target byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == target {
			return i
		}
	}
	return -1
}

// Revoke deletes one folder grant within the tenant.
func (r *folderACLRepository) Revoke(
	ctx context.Context, tenantID uint64, grantID string,
) error {
	return r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, grantID).
		Delete(&types.FolderACL{}).Error
}

// DeleteByFolder removes every grant attached to one folder path.
func (r *folderACLRepository) DeleteByFolder(
	ctx context.Context, tenantID uint64, kbID, folderPath string,
) (int64, error) {
	result := r.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_base_id = ? AND folder_path = ?",
			tenantID, kbID, types.NormalizeKnowledgeFolderPath(folderPath)).
		Delete(&types.FolderACL{})
	return result.RowsAffected, result.Error
}

// RecordCeilingViolation appends one governance finding.
func (r *folderACLRepository) RecordCeilingViolation(
	ctx context.Context, violation *types.ACLCeilingViolation,
) error {
	if violation.DetectedAt.IsZero() {
		violation.DetectedAt = time.Now()
	}
	return r.db.WithContext(ctx).Create(violation).Error
}

// ListCeilingViolations returns the governance findings of one tenant.
func (r *folderACLRepository) ListCeilingViolations(
	ctx context.Context, tenantID uint64, limit int,
) ([]*types.ACLCeilingViolation, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	var rows []*types.ACLCeilingViolation
	err := r.db.WithContext(ctx).
		Where("tenant_id = ?", tenantID).
		Order("detected_at DESC").
		Limit(limit).
		Find(&rows).Error
	return rows, err
}
