package types

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// PermissionScopeType names the granularity a permission version tracks
// (KnowAxis V3.0 §2.6). A change bumps the smallest scope that covers it and
// the readers of every enclosing scope see the bump.
type PermissionScopeType string

const (
	// PermissionScopeTenant covers tenant-wide policy such as the role matrix.
	PermissionScopeTenant PermissionScopeType = "tenant"
	// PermissionScopeSpace covers one knowledge base (shares, bulk grants).
	PermissionScopeSpace PermissionScopeType = "space"
	// PermissionScopeKnowledge covers one document (ACL, classification).
	PermissionScopeKnowledge PermissionScopeType = "knowledge"
)

// Valid reports whether the scope type is known.
func (s PermissionScopeType) Valid() bool {
	switch s {
	case PermissionScopeTenant, PermissionScopeSpace, PermissionScopeKnowledge:
		return true
	default:
		return false
	}
}

// PermissionVersion is the monotonically increasing authorization epoch of one
// scope. Caches that must never serve content a user no longer has access to
// fold this number into their key: when a grant changes, the epoch moves and
// every old key becomes unreachable in one step (V3.0 §2.6 answer-cache outlet
// ≤ 1 minute, and §2.7 "缓存键包含权限版本").
type PermissionVersion struct {
	// ID of the row.
	ID string `json:"id" gorm:"type:varchar(36);primaryKey"`
	// TenantID owning the epoch.
	TenantID uint64 `json:"tenant_id" gorm:"not null"`
	// ScopeType / ScopeID identify what the epoch covers. Tenant scope uses an
	// empty ScopeID so there is exactly one row per tenant.
	ScopeType string `json:"scope_type" gorm:"type:varchar(16);not null"`
	ScopeID   string `json:"scope_id" gorm:"type:varchar(64);not null;default:''"`
	// Version moves forward on every authorization change.
	Version int64 `json:"version" gorm:"not null;default:1"`
	// LastReason names the change that bumped the epoch, for debugging why a
	// cache entry vanished.
	LastReason string    `json:"last_reason" gorm:"type:varchar(64);not null;default:''"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// TableName pins the GORM table name.
func (PermissionVersion) TableName() string { return "permission_versions" }

// BeforeCreate assigns an ID before persistence.
func (v *PermissionVersion) BeforeCreate(_ *gorm.DB) error {
	if v.ID == "" {
		v.ID = uuid.NewString()
	}
	return nil
}

// RevocationTrigger names what caused a revocation propagation sweep.
type RevocationTrigger string

const (
	// RevocationTriggerACLRevoke is an explicit ACL row removal.
	RevocationTriggerACLRevoke RevocationTrigger = "acl_revoke"
	// RevocationTriggerACLGrant is a new deny row or narrowed allow.
	RevocationTriggerACLGrant RevocationTrigger = "acl_grant"
	// RevocationTriggerClassification is a classification change (升密).
	RevocationTriggerClassification RevocationTrigger = "classification_change"
	// RevocationTriggerMove is a cross-space move that reset document grants.
	RevocationTriggerMove RevocationTrigger = "move"
	// RevocationTriggerShareRevoke is a share-link revocation.
	RevocationTriggerShareRevoke RevocationTrigger = "share_revoke"
)

// RevocationOutlet enumerates the exits V3.0 §2.6 requires to stop showing
// content after a revocation. Each has its own target latency in the spec.
type RevocationOutlet string

const (
	RevocationOutletSearchIndex  RevocationOutlet = "search_index"
	RevocationOutletVectorIndex  RevocationOutlet = "vector_index"
	RevocationOutletGraph        RevocationOutlet = "graph"
	RevocationOutletAnswerCache  RevocationOutlet = "answer_cache"
	RevocationOutletSession      RevocationOutlet = "session_history"
	RevocationOutletExport       RevocationOutlet = "export_package"
	RevocationOutletOffline      RevocationOutlet = "offline_file"
	RevocationOutletNotification RevocationOutlet = "notification"
	RevocationOutletSuggestion   RevocationOutlet = "suggestion"
	RevocationOutletAudit        RevocationOutlet = "audit_snapshot"
)

// AllRevocationOutlets is the full §2.6 outlet list, in spec order.
var AllRevocationOutlets = []RevocationOutlet{
	RevocationOutletSearchIndex, RevocationOutletVectorIndex, RevocationOutletGraph,
	RevocationOutletAnswerCache, RevocationOutletSession, RevocationOutletExport,
	RevocationOutletOffline, RevocationOutletNotification, RevocationOutletSuggestion,
	RevocationOutletAudit,
}

// RevocationOutcome is how one outlet handled a revocation.
type RevocationOutcome string

const (
	// RevocationOutcomeInvalidated means the outlet was actively invalidated.
	RevocationOutcomeInvalidated RevocationOutcome = "invalidated"
	// RevocationOutcomeNotApplicable means the deployment has no such outlet
	// (e.g. no answer cache), which must be stated rather than left silent.
	RevocationOutcomeNotApplicable RevocationOutcome = "not_applicable"
	// RevocationOutcomePending means the outlet still needs action.
	RevocationOutcomePending RevocationOutcome = "pending"
)

// RevocationPropagation is the audit row for one outlet of one revocation.
// §2.6: "撤权的完成标准不是事件已发出，而是每个出口都验证过看不到".
type RevocationPropagation struct {
	ID                string    `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID          uint64    `json:"tenant_id" gorm:"not null"`
	KnowledgeID       string    `json:"knowledge_id" gorm:"type:varchar(36);not null;default:''"`
	SubjectID         string    `json:"subject_id" gorm:"type:varchar(64);not null;default:''"`
	Trigger           string    `json:"trigger" gorm:"type:varchar(32);not null"`
	Outlet            string    `json:"outlet" gorm:"type:varchar(32);not null"`
	Outcome           string    `json:"outcome" gorm:"type:varchar(16);not null;default:'pending'"`
	PermissionVersion int64     `json:"permission_version" gorm:"not null;default:0"`
	CreatedAt         time.Time `json:"created_at"`
}

// TableName pins the GORM table name.
func (RevocationPropagation) TableName() string { return "revocation_propagations" }

// BeforeCreate assigns an ID before persistence.
func (r *RevocationPropagation) BeforeCreate(_ *gorm.DB) error {
	if r.ID == "" {
		r.ID = uuid.NewString()
	}
	return nil
}
