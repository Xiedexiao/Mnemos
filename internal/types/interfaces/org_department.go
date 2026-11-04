package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// OrgDepartmentRepository persists the tenant organization tree (§2.1 L1).
type OrgDepartmentRepository interface {
	// CreateDepartment inserts one node (path already computed by the service).
	CreateDepartment(ctx context.Context, department *types.OrgDepartment) error
	// GetDepartment returns one node in the tenant.
	GetDepartment(ctx context.Context, tenantID uint64, departmentID string) (*types.OrgDepartment, error)
	// ListDepartments returns the tenant's departments in path order.
	ListDepartments(ctx context.Context, tenantID uint64) ([]*types.OrgDepartment, error)
	// ListSubtree returns the node and all its descendants (path prefix).
	ListSubtree(ctx context.Context, tenantID uint64, path string) ([]*types.OrgDepartment, error)
	// UpdateDepartment persists name/manager/group/external id changes.
	UpdateDepartment(ctx context.Context, department *types.OrgDepartment) error
	// MoveDepartment rewrites the node's own path and every descendant's path.
	MoveDepartment(
		ctx context.Context, tenantID uint64, department *types.OrgDepartment,
		oldPath string,
	) error
	// DeleteDepartment removes one node; callers must have moved its children.
	DeleteDepartment(ctx context.Context, tenantID uint64, departmentID string) error
	// FindByExternalID resolves the SCIM externalId.
	FindByExternalID(
		ctx context.Context, tenantID uint64, externalID string,
	) (*types.OrgDepartment, error)

	// AddMembers links users to a department, ignoring existing rows.
	AddMembers(
		ctx context.Context, tenantID uint64, departmentID string, userIDs []string, addedBy string,
	) (int64, error)
	// RemoveMember unlinks one user.
	RemoveMember(ctx context.Context, tenantID uint64, departmentID, userID string) error
	// ListMembers returns the member ids of one department.
	ListMembers(ctx context.Context, tenantID uint64, departmentID string) ([]string, error)
	// DepartmentsOfUser returns the departments a user belongs to.
	DepartmentsOfUser(ctx context.Context, tenantID uint64, userID string) ([]*types.OrgDepartment, error)
	// CountMembersByDepartment returns department id → member count.
	CountMembersByDepartment(ctx context.Context, tenantID uint64) (map[string]int64, error)
}

// OrgDepartmentService manages the organization tree and answers the scope
// questions authorization needs.
type OrgDepartmentService interface {
	// CreateDepartment adds a node (Admin).
	CreateDepartment(
		ctx context.Context, tenantID uint64, actorID string, input types.OrgDepartmentInput,
	) (*types.OrgDepartment, error)
	// ListTree returns the whole tree.
	ListTree(ctx context.Context, tenantID uint64) ([]*types.OrgDepartmentNode, error)
	// GetDepartment returns one node with its member ids.
	GetDepartment(
		ctx context.Context, tenantID uint64, departmentID string,
	) (*types.OrgDepartmentNode, error)
	// UpdateDepartment edits name/manager/group (Admin).
	UpdateDepartment(
		ctx context.Context, tenantID uint64, departmentID, actorID string,
		input types.OrgDepartmentInput,
	) (*types.OrgDepartment, error)
	// MoveDepartment re-parents a node (Admin), rejecting cycles and depth
	// overflow.
	MoveDepartment(
		ctx context.Context, tenantID uint64, departmentID, actorID, newParentID string,
	) (*types.OrgDepartment, error)
	// DeleteDepartment removes a node; it refuses when children exist so a
	// whole subtree cannot vanish by accident.
	DeleteDepartment(ctx context.Context, tenantID uint64, departmentID, actorID string) error
	// AddMembers links users to a department.
	AddMembers(
		ctx context.Context, tenantID uint64, departmentID, actorID string, userIDs []string,
	) (int64, error)
	// RemoveMember unlinks one user.
	RemoveMember(ctx context.Context, tenantID uint64, departmentID, userID, actorID string) error
	// ScopeOf answers "which departments may this user administer".
	//
	// 口径：Admin+ 管理全租户；其余人管理自己是经理的部门子树。没有这条判断，
	// 部门经理与普通人无法区分。
	ScopeOf(ctx context.Context, tenantID uint64, userID string) (types.DepartmentScope, error)
	// CanManageDepartment reports whether the user may administer that node.
	CanManageDepartment(ctx context.Context, tenantID uint64, userID, departmentID string) (bool, error)
}
