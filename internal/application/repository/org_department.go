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

// ErrOrgDepartmentNotFound is returned when a department lookup misses.
var ErrOrgDepartmentNotFound = errors.New("org department not found")

// orgDepartmentRepository implements interfaces.OrgDepartmentRepository
// (KnowAxis V3.0 §2.1 L1 organization layer).
type orgDepartmentRepository struct {
	db *gorm.DB
}

// NewOrgDepartmentRepository constructs the repository.
func NewOrgDepartmentRepository(db *gorm.DB) interfaces.OrgDepartmentRepository {
	return &orgDepartmentRepository{db: db}
}

// CreateDepartment inserts one node.
func (r *orgDepartmentRepository) CreateDepartment(
	ctx context.Context, department *types.OrgDepartment,
) error {
	return r.db.WithContext(ctx).Create(department).Error
}

// GetDepartment returns one node in the tenant.
func (r *orgDepartmentRepository) GetDepartment(
	ctx context.Context, tenantID uint64, departmentID string,
) (*types.OrgDepartment, error) {
	var department types.OrgDepartment
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, departmentID).
		First(&department).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrOrgDepartmentNotFound
	}
	if err != nil {
		return nil, err
	}
	return &department, nil
}

// ListDepartments returns the tenant's departments in path order.
//
// 按 path 排序而不是按 created_at：树形渲染依赖父节点先于子节点出现，路径
// 字典序天然满足这一点（父路径是子路径的前缀）。
func (r *orgDepartmentRepository) ListDepartments(
	ctx context.Context, tenantID uint64,
) ([]*types.OrgDepartment, error) {
	var rows []*types.OrgDepartment
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ?", tenantID).
		Order("path ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// ListSubtree returns the node and all its descendants.
func (r *orgDepartmentRepository) ListSubtree(
	ctx context.Context, tenantID uint64, path string,
) ([]*types.OrgDepartment, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	var rows []*types.OrgDepartment
	// 前缀匹配带右斜杠，避免 "/a/b" 匹配到 "/a/bc"。
	prefix := strings.TrimRight(path, "/")
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND (path = ? OR path LIKE ?)", tenantID, prefix, prefix+"/%").
		Order("path ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// UpdateDepartment persists the mutable fields.
func (r *orgDepartmentRepository) UpdateDepartment(
	ctx context.Context, department *types.OrgDepartment,
) error {
	result := r.db.WithContext(ctx).Model(&types.OrgDepartment{}).
		Where("tenant_id = ? AND id = ?", department.TenantID, department.ID).
		Updates(map[string]interface{}{
			"name":             department.Name,
			"manager_user_id":  department.ManagerUserID,
			"subject_group_id": department.SubjectGroupID,
			"external_id":      department.ExternalID,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrOrgDepartmentNotFound
	}
	return nil
}

// MoveDepartment rewrites the node's path and every descendant's path.
//
// 一个事务里做两次更新：先改自身，再按旧前缀批量改子孙。中途失败会让树
// 出现断链（子节点路径不再以父路径开头），所以必须原子。
func (r *orgDepartmentRepository) MoveDepartment(
	ctx context.Context, tenantID uint64, department *types.OrgDepartment, oldPath string,
) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		updates := map[string]interface{}{
			"parent_id": department.ParentID,
			"path":      department.Path,
			"depth":     department.Depth,
		}
		if err := tx.Model(&types.OrgDepartment{}).
			Where("tenant_id = ? AND id = ?", tenantID, department.ID).
			Updates(updates).Error; err != nil {
			return err
		}
		if oldPath == "" || oldPath == department.Path {
			return nil
		}
		// 子孙路径：把旧前缀替换成新前缀。用 SQL 的 REPLACE 而不是逐行读改写，
		// 一条语句完成，也避免中途行数变化导致的错位。
		return tx.Model(&types.OrgDepartment{}).
			Where("tenant_id = ? AND path LIKE ?", tenantID, strings.TrimRight(oldPath, "/")+"/%").
			Update("path", gorm.Expr(
				"REPLACE(path, ?, ?)", strings.TrimRight(oldPath, "/"), strings.TrimRight(department.Path, "/"),
			)).Error
	})
}

// DeleteDepartment removes one node.
func (r *orgDepartmentRepository) DeleteDepartment(
	ctx context.Context, tenantID uint64, departmentID string,
) error {
	result := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, departmentID).
		Delete(&types.OrgDepartment{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrOrgDepartmentNotFound
	}
	return nil
}

// FindByExternalID resolves the SCIM externalId.
func (r *orgDepartmentRepository) FindByExternalID(
	ctx context.Context, tenantID uint64, externalID string,
) (*types.OrgDepartment, error) {
	trimmed := strings.TrimSpace(externalID)
	if trimmed == "" {
		return nil, nil
	}
	var department types.OrgDepartment
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND external_id = ?", tenantID, trimmed).
		First(&department).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &department, nil
}

// AddMembers links users to a department.
func (r *orgDepartmentRepository) AddMembers(
	ctx context.Context, tenantID uint64, departmentID string, userIDs []string, addedBy string,
) (int64, error) {
	cleaned := types.NormalizeKnowledgeIDs(userIDs)
	if len(cleaned) == 0 {
		return 0, nil
	}
	rows := make([]types.OrgDepartmentMember, 0, len(cleaned))
	for _, userID := range cleaned {
		rows = append(rows, types.OrgDepartmentMember{
			DepartmentID: departmentID, UserID: userID, TenantID: tenantID, AddedBy: addedBy,
		})
	}
	result := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&rows)
	return result.RowsAffected, result.Error
}

// RemoveMember unlinks one user.
func (r *orgDepartmentRepository) RemoveMember(
	ctx context.Context, tenantID uint64, departmentID, userID string,
) error {
	return r.db.WithContext(ctx).
		Where("tenant_id = ? AND department_id = ? AND user_id = ?", tenantID, departmentID, userID).
		Delete(&types.OrgDepartmentMember{}).Error
}

// ListMembers returns the member ids of one department.
func (r *orgDepartmentRepository) ListMembers(
	ctx context.Context, tenantID uint64, departmentID string,
) ([]string, error) {
	var ids []string
	if err := r.db.WithContext(ctx).
		Model(&types.OrgDepartmentMember{}).
		Where("tenant_id = ? AND department_id = ?", tenantID, departmentID).
		Order("created_at ASC").
		Pluck("user_id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

// DepartmentsOfUser returns the departments a user belongs to.
func (r *orgDepartmentRepository) DepartmentsOfUser(
	ctx context.Context, tenantID uint64, userID string,
) ([]*types.OrgDepartment, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, nil
	}
	var rows []*types.OrgDepartment
	if err := r.db.WithContext(ctx).
		Table("org_departments AS d").
		Select("d.*").
		Joins("JOIN org_department_members m ON m.department_id = d.id").
		Where("d.tenant_id = ? AND m.tenant_id = ? AND m.user_id = ?", tenantID, tenantID, userID).
		Order("d.path ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// CountMembersByDepartment returns department id → member count.
func (r *orgDepartmentRepository) CountMembersByDepartment(
	ctx context.Context, tenantID uint64,
) (map[string]int64, error) {
	type row struct {
		DepartmentID string
		Total        int64
	}
	var rows []row
	if err := r.db.WithContext(ctx).
		Model(&types.OrgDepartmentMember{}).
		Select("department_id, COUNT(*) AS total").
		Where("tenant_id = ?", tenantID).
		Group("department_id").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(rows))
	for _, entry := range rows {
		out[entry.DepartmentID] = entry.Total
	}
	return out, nil
}
