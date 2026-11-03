package types

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ============================================================================
// 业务系统嵌入式上下文推送（KnowAxis V3.0 §15.5 "嵌入式学习" + §16.4 嵌入式组件）。
//
// §15.5 要求：员工在业务系统里处理工单、填表单、看设备时，平台按上下文
// 推送相关知识或微课，"学完即用，不打断工作流"，且推送同样受权限与密级
// 约束、可以关闭。
//
// 设计要点：
//  1. 推送匹配是**规则驱动**的，不是"拿上下文去猜语义"。规则明确写了
//     "MES 的 work_order、设备 E45 → 这些知识/这些标签"，可审计、可解释，
//     也不会因为向量相似度把无关文档推到工位看板上。
//  2. 推送结果必须再过三道门槛：生命周期（过期/草稿不下发）、文档 ACL
//     （deny 优先、密级无显式允许即拒绝）、目录 ACL（deny 目录整体不可见）。
//  3. 用户可以一键关闭；关闭后接口返回 enabled=false，不再产生任何曝光。
//  4. 曝光/点击/忽略都记事件，用来算规则效果，也用来抑制"用户已经明确
//     忽略过的卡片"。
// ============================================================================

// EmbedPushEventType is the interaction recorded for one pushed card.
type EmbedPushEventType string

const (
	// EmbedPushEventImpression is a card returned to the widget.
	EmbedPushEventImpression EmbedPushEventType = "impression"
	// EmbedPushEventClick is a card the user opened.
	EmbedPushEventClick EmbedPushEventType = "click"
	// EmbedPushEventDismiss is a card the user explicitly dismissed.
	EmbedPushEventDismiss EmbedPushEventType = "dismiss"
)

// Valid reports whether the event type is known.
func (t EmbedPushEventType) Valid() bool {
	switch t {
	case EmbedPushEventImpression, EmbedPushEventClick, EmbedPushEventDismiss:
		return true
	default:
		return false
	}
}

// EmbedPushRule maps a business-system context onto knowledge to suggest.
type EmbedPushRule struct {
	ID       string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID uint64 `json:"tenant_id" gorm:"not null;index:idx_embed_push_rules_tenant,priority:1"`
	// Name is the operator-facing rule label.
	Name string `json:"name" gorm:"type:varchar(128);not null"`
	// System matches the calling system (e.g. "mes"); empty matches any.
	System string `json:"system" gorm:"type:varchar(32);not null;default:''"`
	// Scene matches the business scene (e.g. "work_order"); empty matches any.
	Scene string `json:"scene" gorm:"type:varchar(64);not null;default:''"`
	// EntityType matches the entity kind (e.g. "equipment"); empty matches any.
	EntityType string `json:"entity_type" gorm:"type:varchar(64);not null;default:''"`
	// EntityValue matches the entity id exactly (case-insensitive); empty
	// matches any entity.
	EntityValue string `json:"entity_value" gorm:"type:varchar(128);not null;default:''"`
	// Attributes requires every key/value to match the context attributes.
	Attributes JSON `json:"attributes" gorm:"type:json;not null;default:'{}'"`
	// TagNames selects every knowledge item carrying one of these tags.
	TagNames JSON `json:"tag_names" gorm:"type:json;not null;default:'[]'"`
	// KnowledgeIDs pins specific knowledge items.
	KnowledgeIDs JSON `json:"knowledge_ids" gorm:"type:json;not null;default:'[]'"`
	// Reason explains why the card is being shown (displayed in the widget).
	Reason string `json:"reason" gorm:"type:varchar(255);not null;default:''"`
	// Priority orders competing rules; higher wins.
	Priority int `json:"priority" gorm:"not null;default:0"`
	// Enabled switches the rule off without deleting it.
	Enabled   bool      `json:"enabled" gorm:"not null;default:true"`
	CreatedBy string    `json:"created_by" gorm:"type:varchar(36);not null;default:''"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName pins the GORM table name.
func (EmbedPushRule) TableName() string { return "embed_push_rules" }

// BeforeCreate assigns an ID before persistence.
func (r *EmbedPushRule) BeforeCreate(_ *gorm.DB) error {
	if r.ID == "" {
		r.ID = uuid.NewString()
	}
	if len(r.Attributes) == 0 {
		r.Attributes = JSON("{}")
	}
	if len(r.TagNames) == 0 {
		r.TagNames = JSON("[]")
	}
	if len(r.KnowledgeIDs) == 0 {
		r.KnowledgeIDs = JSON("[]")
	}
	return nil
}

// EmbedPushRuleTagNames decodes the tag list, tolerating a corrupt column.
func (r *EmbedPushRule) EmbedPushRuleTagNames() []string {
	return normalizeEmbedList(decodeEmbedList(r.TagNames))
}

// EmbedPushRuleKnowledgeIDs decodes the pinned knowledge ids.
func (r *EmbedPushRule) EmbedPushRuleKnowledgeIDs() []string {
	return normalizeEmbedList(decodeEmbedList(r.KnowledgeIDs))
}

// EmbedPushRuleAttributes decodes the required attributes.
func (r *EmbedPushRule) EmbedPushRuleAttributes() map[string]string {
	if len(r.Attributes) == 0 {
		return nil
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(r.Attributes, &raw); err != nil {
		return nil
	}
	out := make(map[string]string, len(raw))
	for key, value := range raw {
		if text, ok := value.(string); ok {
			out[key] = text
		}
	}
	return out
}

// EmbedPushPreference is a user's opt-out switch (§15.5 "可关闭").
type EmbedPushPreference struct {
	ID       string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID uint64 `json:"tenant_id" gorm:"not null;uniqueIndex:uq_embed_push_pref_user,priority:1"`
	UserID   string `json:"user_id" gorm:"type:varchar(36);not null;uniqueIndex:uq_embed_push_pref_user,priority:2"`
	// Enabled defaults to true when no row exists.
	Enabled   bool      `json:"enabled" gorm:"not null"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName pins the GORM table name.
func (EmbedPushPreference) TableName() string { return "embed_push_preferences" }

// BeforeCreate assigns an ID before persistence.
func (p *EmbedPushPreference) BeforeCreate(_ *gorm.DB) error {
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	return nil
}

// EmbedPushEvent records one interaction with a pushed card.
type EmbedPushEvent struct {
	ID          string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID    uint64 `json:"tenant_id" gorm:"not null;index:idx_embed_push_events_user,priority:1"`
	UserID      string `json:"user_id" gorm:"type:varchar(36);not null;index:idx_embed_push_events_user,priority:2"`
	RuleID      string `json:"rule_id" gorm:"type:varchar(36);not null;index:idx_embed_push_events_rule,priority:1"`
	KnowledgeID string `json:"knowledge_id" gorm:"type:varchar(36);not null;default:''"`
	EventType   string `json:"event_type" gorm:"type:varchar(16);not null"`
	// Context is the caller's business context, kept for rule tuning.
	Context   JSON      `json:"context" gorm:"type:json;not null;default:'{}'"`
	CreatedAt time.Time `json:"created_at"`
}

// TableName pins the GORM table name.
func (EmbedPushEvent) TableName() string { return "embed_push_events" }

// BeforeCreate assigns an ID before persistence.
func (e *EmbedPushEvent) BeforeCreate(_ *gorm.DB) error {
	if e.ID == "" {
		e.ID = uuid.NewString()
	}
	if len(e.Context) == 0 {
		e.Context = JSON("{}")
	}
	return nil
}

// EmbedPushContext is the business context the widget reports.
type EmbedPushContext struct {
	// System is the calling business system (mes/erp/oam/crm/...).
	System string `json:"system"`
	// Scene is the business scene (work_order/form/equipment/...).
	Scene string `json:"scene"`
	// EntityType and EntityID describe the object the user is looking at.
	EntityType string `json:"entity_type"`
	EntityID   string `json:"entity_id"`
	// Attributes are extra key/value facts the rules may require.
	Attributes map[string]string `json:"attributes"`
}

// EmbedPushItem is one knowledge card returned to the widget.
type EmbedPushItem struct {
	// RuleID is the rule that selected the card.
	RuleID string `json:"rule_id"`
	// KnowledgeID / KnowledgeBaseID identify the document.
	KnowledgeID     string `json:"knowledge_id"`
	KnowledgeBaseID string `json:"knowledge_base_id"`
	// Title / Summary are display fields.
	Title   string `json:"title"`
	Summary string `json:"summary"`
	// Reason is the rule's explanation, shown as the card caption.
	Reason string `json:"reason"`
	// MatchedTag is the domain tag that matched (empty for pinned ids).
	MatchedTag string `json:"matched_tag,omitempty"`
	// Priority is the rule priority (for client-side ordering).
	Priority int `json:"priority"`
}

// EmbedPushResult is one push resolution.
type EmbedPushResult struct {
	// Enabled reports whether the user allows contextual push. false means
	// the widget must hide the panel (§15.5 可关闭).
	Enabled bool `json:"enabled"`
	// Items are the cards, already filtered by permission and lifecycle.
	Items []EmbedPushItem `json:"items"`
	// Suppressed explains an empty result: "disabled", "no_match" or
	// "no_permission". Never describes a security decision in detail.
	Suppressed string `json:"suppressed,omitempty"`
}

// EmbedPushRuleInput is the Admin create/update payload.
type EmbedPushRuleInput struct {
	Name         string            `json:"name"`
	System       string            `json:"system"`
	Scene        string            `json:"scene"`
	EntityType   string            `json:"entity_type"`
	EntityValue  string            `json:"entity_value"`
	Attributes   map[string]string `json:"attributes"`
	TagNames     []string          `json:"tag_names"`
	KnowledgeIDs []string          `json:"knowledge_ids"`
	Reason       string            `json:"reason"`
	Priority     int               `json:"priority"`
	Enabled      *bool             `json:"enabled"`
}

// EmbedPushRuleStats aggregates one rule's interaction counts.
type EmbedPushRuleStats struct {
	RuleID      string `json:"rule_id"`
	RuleName    string `json:"rule_name"`
	Impressions int64  `json:"impressions"`
	Clicks      int64  `json:"clicks"`
	Dismissals  int64  `json:"dismissals"`
}

// ValidEmbedPushRuleInput validates the rule shape shared by create and update.
//
// 规则至少要有一个知识来源：既没有标签也没有指定知识的规则永远匹配不到
// 任何内容，留着只会让运维误以为"推送没生效"。
func ValidEmbedPushRuleInput(input EmbedPushRuleInput, create bool) error {
	if create && strings.TrimSpace(input.Name) == "" {
		return errEmbedRuleName
	}
	tags := normalizeEmbedList(input.TagNames)
	ids := normalizeEmbedList(input.KnowledgeIDs)
	if create && len(tags) == 0 && len(ids) == 0 {
		return errEmbedRuleSource
	}
	if input.Priority < 0 || input.Priority > 1000 {
		return errEmbedRulePriority
	}
	return nil
}

// embedRuleError is a plain error with a stable message for validation.
type embedRuleError string

func (e embedRuleError) Error() string { return string(e) }

const (
	errEmbedRuleName     embedRuleError = "name is required"
	errEmbedRuleSource   embedRuleError = "at least one tag_name or knowledge_id is required"
	errEmbedRulePriority embedRuleError = "priority must be between 0 and 1000"
)

// normalizeEmbedList trims, dedupes and preserves order.
func normalizeEmbedList(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

// decodeEmbedList parses a JSON string array; nil/empty/malformed yields nil.
func decodeEmbedList(raw JSON) []string {
	if len(raw) == 0 {
		return nil
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil
	}
	return values
}

// EmbedPushKnowledgeMatch is one knowledge item matched to a push rule tag.
type EmbedPushKnowledgeMatch struct {
	// KnowledgeID is the matched document.
	KnowledgeID string
	// KnowledgeBaseID is its knowledge base.
	KnowledgeBaseID string
	// TagName is the tag that caused the match.
	TagName string
}

// EmbedPushDismissal identifies one dismissed card.
type EmbedPushDismissal struct {
	// RuleID is the rule that produced the card.
	RuleID string
	// KnowledgeID is the dismissed document.
	KnowledgeID string
}
