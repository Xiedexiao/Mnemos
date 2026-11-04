package repository

import (
	"context"
	"errors"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// ErrACLGroupNotFound is returned when a group lookup misses.
var ErrACLGroupNotFound = errors.New("acl subject group not found")

// aclSubjectGroupRepository implements interfaces.ACLSubjectGroupRepository
// (KnowAxis V3.0 §2.1 group subjects).
type aclSubjectGroupRepository struct {
	db *gorm.DB
}

// NewACLSubjectGroupRepository constructs the repository.
func NewACLSubjectGroupRepository(db *gorm.DB) interfaces.ACLSubjectGroupRepository {
	return &aclSubjectGroupRepository{db: db}
}

// CreateGroup inserts one group.
func (r *aclSubjectGroupRepository) CreateGroup(
	ctx context.Context, group *types.ACLSubjectGroup,
) error {
	return r.db.WithContext(ctx).Create(group).Error
}

// GetGroup returns one group in the tenant.
func (r *aclSubjectGroupRepository) GetGroup(
	ctx context.Context, tenantID uint64, groupID string,
) (*types.ACLSubjectGroup, error) {
	var group types.ACLSubjectGroup
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, groupID).
		First(&group).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrACLGroupNotFound
	}
	if err != nil {
		return nil, err
	}
	return &group, nil
}

// ListGroups returns the tenant's groups with member counts.
func (r *aclSubjectGroupRepository) ListGroups(
	ctx context.Context, tenantID uint64,
) ([]*types.ACLSubjectGroup, error) {
	var groups []*types.ACLSubjectGroup
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ?", tenantID).
		Order("name ASC").
		Find(&groups).Error; err != nil {
		return nil, err
	}
	if len(groups) == 0 {
		return groups, nil
	}
	type countRow struct {
		GroupID string
		Total   int64
	}
	var counts []countRow
	if err := r.db.WithContext(ctx).
		Model(&types.ACLSubjectGroupMember{}).
		Select("group_id, COUNT(*) AS total").
		Where("tenant_id = ?", tenantID).
		Group("group_id").
		Scan(&counts).Error; err != nil {
		return nil, err
	}
	byGroup := make(map[string]int64, len(counts))
	for _, row := range counts {
		byGroup[row.GroupID] = row.Total
	}
	for _, group := range groups {
		group.MemberCount = byGroup[group.ID]
	}
	return groups, nil
}

// UpdateGroup persists the mutable fields.
func (r *aclSubjectGroupRepository) UpdateGroup(
	ctx context.Context, group *types.ACLSubjectGroup,
) error {
	result := r.db.WithContext(ctx).Model(&types.ACLSubjectGroup{}).
		Where("tenant_id = ? AND id = ?", group.TenantID, group.ID).
		Updates(map[string]interface{}{
			"name":        group.Name,
			"description": group.Description,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrACLGroupNotFound
	}
	return nil
}

// DeleteGroup removes one group and its memberships.
func (r *aclSubjectGroupRepository) DeleteGroup(
	ctx context.Context, tenantID uint64, groupID string,
) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("tenant_id = ? AND group_id = ?", tenantID, groupID).
			Delete(&types.ACLSubjectGroupMember{}).Error; err != nil {
			return err
		}
		result := tx.Where("tenant_id = ? AND id = ?", tenantID, groupID).
			Delete(&types.ACLSubjectGroup{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrACLGroupNotFound
		}
		return nil
	})
}

// AddMembers adds users to a group, ignoring existing rows.
func (r *aclSubjectGroupRepository) AddMembers(
	ctx context.Context, tenantID uint64, groupID string, userIDs []string, addedBy string,
) (int64, error) {
	cleaned := make([]string, 0, len(userIDs))
	seen := make(map[string]struct{}, len(userIDs))
	for _, userID := range userIDs {
		trimmed := strings.TrimSpace(userID)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		cleaned = append(cleaned, trimmed)
	}
	if len(cleaned) == 0 {
		return 0, nil
	}
	rows := make([]types.ACLSubjectGroupMember, 0, len(cleaned))
	for _, userID := range cleaned {
		rows = append(rows, types.ACLSubjectGroupMember{
			GroupID: groupID, UserID: userID, TenantID: tenantID, AddedBy: addedBy,
		})
	}
	result := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&rows)
	return result.RowsAffected, result.Error
}

// RemoveMember removes one user from a group.
func (r *aclSubjectGroupRepository) RemoveMember(
	ctx context.Context, tenantID uint64, groupID, userID string,
) error {
	return r.db.WithContext(ctx).
		Where("tenant_id = ? AND group_id = ? AND user_id = ?", tenantID, groupID, userID).
		Delete(&types.ACLSubjectGroupMember{}).Error
}

// ListMembers returns the group's member ids.
func (r *aclSubjectGroupRepository) ListMembers(
	ctx context.Context, tenantID uint64, groupID string,
) ([]string, error) {
	var ids []string
	err := r.db.WithContext(ctx).
		Model(&types.ACLSubjectGroupMember{}).
		Where("tenant_id = ? AND group_id = ?", tenantID, groupID).
		Order("created_at ASC").
		Pluck("user_id", &ids).Error
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// GroupIDsForUser returns the ids of the groups the user belongs to.
func (r *aclSubjectGroupRepository) GroupIDsForUser(
	ctx context.Context, tenantID uint64, userID string,
) ([]string, error) {
	var ids []string
	err := r.db.WithContext(ctx).
		Model(&types.ACLSubjectGroupMember{}).
		Where("tenant_id = ? AND user_id = ?", tenantID, userID).
		Order("created_at ASC").
		Pluck("group_id", &ids).Error
	if err != nil {
		return nil, err
	}
	return ids, nil
}
