package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Tencent/WeKnora/internal/application/repository"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
)

// 组织树（§2.1）的审计动作。
const (
	auditActionDeptCreated = "organization.department_created"
	auditActionDeptUpdated = "organization.department_updated"
	auditActionDeptMoved   = "organization.department_moved"
	auditActionDeptDeleted = "organization.department_deleted"
	auditActionDeptMembers = "organization.department_members_changed"
)

// orgDepartmentService implements interfaces.OrgDepartmentService.
type orgDepartmentService struct {
	repo     interfaces.OrgDepartmentRepository
	groups   interfaces.ACLSubjectGroupService
	versions interfaces.PermissionVersionService
	audit    interfaces.AuditLogService
}

// NewOrgDepartmentService constructs the service.
func NewOrgDepartmentService(
	repo interfaces.OrgDepartmentRepository,
	groups interfaces.ACLSubjectGroupService,
	versions interfaces.PermissionVersionService,
	audit interfaces.AuditLogService,
) interfaces.OrgDepartmentService {
	return &orgDepartmentService{
		repo: repo, groups: groups, versions: versions, audit: audit,
	}
}

// CreateDepartment adds a node.
//
// 路径在服务层算：仓储只存，不推导层级。这样"父节点必须存在且可见"与
// 深度上限都能在一个地方校验。
func (s *orgDepartmentService) CreateDepartment(
	ctx context.Context, tenantID uint64, actorID string, input types.OrgDepartmentInput,
) (*types.OrgDepartment, error) {
	if tenantID == 0 {
		return nil, apperrors.NewUnauthorizedError("tenant context is required")
	}
	name, err := types.NormalizeOrgDepartmentName(input.Name)
	if err != nil {
		return nil, apperrors.NewBadRequestError(err.Error())
	}
	department := &types.OrgDepartment{
		TenantID:       tenantID,
		ParentID:       strings.TrimSpace(input.ParentID),
		Name:           name,
		ManagerUserID:  strings.TrimSpace(input.ManagerUserID),
		SubjectGroupID: strings.TrimSpace(input.SubjectGroupID),
		ExternalID:     strings.TrimSpace(input.ExternalID),
	}
	parentPath := ""
	if department.ParentID != "" {
		parent, parentErr := s.repo.GetDepartment(ctx, tenantID, department.ParentID)
		if parentErr != nil {
			return nil, mapDepartmentError(parentErr)
		}
		parentPath = parent.Path
		if parent.Depth+1 >= types.MaxOrgDepartmentDepth {
			return nil, apperrors.NewBadRequestError(fmt.Sprintf(
				"department depth would exceed the maximum of %d", types.MaxOrgDepartmentDepth))
		}
	}
	// path 需要自身 ID，所以先分配 ID 再算路径，**一次**插入完成。
	// 分两次写会给 (tenant_id, path) 的唯一索引留下一个 path='' 的中间态，
	// 两个并发创建会在那里撞车。
	if department.ID == "" {
		department.ID = uuid.NewString()
	}
	department.Path = types.BuildDepartmentPath(parentPath, department.ID)
	department.Depth = types.DepartmentDepth(department.Path)
	if err := s.repo.CreateDepartment(ctx, department); err != nil {
		return nil, mapDepartmentError(err)
	}
	if department.SubjectGroupID == "" && s.groups != nil {
		// 部门默认拥有一个同名主体组：授权"给整个部门"不需要先手工建组。
		if group, groupErr := s.groups.CreateGroup(ctx, tenantID, actorID, name, "部门主体组"); groupErr == nil {
			department.SubjectGroupID = group.ID
			_ = s.repo.UpdateDepartment(ctx, department)
		}
	}
	s.logAudit(ctx, tenantID, actorID, auditActionDeptCreated, department.ID,
		map[string]interface{}{"name": department.Name, "path": department.Path})
	return department, nil
}

// ListTree returns the whole tree with member counts.
func (s *orgDepartmentService) ListTree(
	ctx context.Context, tenantID uint64,
) ([]*types.OrgDepartmentNode, error) {
	rows, err := s.repo.ListDepartments(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	counts, err := s.repo.CountMembersByDepartment(ctx, tenantID)
	if err != nil {
		logger.Warnf(ctx, "[org] member count failed: %v", err)
	}
	for _, row := range rows {
		row.MemberCount = counts[row.ID]
	}
	return types.BuildOrgDepartmentTree(rows), nil
}

// GetDepartment returns one node with its member ids.
func (s *orgDepartmentService) GetDepartment(
	ctx context.Context, tenantID uint64, departmentID string,
) (*types.OrgDepartmentNode, error) {
	department, err := s.repo.GetDepartment(ctx, tenantID, departmentID)
	if err != nil {
		return nil, mapDepartmentError(err)
	}
	members, err := s.repo.ListMembers(ctx, tenantID, departmentID)
	if err != nil {
		return nil, err
	}
	node := &types.OrgDepartmentNode{OrgDepartment: *department, Members: members}
	node.MemberCount = int64(len(members))
	return node, nil
}

// UpdateDepartment edits the mutable fields.
func (s *orgDepartmentService) UpdateDepartment(
	ctx context.Context, tenantID uint64, departmentID, actorID string,
	input types.OrgDepartmentInput,
) (*types.OrgDepartment, error) {
	department, err := s.repo.GetDepartment(ctx, tenantID, departmentID)
	if err != nil {
		return nil, mapDepartmentError(err)
	}
	if trimmed := strings.TrimSpace(input.Name); trimmed != "" {
		name, nameErr := types.NormalizeOrgDepartmentName(trimmed)
		if nameErr != nil {
			return nil, apperrors.NewBadRequestError(nameErr.Error())
		}
		department.Name = name
	}
	if input.ManagerUserID != "" {
		department.ManagerUserID = strings.TrimSpace(input.ManagerUserID)
	}
	if input.SubjectGroupID != "" {
		department.SubjectGroupID = strings.TrimSpace(input.SubjectGroupID)
	}
	if input.ExternalID != "" {
		department.ExternalID = strings.TrimSpace(input.ExternalID)
	}
	if err := s.repo.UpdateDepartment(ctx, department); err != nil {
		return nil, mapDepartmentError(err)
	}
	s.logAudit(ctx, tenantID, actorID, auditActionDeptUpdated, department.ID,
		map[string]interface{}{"name": department.Name})
	return department, nil
}

// MoveDepartment re-parents a node.
func (s *orgDepartmentService) MoveDepartment(
	ctx context.Context, tenantID uint64, departmentID, actorID, newParentID string,
) (*types.OrgDepartment, error) {
	department, err := s.repo.GetDepartment(ctx, tenantID, departmentID)
	if err != nil {
		return nil, mapDepartmentError(err)
	}
	newParentID = strings.TrimSpace(newParentID)
	newParentPath := ""
	if newParentID != "" {
		parent, parentErr := s.repo.GetDepartment(ctx, tenantID, newParentID)
		if parentErr != nil {
			return nil, mapDepartmentError(parentErr)
		}
		newParentPath = parent.Path
	}
	// 子树高度决定移动后是否越界；子树自身也要一起搬。
	subtree, err := s.repo.ListSubtree(ctx, tenantID, department.Path)
	if err != nil {
		return nil, err
	}
	height := 0
	for _, node := range subtree {
		if delta := node.Depth - department.Depth; delta > height {
			height = delta
		}
	}
	if err := types.ValidateDepartmentMove(department.Path, newParentPath, height); err != nil {
		return nil, apperrors.NewBadRequestError(err.Error())
	}
	oldPath := department.Path
	department.ParentID = newParentID
	department.Path = types.BuildDepartmentPath(newParentPath, department.ID)
	department.Depth = types.DepartmentDepth(department.Path)
	if err := s.repo.MoveDepartment(ctx, tenantID, department, oldPath); err != nil {
		return nil, mapDepartmentError(err)
	}
	// 层级变化会改变"谁能管谁"，缓存必须失效。
	s.bumpEpoch(ctx, tenantID)
	s.logAudit(ctx, tenantID, actorID, auditActionDeptMoved, department.ID,
		map[string]interface{}{"from": oldPath, "to": department.Path})
	return department, nil
}

// DeleteDepartment removes a node, refusing when it still has children.
//
// 拒绝而不是级联删除：一次误删会让整棵子树连同成员关系一起消失，而组织
// 结构是企业权限的输入，恢复代价极高。要先删子节点就得先显式处理它们。
func (s *orgDepartmentService) DeleteDepartment(
	ctx context.Context, tenantID uint64, departmentID, actorID string,
) error {
	department, err := s.repo.GetDepartment(ctx, tenantID, departmentID)
	if err != nil {
		return mapDepartmentError(err)
	}
	subtree, err := s.repo.ListSubtree(ctx, tenantID, department.Path)
	if err != nil {
		return err
	}
	for _, node := range subtree {
		if node.ID != department.ID {
			return apperrors.NewBadRequestError(
				"department still has children; delete or move them first")
		}
	}
	if err := s.repo.DeleteDepartment(ctx, tenantID, departmentID); err != nil {
		return mapDepartmentError(err)
	}
	s.bumpEpoch(ctx, tenantID)
	s.logAudit(ctx, tenantID, actorID, auditActionDeptDeleted, departmentID,
		map[string]interface{}{"name": department.Name})
	return nil
}

// AddMembers links users to a department.
func (s *orgDepartmentService) AddMembers(
	ctx context.Context, tenantID uint64, departmentID, actorID string, userIDs []string,
) (int64, error) {
	department, err := s.repo.GetDepartment(ctx, tenantID, departmentID)
	if err != nil {
		return 0, mapDepartmentError(err)
	}
	added, err := s.repo.AddMembers(ctx, tenantID, departmentID, userIDs, actorID)
	if err != nil {
		return 0, err
	}
	// 部门成员同时进入该部门的主体组：组才是 ACL 真正解析的主体。
	if added > 0 && department.SubjectGroupID != "" && s.groups != nil {
		if _, groupErr := s.groups.AddMembers(ctx, tenantID, department.SubjectGroupID, actorID, userIDs); groupErr != nil {
			logger.Warnf(ctx, "[org] sync department group members failed: %v", groupErr)
		}
	}
	if added > 0 {
		s.bumpEpoch(ctx, tenantID)
	}
	s.logAudit(ctx, tenantID, actorID, auditActionDeptMembers, departmentID,
		map[string]interface{}{"added": added, "user_ids": userIDs})
	return added, nil
}

// RemoveMember unlinks one user.
func (s *orgDepartmentService) RemoveMember(
	ctx context.Context, tenantID uint64, departmentID, userID, actorID string,
) error {
	department, err := s.repo.GetDepartment(ctx, tenantID, departmentID)
	if err != nil {
		return mapDepartmentError(err)
	}
	if err := s.repo.RemoveMember(ctx, tenantID, departmentID, userID); err != nil {
		return err
	}
	if department.SubjectGroupID != "" && s.groups != nil {
		if groupErr := s.groups.RemoveMember(ctx, tenantID, department.SubjectGroupID, userID, actorID); groupErr != nil {
			logger.Warnf(ctx, "[org] sync department group removal failed: %v", groupErr)
		}
	}
	// 退部门等于收权：权限版本必须前进。
	s.bumpEpoch(ctx, tenantID)
	s.logAudit(ctx, tenantID, actorID, auditActionDeptMembers, departmentID,
		map[string]interface{}{"removed": userID})
	return nil
}

// ScopeOf answers which departments a user may administer.
func (s *orgDepartmentService) ScopeOf(
	ctx context.Context, tenantID uint64, userID string,
) (types.DepartmentScope, error) {
	scope := types.DepartmentScope{UserID: userID}
	if userID == "" {
		return scope, nil
	}
	// Admin+ 管理全租户：这是既有角色矩阵的口径，不在组织层重复定义。
	if role := types.CallerFromContext(ctx).Role; role.HasPermission(types.TenantRoleAdmin) {
		scope.TenantWide = true
		return scope, nil
	}
	rows, err := s.repo.DepartmentsOfUser(ctx, tenantID, userID)
	if err != nil {
		return scope, err
	}
	for _, department := range rows {
		if department.ManagerUserID == userID {
			scope.ManagedPaths = append(scope.ManagedPaths, department.Path)
		}
	}
	return scope, nil
}

// CanManageDepartment reports whether the user may administer that node.
func (s *orgDepartmentService) CanManageDepartment(
	ctx context.Context, tenantID uint64, userID, departmentID string,
) (bool, error) {
	department, err := s.repo.GetDepartment(ctx, tenantID, departmentID)
	if err != nil {
		return false, mapDepartmentError(err)
	}
	scope, err := s.ScopeOf(ctx, tenantID, userID)
	if err != nil {
		return false, err
	}
	return scope.Covers(department.Path), nil
}

// bumpEpoch advances the tenant epoch so snapshot caches miss.
func (s *orgDepartmentService) bumpEpoch(ctx context.Context, tenantID uint64) {
	if s.versions == nil || tenantID == 0 {
		return
	}
	if _, err := s.versions.Bump(ctx, tenantID, types.PermissionScopeTenant, "", "org_department_changed"); err != nil {
		logger.Warnf(ctx, "failed to bump permission version for tenant %d: %v", tenantID, err)
	}
}

// logAudit writes one organization audit event.
func (s *orgDepartmentService) logAudit(
	ctx context.Context, tenantID uint64, actorID, action, departmentID string,
	details map[string]interface{},
) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Log(ctx, &types.AuditLog{
		TenantID:    tenantID,
		ActorUserID: actorID,
		Action:      types.AuditAction(action),
		ScopeType:   "organization",
		TargetType:  "department",
		TargetID:    departmentID,
		Outcome:     "success",
		Details:     gamificationAuditJSON(details),
	})
}

// mapDepartmentError maps repository misses onto the API 404.
func mapDepartmentError(err error) error {
	if errors.Is(err, repository.ErrOrgDepartmentNotFound) {
		return apperrors.NewNotFoundError("department not found")
	}
	return err
}
