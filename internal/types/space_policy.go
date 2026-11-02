package types

import (
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ============================================================================
// 空间类型与目录级权限（KnowAxis V3.0 §4.1 空间 / §2.3 权限继承与上限）。
//
// 平台此前只有"知识库"一个容器概念：没有个人/团队/公共/项目/外部协作的
// 类型区分，也就没有各自的默认策略；目录只是 folder_path 字符串，不是
// 策略节点，"目录单独授权、子目录继承、移动触发重算"都无处落地。
// ============================================================================

// SpaceKind is the type of a knowledge space (§4.1 五种空间).
type SpaceKind string

const (
	// SpaceKindPersonal 个人空间：仅本人可见。
	SpaceKindPersonal SpaceKind = "personal"
	// SpaceKindTeam 团队空间：团队成员默认可读。
	SpaceKindTeam SpaceKind = "team"
	// SpaceKindPublic 公共空间：全公司可读，发布需审核。
	SpaceKindPublic SpaceKind = "public"
	// SpaceKindProject 项目空间：项目成员自动授权，关闭后转只读。
	SpaceKindProject SpaceKind = "project"
	// SpaceKindExternalCollab 外部协作空间：访客身份、范围受限、到期失效。
	SpaceKindExternalCollab SpaceKind = "external_collab"
)

// Valid reports whether the kind is known.
func (k SpaceKind) Valid() bool {
	switch k {
	case SpaceKindPersonal, SpaceKindTeam, SpaceKindPublic, SpaceKindProject, SpaceKindExternalCollab:
		return true
	default:
		return false
	}
}

// ParseSpaceKind normalises a stored value; unknown values fall back to team so
// legacy rows keep working.
func ParseSpaceKind(raw string) SpaceKind {
	kind := SpaceKind(raw)
	if kind.Valid() {
		return kind
	}
	return SpaceKindTeam
}

// DefaultClassification is the space kind's classification floor (§2.4 定密：
// 无确认时按空间默认密级处理).
//
// The floors are deliberately conservative: an external-collaboration space and
// a project space never default to "public" because their contents usually
// originate from customers or unreleased work.
func (k SpaceKind) DefaultClassification() KnowledgeClassification {
	switch k {
	case SpaceKindPublic:
		return KnowledgeClassificationPublic
	case SpaceKindExternalCollab, SpaceKindProject:
		return KnowledgeClassificationInternal
	default:
		return KnowledgeClassificationInternal
	}
}

// RequiresReview reports whether content in this space kind must pass review
// before publishing (§4.1 公共空间"发布需审核").
func (k SpaceKind) RequiresReview() bool {
	return k == SpaceKindPublic || k == SpaceKindExternalCollab
}

// SpacePolicy is the effective default policy of a space, derived from its kind
// unless the operator overrode a field.
type SpacePolicy struct {
	// Kind of the space.
	Kind SpaceKind `json:"kind"`
	// DefaultClassification applies to new content without an explicit
	// classification.
	DefaultClassification KnowledgeClassification `json:"default_classification"`
	// RequireReview forces new content through the approval flow.
	RequireReview bool `json:"require_review"`
	// ReadOnlyAfterExpiry turns the space read-only when ExpiresAt passes
	// (project close / external NDA expiry).
	ReadOnlyAfterExpiry bool `json:"read_only_after_expiry"`
	// ExpiresAt is the space's own expiry, if any.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// ResolveSpacePolicy merges the kind's defaults with explicit overrides.
func ResolveSpacePolicy(kind SpaceKind, defaultClassification, expiresAt interface{}) SpacePolicy {
	policy := SpacePolicy{
		Kind:                  kind,
		DefaultClassification: kind.DefaultClassification(),
		RequireReview:         kind.RequiresReview(),
		ReadOnlyAfterExpiry:   kind == SpaceKindProject || kind == SpaceKindExternalCollab,
	}
	if raw, ok := defaultClassification.(string); ok {
		if parsed := ParseKnowledgeClassification(raw); parsed.Valid() &&
			raw != "" {
			policy.DefaultClassification = parsed
		}
	}
	if expiry, ok := expiresAt.(*time.Time); ok {
		policy.ExpiresAt = expiry
	}
	return policy
}

// SpaceReadOnly reports whether the space has passed its expiry and must reject
// writes (§4.1 项目关闭后转只读).
func (p SpacePolicy) SpaceReadOnly(now time.Time) bool {
	if !p.ReadOnlyAfterExpiry || p.ExpiresAt == nil {
		return false
	}
	return now.After(*p.ExpiresAt)
}

// FolderACL is one directory-level grant (§2.3 目录单独授权).
//
// Directories are identified by their normalised path within a knowledge base,
// matching the folder_path stored on knowledge rows. A grant inherits downward
// unless inherit_children is cleared.
type FolderACL struct {
	ID              string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID        uint64 `json:"tenant_id" gorm:"not null"`
	KnowledgeBaseID string `json:"knowledge_base_id" gorm:"type:varchar(36);not null"`
	FolderPath      string `json:"folder_path" gorm:"type:varchar(1024);not null;default:''"`
	// SubjectType: user (group/role reserved).
	SubjectType string `json:"subject_type" gorm:"type:varchar(16);not null;default:'user'"`
	SubjectID   string `json:"subject_id" gorm:"type:varchar(64);not null"`
	Action      string `json:"action" gorm:"type:varchar(24);not null;default:'view'"`
	Effect      string `json:"effect" gorm:"type:varchar(8);not null;default:'allow'"`
	// InheritChildren makes the grant apply to descendant folders.
	InheritChildren bool       `json:"inherit_children" gorm:"not null;default:true"`
	GrantedBy       string     `json:"granted_by" gorm:"type:varchar(36);not null;default:''"`
	ExpiresAt       *time.Time `json:"expires_at"`
	CreatedAt       time.Time  `json:"created_at"`
}

// TableName pins the GORM table name.
func (FolderACL) TableName() string { return "folder_acl" }

// BeforeCreate assigns an ID before persistence.
func (a *FolderACL) BeforeCreate(_ *gorm.DB) error {
	if a.ID == "" {
		a.ID = uuid.NewString()
	}
	return nil
}

// Applies reports whether this grant covers the given folder path.
func (a *FolderACL) Applies(folderPath string) bool {
	if a == nil {
		return false
	}
	if a.FolderPath == "" {
		// 根目录授权：除非显式不继承，否则覆盖全库。
		return a.InheritChildren || folderPath == ""
	}
	if a.FolderPath == folderPath {
		return true
	}
	return a.InheritChildren && strings.HasPrefix(folderPath, a.FolderPath+"/")
}

// FolderACLDecision is the folded verdict for one folder path.
type FolderACLDecision struct {
	// FolderPath the verdict applies to.
	FolderPath string `json:"folder_path"`
	// AllowedActions after folding all applicable grants (deny wins).
	AllowedActions []PermissionAction `json:"allowed_actions"`
	// DeniedActions after folding.
	DeniedActions []PermissionAction `json:"denied_actions"`
	// InheritedFrom names the folder whose grant decided this verdict (empty
	// when the folder's own grant decided it).
	InheritedFrom string `json:"inherited_from,omitempty"`
}

// FoldFolderACL computes the effective verdict for one folder from the grants
// that apply to it.
//
// Deny wins over allow at every level (§2.1 deny 优先), and a verdict inherited
// from an ancestor is reported so the UI can explain where an unexpected
// permission came from.
func FoldFolderACL(folderPath string, grants []*FolderACL, now time.Time) FolderACLDecision {
	decision := FolderACLDecision{FolderPath: NormalizeKnowledgeFolderPath(folderPath)}
	allowed := make(map[PermissionAction]bool)
	denied := make(map[PermissionAction]bool)
	inheritedFrom := ""
	for _, grant := range grants {
		if grant == nil || !grant.Applies(decision.FolderPath) {
			continue
		}
		if grant.ExpiresAt != nil && now.After(*grant.ExpiresAt) {
			continue
		}
		action := PermissionAction(grant.Action)
		if !action.Valid() {
			continue
		}
		if grant.Effect == string(KnowledgeACLEffectDeny) {
			denied[action] = true
			continue
		}
		allowed[action] = true
		// 记录"这条判定来自哪一层"，只在授权确实来自祖先时填。
		if grant.FolderPath != decision.FolderPath && inheritedFrom == "" {
			inheritedFrom = grant.FolderPath
		}
	}
	// deny 优先：任何 deny 命中即从允许集合中移除。
	for action := range denied {
		delete(allowed, action)
	}
	for action := range allowed {
		decision.AllowedActions = append(decision.AllowedActions, action)
	}
	for action := range denied {
		decision.DeniedActions = append(decision.DeniedActions, action)
	}
	sort.Slice(decision.AllowedActions, func(i, j int) bool {
		return decision.AllowedActions[i] < decision.AllowedActions[j]
	})
	sort.Slice(decision.DeniedActions, func(i, j int) bool {
		return decision.DeniedActions[i] < decision.DeniedActions[j]
	})
	decision.InheritedFrom = inheritedFrom
	return decision
}

// ACLCeilingViolation records a case where a child grant exceeds its parent
// (§2.3 上限规则：子级授权 ⊆ 父级允许).
type ACLCeilingViolation struct {
	ID              string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID        uint64 `json:"tenant_id" gorm:"not null"`
	KnowledgeBaseID string `json:"knowledge_base_id" gorm:"type:varchar(36);not null"`
	// ResourceType is "knowledge" or "folder".
	ResourceType string `json:"resource_type" gorm:"type:varchar(16);not null"`
	ResourceID   string `json:"resource_id" gorm:"type:varchar(1024);not null"`
	// Kind is subject_ceiling | classification_ceiling | action_ceiling.
	Kind       string    `json:"kind" gorm:"type:varchar(32);not null"`
	Detail     JSON      `json:"detail" gorm:"type:json;not null;default:'{}'"`
	DetectedAt time.Time `json:"detected_at"`
}

// TableName pins the GORM table name.
func (ACLCeilingViolation) TableName() string { return "acl_ceiling_violations" }

// BeforeCreate assigns an ID before persistence.
func (v *ACLCeilingViolation) BeforeCreate(_ *gorm.DB) error {
	if v.ID == "" {
		v.ID = uuid.NewString()
	}
	return nil
}

// CeilingViolationKind names the three ceiling rules.
type CeilingViolationKind string

const (
	// CeilingSubject child grants a subject the parent does not allow.
	CeilingSubject CeilingViolationKind = "subject_ceiling"
	// CeilingClassification child's classification is lower than the parent's
	// floor (a high-classification parent must not contain low-classification
	// children without approval).
	CeilingClassification CeilingViolationKind = "classification_ceiling"
	// CeilingAction child grants an action the parent does not open.
	CeilingAction CeilingViolationKind = "action_ceiling"
)

// CheckActionCeiling reports the actions a child grant list adds beyond the
// parent's allowed set (§2.3 动作上限).
func CheckActionCeiling(parentAllowed, childAllowed []PermissionAction) []PermissionAction {
	parent := make(map[PermissionAction]bool, len(parentAllowed))
	for _, action := range parentAllowed {
		parent[action] = true
	}
	var violations []PermissionAction
	for _, action := range childAllowed {
		if !parent[action] {
			violations = append(violations, action)
		}
	}
	return violations
}

// CheckClassificationCeiling reports whether a child classification breaches the
// parent floor.
//
// "不得低于父级要求" means a child may not be *less* restricted than its parent:
// a 秘密 child inside a 机密 parent is a violation because the parent's floor
// exists to keep that content contained.
func CheckClassificationCeiling(parentFloor, child KnowledgeClassification) bool {
	return child.Rank() < parentFloor.Rank()
}
