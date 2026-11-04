package types

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ============================================================================
// 组织树（KnowAxis V3.0 §2.1 权限 L1 组织层 + §16.3 组织架构同步）。
//
// §2.1 的权限模型是四层：租户 → 组织/部门 → 空间 → 文档。平台此前只有租户层
// 与文档层——中间那层不存在，于是"授权人只能管理自己部门的人"这类判断无处
// 落地：一个部门经理在系统里和普通管理员没有区别。
//
// 设计要点：
//  1. 用**物化路径**（path）而不是递归查询：鉴权路径上要频繁回答"这个部门
//     是不是我的下级"，一次字符串前缀比较远比递归 CTE 便宜，而且跨 SQLite/
//     MySQL/Postgres 一致。
//  2. path 用 id 段而不是名字：改名不影响层级，重名也不会混淆。
//  3. 每个部门可绑定一个 ACL 主体组（§2.1 group 主体），这样"给整个部门授权"
//     不需要先手工建组——部门本身就是组。
// ============================================================================

// MaxOrgDepartmentDepth bounds the tree.
//
// 16 层足够覆盖"集团→事业部→中心→部门→组→小队"这类现实结构；更深的树通常
// 意味着建模出了问题（用类别而不是部门表达分类）。
const MaxOrgDepartmentDepth = 16

// OrgDepartment is one node of the tenant's organization tree.
type OrgDepartment struct {
	ID       string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID uint64 `json:"tenant_id" gorm:"not null;uniqueIndex:uq_org_department_path,priority:1"`
	// ParentID is empty for a root department.
	ParentID string `json:"parent_id" gorm:"type:varchar(36);not null;default:''"`
	// Name is the display name; unique among siblings.
	Name string `json:"name" gorm:"type:varchar(128);not null"`
	// Path is the materialized path of ancestor ids, "/" separated, ending
	// with this department's own id: "/root-id/child-id".
	//
	// 用 id 而不是名字：改名不改变层级，同级重名也不会混淆前缀比较。
	Path string `json:"path" gorm:"type:varchar(1024);not null;uniqueIndex:uq_org_department_path,priority:2"`
	// Depth is the number of '/' separators in Path (root = 0).
	Depth int `json:"depth" gorm:"not null;default:0"`
	// ManagerUserID names the person responsible for this department (§2.1
	// 组织范围授权：经理可管理本部门子树).
	ManagerUserID string `json:"manager_user_id" gorm:"type:varchar(36);not null;default:''"`
	// ExternalID is the SCIM externalId, so an IdP rename/relink does not
	// create a duplicate department.
	ExternalID string `json:"external_id" gorm:"type:varchar(191);not null;default:'';uniqueIndex:uq_org_department_external,priority:2"`
	// SubjectGroupID links this department to an ACL subject group, making
	// "grant to the department" a single row instead of per-user work.
	SubjectGroupID string    `json:"subject_group_id" gorm:"type:varchar(36);not null;default:''"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	// MemberCount is filled by list queries.
	MemberCount int64 `json:"member_count" gorm:"-"`
}

// TableName pins the GORM table name.
func (OrgDepartment) TableName() string { return "org_departments" }

// BeforeCreate assigns an ID before persistence.
func (d *OrgDepartment) BeforeCreate(_ *gorm.DB) error {
	if d.ID == "" {
		d.ID = uuid.NewString()
	}
	return nil
}

// OrgDepartmentMember is one user's membership in a department.
//
// 一个用户可以同时属于多个部门（兼任很常见），但每个部门内只有一条记录。
type OrgDepartmentMember struct {
	DepartmentID string `json:"department_id" gorm:"type:varchar(36);primaryKey"`
	UserID       string `json:"user_id" gorm:"type:varchar(36);primaryKey"`
	TenantID     uint64 `json:"tenant_id" gorm:"not null;index:idx_org_dept_members_tenant,priority:1"`
	// IsManager marks department leads; they may manage the subtree's grants.
	IsManager bool      `json:"is_manager" gorm:"not null;default:false"`
	AddedBy   string    `json:"added_by" gorm:"type:varchar(36);not null;default:''"`
	CreatedAt time.Time `json:"created_at"`
}

// TableName pins the GORM table name.
func (OrgDepartmentMember) TableName() string { return "org_department_members" }

// BuildDepartmentPath renders the materialized path for a new node.
func BuildDepartmentPath(parentPath, departmentID string) string {
	parentPath = strings.TrimSpace(parentPath)
	if parentPath == "" {
		return "/" + departmentID
	}
	return strings.TrimRight(parentPath, "/") + "/" + departmentID
}

// DepartmentDepth returns the 0-based depth of a path.
func DepartmentDepth(path string) int {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return 0
	}
	return strings.Count(trimmed, "/")
}

// DepartmentIsDescendantOf reports whether path is ancestorPath or below it.
//
// 前缀比较必须带分隔符：否则 "/a/bc" 会被误判为 "/a/b" 的下级。
func DepartmentIsDescendantOf(path, ancestorPath string) bool {
	path = strings.TrimSpace(path)
	ancestorPath = strings.TrimSpace(ancestorPath)
	if path == "" || ancestorPath == "" {
		return false
	}
	if path == ancestorPath {
		return true
	}
	return strings.HasPrefix(path, strings.TrimRight(ancestorPath, "/")+"/")
}

// DepartmentParentPath strips the last segment of a path.
func DepartmentParentPath(path string) string {
	trimmed := strings.Trim(strings.TrimSpace(path), "/")
	if trimmed == "" {
		return ""
	}
	if index := strings.LastIndexByte(trimmed, '/'); index > 0 {
		return "/" + trimmed[:index]
	}
	return ""
}

// NormalizeOrgDepartmentName trims and validates a department name.
func NormalizeOrgDepartmentName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", fmt.Errorf("department name is required")
	}
	if runes := []rune(name); len(runes) > 128 {
		return "", fmt.Errorf("department name must be at most 128 characters")
	}
	// 路径分隔符会破坏物化路径，必须拒绝而不是替换：静默替换会让两个不同
	// 部门得到同一个名字。
	if strings.ContainsAny(name, "/\x00") {
		return "", fmt.Errorf("department name must not contain '/'")
	}
	return name, nil
}

// OrgDepartmentNode is one department with its children, for the tree read.
type OrgDepartmentNode struct {
	OrgDepartment
	Children []*OrgDepartmentNode `json:"children"`
	// Members is filled for the leaf read (or when the caller asks for it).
	Members []string `json:"members,omitempty"`
}

// BuildOrgDepartmentTree assembles a flat list into a forest.
//
// 排序按名字：树形结构如果没有稳定顺序，每次刷新都会重排，读者会以为结构变了。
func BuildOrgDepartmentTree(rows []*OrgDepartment) []*OrgDepartmentNode {
	nodes := make(map[string]*OrgDepartmentNode, len(rows))
	for _, row := range rows {
		if row == nil {
			continue
		}
		nodes[row.ID] = &OrgDepartmentNode{
			OrgDepartment: *row,
			Children:      []*OrgDepartmentNode{},
		}
	}
	roots := make([]*OrgDepartmentNode, 0)
	for _, row := range rows {
		if row == nil {
			continue
		}
		node := nodes[row.ID]
		if row.ParentID == "" {
			roots = append(roots, node)
			continue
		}
		if parent, ok := nodes[row.ParentID]; ok {
			parent.Children = append(parent.Children, node)
			continue
		}
		// 父节点不可见（被过滤或已删除）：提升为根，避免整棵子树消失。
		roots = append(roots, node)
	}
	var sortNodes func(list []*OrgDepartmentNode)
	sortNodes = func(list []*OrgDepartmentNode) {
		sort.SliceStable(list, func(i, j int) bool {
			if list[i].Name != list[j].Name {
				return list[i].Name < list[j].Name
			}
			return list[i].ID < list[j].ID
		})
		for _, node := range list {
			sortNodes(node.Children)
		}
	}
	sortNodes(roots)
	return roots
}

// ValidateDepartmentMove guards a re-parent operation.
//
// 两个必须拒绝的动作：
//  1. 把部门挂到自己的子树下——会形成环，整棵子树从根上消失；
//  2. 移动导致深度超过上限。
func ValidateDepartmentMove(movingPath, newParentPath string, subtreeHeight int) error {
	if strings.TrimSpace(newParentPath) != "" &&
		DepartmentIsDescendantOf(newParentPath, movingPath) {
		return fmt.Errorf("cannot move a department under its own subtree")
	}
	depth := 0
	if strings.TrimSpace(newParentPath) != "" {
		depth = DepartmentDepth(newParentPath) + 1
	}
	if depth+subtreeHeight > MaxOrgDepartmentDepth {
		return fmt.Errorf("moving here would exceed the maximum depth of %d", MaxOrgDepartmentDepth)
	}
	return nil
}

// DepartmentScope describes what one caller may administer (§2.1 组织范围).
type DepartmentScope struct {
	// UserID the scope belongs to.
	UserID string `json:"user_id"`
	// ManagedPaths are the department subtrees the caller may administer. An
	// entry grants the whole subtree.
	ManagedPaths []string `json:"managed_paths"`
	// TenantWide is set for admins: they manage every department.
	TenantWide bool `json:"tenant_wide"`
}

// Covers reports whether the scope includes one department path.
func (s DepartmentScope) Covers(path string) bool {
	if s.TenantWide {
		return true
	}
	for _, managed := range s.ManagedPaths {
		if DepartmentIsDescendantOf(path, managed) {
			return true
		}
	}
	return false
}

// OrgDepartmentInput is the create/update payload.
type OrgDepartmentInput struct {
	// ParentID is the parent node ('' for a root department).
	ParentID string `json:"parent_id"`
	// Name is the display name.
	Name string `json:"name"`
	// ManagerUserID optionally names the department lead.
	ManagerUserID string `json:"manager_user_id"`
	// SubjectGroupID optionally links an ACL subject group.
	SubjectGroupID string `json:"subject_group_id"`
	// ExternalID is the SCIM externalId ('' for hand-created nodes).
	ExternalID string `json:"external_id"`
}
