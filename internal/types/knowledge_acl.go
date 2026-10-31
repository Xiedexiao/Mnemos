package types

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// KnowledgeACLSubjectType enumerates ACL subject kinds. v1 only resolves
// user subjects; group/role are reserved for the relation-based
// authorization phase (P3 关系型授权).
type KnowledgeACLSubjectType string

const (
	// KnowledgeACLSubjectUser 单个用户。
	KnowledgeACLSubjectUser KnowledgeACLSubjectType = "user"
	// KnowledgeACLSubjectGroup 用户组（预留）。
	KnowledgeACLSubjectGroup KnowledgeACLSubjectType = "group"
	// KnowledgeACLSubjectRole 角色（预留）。
	KnowledgeACLSubjectRole KnowledgeACLSubjectType = "role"
)

// KnowledgeACLEffect implements deny-wins semantics (V3.0 §2.1): any deny hit
// rejects; allow can never override deny.
type KnowledgeACLEffect string

const (
	// KnowledgeACLEffectAllow 显式允许。
	KnowledgeACLEffectAllow KnowledgeACLEffect = "allow"
	// KnowledgeACLEffectDeny 显式拒绝（优先于一切 allow）。
	KnowledgeACLEffectDeny KnowledgeACLEffect = "deny"
)

// KnowledgeACL is one per-document authorization row (V3.0 §2.1 L2 资源层:
// 租户 → 空间 → 目录 → 文档 → Chunk；chunk 默认继承文档，v1 不做 chunk 级例外).
type KnowledgeACL struct {
	// ID of the grant.
	ID string `json:"id" gorm:"type:varchar(36);primaryKey"`
	// TenantID owning the grant.
	TenantID uint64 `json:"tenant_id" gorm:"not null"`
	// KnowledgeID this grant applies to (chunks inherit).
	KnowledgeID string `json:"knowledge_id" gorm:"type:varchar(36);not null;index"`
	// SubjectType: user (group/role reserved).
	SubjectType string `json:"subject_type" gorm:"type:varchar(16);not null;default:'user'"`
	// SubjectID: user ID (group/role identifier once supported).
	SubjectID string `json:"subject_id" gorm:"type:varchar(64);not null"`
	// Action granted/denied; one row per action so "能看≠能问≠能下载".
	Action string `json:"action" gorm:"type:varchar(24);not null;default:'view'"`
	// Effect: allow or deny; deny wins.
	Effect string `json:"effect" gorm:"type:varchar(8);not null;default:'allow'"`
	// GrantedBy records who created the grant (不能自授在路由层校验).
	GrantedBy string `json:"granted_by" gorm:"type:varchar(36);not null;default:''"`
	// ExpiresAt makes the grant self-invalidating (外部协作者到期).
	ExpiresAt *time.Time `json:"expires_at"`
	// CreatedAt of the grant.
	CreatedAt time.Time `json:"created_at"`
}

// TableName pins the GORM table name.
func (KnowledgeACL) TableName() string { return "knowledge_acl" }

// BeforeCreate assigns an ID before persistence.
func (a *KnowledgeACL) BeforeCreate(_ *gorm.DB) error {
	if a.ID == "" {
		a.ID = uuid.NewString()
	}
	return nil
}

// Valid reports whether the subject type is known.
func (t KnowledgeACLSubjectType) Valid() bool {
	switch t {
	case KnowledgeACLSubjectUser, KnowledgeACLSubjectGroup, KnowledgeACLSubjectRole:
		return true
	default:
		return false
	}
}
