package types

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ============================================================================
// 授权主体组（KnowAxis V3.0 §2.1 L2 资源层 + §2.9 权限矩阵）。
//
// 平台此前的 ACL 主体只有单个用户：`group`/`role` 是迁移里预留的列，但没有
// 任何解析器，写进去也不会生效。企业里"给工艺部全体可见"是常态，逐人授权
// 既不可维护，也会在人员变动时留下过期授权。
//
// 这里补上：
//   - ACLSubjectGroup：租户内的用户组与成员；
//   - ACLSubject：一次授权判定的主体三元组（user / group / role）；
//   - ACLSubjectGroupService.SubjectsForUser：把"这个用户"展开成它实际拥有
//     的全部主体，供 deny/allow 查询使用。
//
// 关键取舍：role 主体是**派生**的，不建表。用户的租户角色已经存在
// tenant_members 里，再存一份角色成员表只会产生两份真相。
// ============================================================================

// ACLSubject is one authorization subject in a grant or a query.
type ACLSubject struct {
	// Type is user / group / role.
	Type KnowledgeACLSubjectType
	// ID is the user id, group id, or role name.
	ID string
}

// String renders the subject for logs and cache keys.
func (s ACLSubject) String() string {
	return string(s.Type) + ":" + s.ID
}

// Valid reports whether the subject is well-formed.
func (s ACLSubject) Valid() bool {
	return s.Type.Valid() && strings.TrimSpace(s.ID) != ""
}

// ACLSubjectGroup is one tenant-scoped user group usable as an ACL subject.
type ACLSubjectGroup struct {
	ID          string    `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID    uint64    `json:"tenant_id" gorm:"not null;uniqueIndex:uq_acl_group_name,priority:1"`
	Name        string    `json:"name" gorm:"type:varchar(128);not null;uniqueIndex:uq_acl_group_name,priority:2"`
	Description string    `json:"description" gorm:"type:varchar(512);not null;default:''"`
	CreatedBy   string    `json:"created_by" gorm:"type:varchar(36);not null;default:''"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	// MemberCount is filled by list queries for display.
	MemberCount int64 `json:"member_count" gorm:"-"`
}

// TableName pins the GORM table name.
func (ACLSubjectGroup) TableName() string { return "acl_subject_groups" }

// BeforeCreate assigns an ID before persistence.
func (g *ACLSubjectGroup) BeforeCreate(_ *gorm.DB) error {
	if g.ID == "" {
		g.ID = uuid.NewString()
	}
	return nil
}

// ACLSubjectGroupMember is one user's membership in a group.
type ACLSubjectGroupMember struct {
	GroupID   string    `json:"group_id" gorm:"type:varchar(36);primaryKey"`
	UserID    string    `json:"user_id" gorm:"type:varchar(36);primaryKey"`
	TenantID  uint64    `json:"tenant_id" gorm:"not null;index:idx_acl_group_members_tenant,priority:1"`
	AddedBy   string    `json:"added_by" gorm:"type:varchar(36);not null;default:''"`
	CreatedAt time.Time `json:"created_at"`
}

// TableName pins the GORM table name.
func (ACLSubjectGroupMember) TableName() string { return "acl_subject_group_members" }

// MaxACLGroupNameLen mirrors the migration's varchar(128).
const MaxACLGroupNameLen = 128

// NormalizeACLGroupName trims and validates a group name.
func NormalizeACLGroupName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", fmt.Errorf("group name is required")
	}
	if runes := []rune(name); len(runes) > MaxACLGroupNameLen {
		return "", fmt.Errorf("group name must be at most %d characters", MaxACLGroupNameLen)
	}
	return name, nil
}

// SubjectTypeForACLAction validates the subject kind a grant may target.
//
// `role` 主体只对租户角色名成立；写错名字的 role 授权永远不会生效，因此在
// 入口就拒绝，而不是让它静默躺在表里。
func SubjectTypeForACLAction(subjectType string, subjectID string) (KnowledgeACLSubjectType, error) {
	kind := KnowledgeACLSubjectType(strings.TrimSpace(subjectType))
	if kind == "" {
		kind = KnowledgeACLSubjectUser
	}
	if !kind.Valid() {
		return "", fmt.Errorf("unknown subject type %q", subjectType)
	}
	id := strings.TrimSpace(subjectID)
	if id == "" {
		return "", fmt.Errorf("subject_id is required")
	}
	if kind == KnowledgeACLSubjectRole {
		if !TenantRole(id).IsValid() {
			return "", fmt.Errorf("unknown role %q", subjectID)
		}
	}
	return kind, nil
}

// GrantCeilingInput describes one grant attempt for the ceiling check.
type GrantCeilingInput struct {
	// GrantorRole is the actor's tenant role.
	GrantorRole TenantRole
	// GrantorActions is the action set the grantor actually holds on the
	// document (document ACL ∪ role matrix). Empty means "derive from role".
	GrantorActions []PermissionAction
	// Action is the action being granted or denied.
	Action PermissionAction
	// Effect is allow or deny.
	Effect KnowledgeACLEffect
	// SubjectType / SubjectID identify the target.
	SubjectType KnowledgeACLSubjectType
	SubjectID   string
	// GrantorUserID is checked against user subjects (不能自授).
	GrantorUserID string
}

// GrantCeilingViolation names the reason a grant is refused.
type GrantCeilingViolation string

const (
	// GrantViolationSelf 不能给自己授权（§2.1）。
	GrantViolationSelf GrantCeilingViolation = "self_grant"
	// GrantViolationUnknownAction 未知动作。
	GrantViolationUnknownAction GrantCeilingViolation = "unknown_action"
	// GrantViolationActionCeiling 授予了授权人自己没有的动作（§2.3 动作上限）。
	GrantViolationActionCeiling GrantCeilingViolation = "action_ceiling"
	// GrantViolationRoleCeiling 非管理员试图按角色授权（角色授权会一次性改变
	// 全租户该角色所有人的访问，只有 Admin+ 能做）。
	GrantViolationRoleCeiling GrantCeilingViolation = "role_ceiling"
	// GrantViolationSubjectType 未知主体类型。
	GrantViolationSubjectType GrantCeilingViolation = "unknown_subject_type"
)

// Error renders the violation for an API response.
func (v GrantCeilingViolation) Error() string {
	switch v {
	case GrantViolationSelf:
		return "不能自授权限（authorize 不能授予自己）"
	case GrantViolationUnknownAction:
		return "unknown permission action"
	case GrantViolationActionCeiling:
		return "cannot grant an action you do not hold yourself"
	case GrantViolationRoleCeiling:
		return "only an admin may grant by role"
	case GrantViolationSubjectType:
		return "unknown subject type"
	default:
		return string(v)
	}
}

// CheckGrantCeiling decides whether an actor may write this grant.
//
// 三条硬规则，全部 fail-closed：
//  1. 不能自授：allow 给不了自己；deny 给自己等于自伤，也一并拒绝。
//  2. 动作上限：授予的 allow 动作必须在授权人自己的动作集合里——否则一个只有
//     view 的人可以给自己人开 export，"能授权"变成了"能提权"。
//  3. 角色上限：按角色授权影响全租户同角色的人，只有 Admin+ 可以做。
//
// deny 不受动作上限约束：收紧永远合法，这是与目录 ACL 一致的口径。
func CheckGrantCeiling(input GrantCeilingInput) error {
	if !input.Action.Valid() {
		return GrantViolationUnknownAction
	}
	if !input.SubjectType.Valid() {
		return GrantViolationSubjectType
	}
	if input.SubjectType == KnowledgeACLSubjectUser &&
		input.GrantorUserID != "" && input.SubjectID == input.GrantorUserID {
		return GrantViolationSelf
	}
	if input.SubjectType == KnowledgeACLSubjectRole &&
		!input.GrantorRole.HasPermission(TenantRoleAdmin) {
		return GrantViolationRoleCeiling
	}
	if input.Effect == KnowledgeACLEffectDeny {
		return nil
	}
	actions := input.GrantorActions
	if len(actions) == 0 {
		actions = DefaultRoleActions[input.GrantorRole]
	}
	for _, action := range actions {
		if action == input.Action {
			return nil
		}
	}
	return GrantViolationActionCeiling
}
