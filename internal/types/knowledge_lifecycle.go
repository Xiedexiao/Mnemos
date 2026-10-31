package types

import (
	"fmt"
	"time"
)

// Knowledge lifecycle states (KnowAxis V3.0 §7.1). The state decides how a
// knowledge entry behaves in search, QA, notifications and the reading page:
// a state is a behaviour switch, not a label.
type KnowledgeLifecycleStatus string

const (
	// KnowledgeLifecycleDraft 未提交审核，仅作者可见，不检索。
	KnowledgeLifecycleDraft KnowledgeLifecycleStatus = "draft"
	// KnowledgeLifecyclePendingReview 已提交，审批中；不进入公共检索。
	KnowledgeLifecyclePendingReview KnowledgeLifecycleStatus = "pending_review"
	// KnowledgeLifecyclePendingEffective 已批准但未到生效日；问答明确提示"尚未生效"。
	KnowledgeLifecyclePendingEffective KnowledgeLifecycleStatus = "pending_effective"
	// KnowledgeLifecycleValid 当前可用；正常检索与问答。
	KnowledgeLifecycleValid KnowledgeLifecycleStatus = "valid"
	// KnowledgeLifecyclePendingReviewCycle 到达复审周期；可检索但降权并提示。
	KnowledgeLifecyclePendingReviewCycle KnowledgeLifecycleStatus = "pending_review_cycle"
	// KnowledgeLifecycleStale 出现过期信号；不作为权威答案。
	KnowledgeLifecycleStale KnowledgeLifecycleStatus = "stale"
	// KnowledgeLifecycleExpired 确认失效；默认不回答，历史查询可见。
	KnowledgeLifecycleExpired KnowledgeLifecycleStatus = "expired"
	// KnowledgeLifecycleReplaced 有新版本替代；指向新版本。
	KnowledgeLifecycleReplaced KnowledgeLifecycleStatus = "replaced"
	// KnowledgeLifecycleConflict 与其他权威知识矛盾；展示双方不下结论。
	KnowledgeLifecycleConflict KnowledgeLifecycleStatus = "conflict"
	// KnowledgeLifecycleDelisted 主动下架；立即排除，历史与审计可见。
	KnowledgeLifecycleDelisted KnowledgeLifecycleStatus = "delisted"
	// KnowledgeLifecycleArchived 归档留存；默认不检索，归档库按需查询。
	KnowledgeLifecycleArchived KnowledgeLifecycleStatus = "archived"
)

// knowledgeLifecycleTransitions lists the legal transitions of the lifecycle
// state machine. "有效→已过期"等不可逆/降级行为必须经由显式动作触发，系统
// 不做静默降级（V3.0 §7.1 硬规则）；到期降级通过 ResolveLifecycleBehavior
// 在读取时计算，不直接改库。
var knowledgeLifecycleTransitions = map[KnowledgeLifecycleStatus][]KnowledgeLifecycleStatus{
	KnowledgeLifecycleDraft: {KnowledgeLifecyclePendingReview, KnowledgeLifecycleArchived, KnowledgeLifecycleDelisted},
	KnowledgeLifecyclePendingReview: {
		KnowledgeLifecyclePendingEffective, KnowledgeLifecycleValid, // 审批通过（立即生效 / 未来生效）
		KnowledgeLifecycleDraft, KnowledgeLifecycleDelisted, // 驳回退回 / 撤回
	},
	KnowledgeLifecyclePendingEffective: {
		KnowledgeLifecycleValid, KnowledgeLifecyclePendingReview, KnowledgeLifecycleDraft, KnowledgeLifecycleDelisted,
	},
	KnowledgeLifecycleValid: {
		KnowledgeLifecyclePendingReviewCycle, KnowledgeLifecycleStale, KnowledgeLifecycleExpired,
		KnowledgeLifecycleReplaced, KnowledgeLifecycleConflict, KnowledgeLifecycleDelisted,
		KnowledgeLifecycleArchived, KnowledgeLifecyclePendingReview, // 更新走变更审核
	},
	KnowledgeLifecyclePendingReviewCycle: {
		KnowledgeLifecycleValid, KnowledgeLifecycleStale, KnowledgeLifecycleExpired,
		KnowledgeLifecycleReplaced, KnowledgeLifecycleDelisted, KnowledgeLifecycleArchived,
		KnowledgeLifecyclePendingReview,
	},
	KnowledgeLifecycleStale: {
		KnowledgeLifecycleValid, KnowledgeLifecycleExpired, KnowledgeLifecycleReplaced,
		KnowledgeLifecycleConflict, KnowledgeLifecycleDelisted, KnowledgeLifecycleArchived, KnowledgeLifecyclePendingReview,
	},
	KnowledgeLifecycleExpired:    {KnowledgeLifecycleValid, KnowledgeLifecycleArchived, KnowledgeLifecycleDelisted},
	KnowledgeLifecycleReplaced:   {KnowledgeLifecycleArchived, KnowledgeLifecycleDelisted},
	KnowledgeLifecycleConflict:   {KnowledgeLifecycleValid, KnowledgeLifecycleStale, KnowledgeLifecycleDelisted, KnowledgeLifecycleArchived},
	KnowledgeLifecycleDelisted:   {KnowledgeLifecycleValid, KnowledgeLifecycleArchived}, // 恢复需走审批
	KnowledgeLifecycleArchived:   {KnowledgeLifecycleValid, KnowledgeLifecycleDelisted},
}

// CanTransitionTo reports whether moving from one lifecycle state to another
// is legal. Every state change must go through KnowledgeLifecycleService so
// the transition stays auditable.
func (s KnowledgeLifecycleStatus) CanTransitionTo(next KnowledgeLifecycleStatus) bool {
	if s == next {
		return false
	}
	for _, allowed := range knowledgeLifecycleTransitions[s] {
		if allowed == next {
			return true
		}
	}
	return false
}

// Valid reports whether the value is a known lifecycle state.
func (s KnowledgeLifecycleStatus) Valid() bool {
	switch s {
	case KnowledgeLifecycleDraft, KnowledgeLifecyclePendingReview, KnowledgeLifecyclePendingEffective,
		KnowledgeLifecycleValid, KnowledgeLifecyclePendingReviewCycle, KnowledgeLifecycleStale,
		KnowledgeLifecycleExpired, KnowledgeLifecycleReplaced, KnowledgeLifecycleConflict,
		KnowledgeLifecycleDelisted, KnowledgeLifecycleArchived:
		return true
	default:
		return false
	}
}

// Searchable reports whether entries in this state appear in search results.
func (s KnowledgeLifecycleStatus) Searchable() bool {
	switch s {
	case KnowledgeLifecycleValid, KnowledgeLifecyclePendingReviewCycle, KnowledgeLifecycleStale:
		return true
	default:
		return false
	}
}

// Askable reports whether entries in this state may back a QA answer.
// Stale entries may still be retrieved but never serve as authoritative
// evidence; callers must surface the freshness caveat.
func (s KnowledgeLifecycleStatus) Askable() bool {
	switch s {
	case KnowledgeLifecycleValid, KnowledgeLifecyclePendingReviewCycle, KnowledgeLifecycleStale:
		return true
	default:
		return false
	}
}

// Authoritative reports whether the state may back high-confidence answers.
func (s KnowledgeLifecycleStatus) Authoritative() bool {
	return s == KnowledgeLifecycleValid
}

// KnowledgeLifecycleSearchableStatuses is the default include-set for
// retrieval filters: everything a regular user may find through search.
var KnowledgeLifecycleSearchableStatuses = []KnowledgeLifecycleStatus{
	KnowledgeLifecycleValid, KnowledgeLifecyclePendingReviewCycle, KnowledgeLifecycleStale,
}

// KnowledgeClassification 数据密级（KnowAxis V3.0 §2.4）。可见范围 = 密级允许 ∩ ACL 允许。
type KnowledgeClassification string

const (
	// KnowledgeClassificationPublic 公开：可对外发布。
	KnowledgeClassificationPublic KnowledgeClassification = "public"
	// KnowledgeClassificationInternal 内部：全体员工可见，外发需审批。
	KnowledgeClassificationInternal KnowledgeClassification = "internal"
	// KnowledgeClassificationSecret 秘密：授权部门/项目组，默认禁止外发。
	KnowledgeClassificationSecret KnowledgeClassification = "secret"
	// KnowledgeClassificationConfidential 机密：指定人员，禁止导出。
	KnowledgeClassificationConfidential KnowledgeClassification = "confidential"
)

// Valid reports whether the value is a known classification level. The zero
// value "" is treated as internal for backwards compatibility.
func (c KnowledgeClassification) Valid() bool {
	switch c {
	case KnowledgeClassificationPublic, KnowledgeClassificationInternal,
		KnowledgeClassificationSecret, KnowledgeClassificationConfidential:
		return true
	default:
		return false
	}
}

// Rank returns the sensitivity ordering: public(0) < internal(1) < secret(2) <
// confidential(3). A derived artifact must not be more visible than its most
// restrictive source evidence.
func (c KnowledgeClassification) Rank() int {
	switch c {
	case KnowledgeClassificationPublic:
		return 0
	case KnowledgeClassificationInternal, "":
		return 1
	case KnowledgeClassificationSecret:
		return 2
	case KnowledgeClassificationConfidential:
		return 3
	default:
		return 1
	}
}

// AtLeast reports whether c is at or above the given sensitivity.
func (c KnowledgeClassification) AtLeast(other KnowledgeClassification) bool {
	return c.Rank() >= other.Rank()
}

// KnowledgeLifecycleResolved is the effective behaviour of a knowledge entry
// at a point in time. Scheduled transitions (effective/expiry dates) are
// evaluated lazily on read instead of being written back by a cron job, so a
// paused worker can never silently flip states (V3.0 §7.1 hard rule).
type KnowledgeLifecycleResolved struct {
	// Status is the stored lifecycle_status.
	Status KnowledgeLifecycleStatus
	// EffectiveStatus applies scheduled effective/expiry windows on top of
	// Status. It is what search/QA/UI should act on.
	EffectiveStatus KnowledgeLifecycleStatus
	// Reasons lists the machine-readable signals that produced EffectiveStatus
	// (e.g. "not_yet_effective", "expires_at_reached"), for UI badges.
	Reasons []string
}

// ResolveLifecycle computes the effective behaviour of one knowledge entry at
// the given instant.
func ResolveLifecycle(status KnowledgeLifecycleStatus, effectiveAt, expiresAt *time.Time, now time.Time) KnowledgeLifecycleResolved {
	resolved := KnowledgeLifecycleResolved{Status: status, EffectiveStatus: status}
	switch status {
	case KnowledgeLifecyclePendingEffective:
		if effectiveAt != nil && !now.Before(*effectiveAt) {
			resolved.EffectiveStatus = KnowledgeLifecycleValid
			resolved.Reasons = append(resolved.Reasons, "effective_date_reached")
		}
	case KnowledgeLifecycleValid:
		if expiresAt != nil && now.After(*expiresAt) {
			// 到期自动失效是读取时行为：默认不回答、历史可查。
			resolved.EffectiveStatus = KnowledgeLifecycleExpired
			resolved.Reasons = append(resolved.Reasons, "expires_at_reached")
		} else if effectiveAt != nil && now.Before(*effectiveAt) {
			resolved.EffectiveStatus = KnowledgeLifecyclePendingEffective
			resolved.Reasons = append(resolved.Reasons, "not_yet_effective")
		}
	}
	return resolved
}

// ParseKnowledgeLifecycleStatus normalises any stored value into a known
// status; legacy/unknown rows count as valid so old deployments keep working.
func ParseKnowledgeLifecycleStatus(raw string) KnowledgeLifecycleStatus {
	s := KnowledgeLifecycleStatus(raw)
	if s.Valid() {
		return s
	}
	return KnowledgeLifecycleValid
}

// ParseKnowledgeClassification normalises a stored classification; empty or
// unknown values fall back to internal (V3.0 default).
func ParseKnowledgeClassification(raw string) KnowledgeClassification {
	c := KnowledgeClassification(raw)
	if c.Valid() {
		return c
	}
	return KnowledgeClassificationInternal
}

// KnowledgeVersion is an immutable snapshot of one published version of a
// knowledge entry (V3.0 §7.4). Answers cite "依据版本 + 生效日期"; historical
// versions stay queryable but never back current answers.
type KnowledgeVersion struct {
	ID          string     `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID    uint64     `json:"tenant_id" gorm:"type:integer;not null"`
	KnowledgeID string     `json:"knowledge_id" gorm:"type:varchar(36);not null;index:idx_knowledge_versions_knowledge,priority:1"`
	Version     int        `json:"version" gorm:"not null;index:idx_knowledge_versions_knowledge,priority:2,sort:desc"`
	ContentHash string     `json:"content_hash" gorm:"type:varchar(128);not null"`
	Title       string     `json:"title" gorm:"type:varchar(255);not null;default:''"`
	ChangeNote  string     `json:"change_note" gorm:"type:text;not null;default:''"`
	Snapshot    JSON       `json:"snapshot" gorm:"type:json;not null"`
	PublishedBy string     `json:"published_by" gorm:"type:varchar(36);not null;default:''"`
	ReceiptID   string     `json:"receipt_id" gorm:"type:varchar(64);not null;default:''"`
	EffectiveAt *time.Time `json:"effective_at"`
	CreatedAt   time.Time  `json:"created_at"`

	// Specified marks fields explicitly provided on partial updates.
	ChangeNoteSpecified bool `json:"-" gorm:"-"`
}

// TableName pins the GORM table name.
func (KnowledgeVersion) TableName() string { return "knowledge_versions" }

// KnowledgeLifecycleAction names one lifecycle state-machine action. Actions
// are the only way states change; every application writes an audit event.
type KnowledgeLifecycleAction string

const (
	// KnowledgeLifecycleActionSubmitReview 提交审核（进入 pending_review）。
	KnowledgeLifecycleActionSubmitReview KnowledgeLifecycleAction = "submit_review"
	// KnowledgeLifecycleActionPublish 发布/生效（审批通过后调用；写版本快照）。
	KnowledgeLifecycleActionPublish KnowledgeLifecycleAction = "publish"
	// KnowledgeLifecycleActionRejectReview 驳回退回草稿。
	KnowledgeLifecycleActionRejectReview KnowledgeLifecycleAction = "reject_review"
	// KnowledgeLifecycleActionConfirmReview 复审确认有效（更新 last_reviewed_*）。
	KnowledgeLifecycleActionConfirmReview KnowledgeLifecycleAction = "confirm_review"
	// KnowledgeLifecycleActionMarkStale 标记疑似过期。
	KnowledgeLifecycleActionMarkStale KnowledgeLifecycleAction = "mark_stale"
	// KnowledgeLifecycleActionMarkExpired 确认过期。
	KnowledgeLifecycleActionMarkExpired KnowledgeLifecycleAction = "mark_expired"
	// KnowledgeLifecycleActionReplace 标记被替代（指向新版本）。
	KnowledgeLifecycleActionReplace KnowledgeLifecycleAction = "replace"
	// KnowledgeLifecycleActionMarkConflict 标记冲突。
	KnowledgeLifecycleActionMarkConflict KnowledgeLifecycleAction = "mark_conflict"
	// KnowledgeLifecycleActionDelist 下架。
	KnowledgeLifecycleActionDelist KnowledgeLifecycleAction = "delist"
	// KnowledgeLifecycleActionArchive 归档。
	KnowledgeLifecycleActionArchive KnowledgeLifecycleAction = "archive"
	// KnowledgeLifecycleActionRestore 恢复为有效（下架/归档/过期的逆操作，需审批）。
	KnowledgeLifecycleActionRestore KnowledgeLifecycleAction = "restore"
	// KnowledgeLifecycleActionUpdateSettings 更新治理配置（密级/生效期/复审周期）。
	KnowledgeLifecycleActionUpdateSettings KnowledgeLifecycleAction = "update_settings"
)

// KnowledgeLifecycleTransitionInput carries one lifecycle action's parameters.
type KnowledgeLifecycleTransitionInput struct {
	// KnowledgeID of the target entry.
	KnowledgeID string
	// Action to apply; validated against the state machine.
	Action KnowledgeLifecycleAction
	// ActorID is the authenticated user performing the action.
	ActorID string
	// ReplacedByID is required for the replace action.
	ReplacedByID string
	// ChangeNote is stored on the published version snapshot.
	ChangeNote string
	// ReceiptID links a publish action to its approval receipt (A2).
	ReceiptID string
	// Classification / EffectiveAt / ExpiresAt / ReviewCycleDays apply to the
	// update_settings action. Pointers distinguish "unset" from zero.
	Classification  *KnowledgeClassification
	EffectiveAt     *time.Time
	ExpiresAt       *time.Time
	ReviewCycleDays *int
}

// String renders the lifecycle status for logs.
func (s KnowledgeLifecycleStatus) String() string { return string(s) }

// Describe returns a human-readable explanation of the resolved behaviour,
// used by UI badges and QA freshness caveats.
func (r KnowledgeLifecycleResolved) Describe() string {
	switch r.EffectiveStatus {
	case KnowledgeLifecyclePendingEffective:
		return "尚未生效"
	case KnowledgeLifecyclePendingReviewCycle:
		return "待复审"
	case KnowledgeLifecycleStale:
		return "疑似过期，不作为权威答案"
	case KnowledgeLifecycleExpired:
		return "已过期，默认不回答"
	case KnowledgeLifecycleReplaced:
		return "已被新版本替代"
	case KnowledgeLifecycleConflict:
		return "存在冲突版本"
	case KnowledgeLifecycleDelisted:
		return "已下架"
	case KnowledgeLifecycleArchived:
		return "已归档"
	case KnowledgeLifecycleValid:
		return "有效"
	default:
		return fmt.Sprintf("状态: %s", r.EffectiveStatus)
	}
}
