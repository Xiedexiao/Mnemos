package types

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ============================================================================
// 模型网关 v1（KnowAxis V3.0 §16.5）。
//
// 三件事：
//   1. 按密级路由：ModelRoutingRule 把"任务/数据密级"映射到模型 ID。规则是
//      覆盖层——ResolveModel 未命中时调用方回退既有默认选择（GetChatModel
//      等的入参模型 / 工作区默认模型），所以网关挂掉或没有规则时系统行为
//      与 v1 之前完全一致。
//   2. Prompt 版本化：PromptVersion 是 config/prompt_templates/ YAML 模板的
//      数据库覆盖层，版本只增不改；模板加载器后续接入（本垂直不改加载器）。
//   3. 成本记录：ModelUsageDaily 按天/模型累计 token 与调用次数（只记录，
//      不做限额与单价换算——成本限额与注入防护是后续阶段，见文件尾注释）。
// ============================================================================

// PlatformLevelTenantID marks a platform-level row: model_routing_rules and
// prompt_versions rows with tenant_id = 0 apply to every tenant unless the
// tenant defines its own. Built-in prompt template versions also live at 0.
const PlatformLevelTenantID uint64 = 0

// ModelRoutingScope is the dimension a routing rule matches on.
type ModelRoutingScope string

const (
	// ModelRoutingScopeTask matches a functional task (scope_value =
	// chat/embedding/rerank/...). Task rules are the low-precedence tier:
	// a matching classification rule wins over a matching task rule.
	ModelRoutingScopeTask ModelRoutingScope = "task"
	// ModelRoutingScopeClassification matches a data classification level
	// (scope_value = public/internal/secret/confidential). This is the
	// §16.5 "按密级路由" tier and takes precedence over task rules: a
	// secret document's chat call must land on the approved model even if
	// a task rule says otherwise.
	ModelRoutingScopeClassification ModelRoutingScope = "classification"
)

// Valid reports whether the value is a known routing scope.
func (s ModelRoutingScope) Valid() bool {
	switch s {
	case ModelRoutingScopeTask, ModelRoutingScopeClassification:
		return true
	default:
		return false
	}
}

// String renders the scope for logs.
func (s ModelRoutingScope) String() string { return string(s) }

// Model gateway task values for ModelRoutingScopeTask rules. The list is
// documentation, not a whitelist: future tasks (e.g. asr) can add rules
// without a schema change, and ResolveModel accepts any non-empty task key.
const (
	// ModelGatewayTaskChat routes conversational / completion chat calls.
	ModelGatewayTaskChat = "chat"
	// ModelGatewayTaskEmbedding routes embedding calls.
	ModelGatewayTaskEmbedding = "embedding"
	// ModelGatewayTaskRerank routes rerank calls.
	ModelGatewayTaskRerank = "rerank"
	// ModelGatewayTaskVLM routes vision-language calls.
	ModelGatewayTaskVLM = "vlm"
	// ModelGatewayTaskASR routes automatic speech recognition calls.
	ModelGatewayTaskASR = "asr"
)

// ModelRoutingRule maps one task or classification value to a model
// (KnowAxis V3.0 §16.5). Rules are configuration, not lifecycle data:
// delete is a physical delete (no soft-delete state machine here).
type ModelRoutingRule struct {
	// ID of the rule.
	ID string `json:"id" gorm:"type:varchar(36);primaryKey"`
	// TenantID owning the rule; PlatformLevelTenantID (0) marks a
	// platform-level rule that every tenant inherits. Tenant rules shadow
	// platform rules on the same (scope, scope_value).
	TenantID uint64 `json:"tenant_id" gorm:"not null;default:0"`
	// Scope selects the matching dimension (task or classification).
	Scope string `json:"scope" gorm:"type:varchar(16);not null"`
	// ScopeValue is the matched value: a task key (chat/embedding/...) or
	// a KnowledgeClassification value. Paired with TenantID + Scope for the
	// UNIQUE constraint (migration 000149).
	ScopeValue string `json:"scope_value" gorm:"type:varchar(32);not null"`
	// ModelID points at the models table ID. No FK: a deleted model leaves
	// the rule behind and the caller falls back when the ID stops resolving.
	ModelID string `json:"model_id" gorm:"type:varchar(64);not null"`
	// Enabled rules participate in ResolveModel; disabled rules are kept so
	// an admin can pause a route without losing the configuration.
	Enabled bool `json:"enabled" gorm:"not null;default:true"`
	// CreatedBy records who created the rule (audit identity).
	CreatedBy string `json:"created_by" gorm:"type:varchar(36);not null;default:''"`
	// CreatedAt of the rule.
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt of the rule (touched by GORM on Updates).
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName pins the GORM table name.
func (ModelRoutingRule) TableName() string { return "model_routing_rules" }

// BeforeCreate assigns an ID before persistence.
func (r *ModelRoutingRule) BeforeCreate(_ *gorm.DB) error {
	if r.ID == "" {
		r.ID = uuid.NewString()
	}
	return nil
}

// IsPlatformLevel reports whether the rule is platform-level (inherited by
// every tenant).
func (r *ModelRoutingRule) IsPlatformLevel() bool { return r.TenantID == PlatformLevelTenantID }

// ModelRoutingRuleUpdate carries the mutable fields of a routing rule. Scope
// and scope_value are intentionally immutable: changing them is "create the
// new rule, delete the old one", which keeps the UNIQUE key meaningful and
// the audit trail readable. Nil fields keep the current value.
type ModelRoutingRuleUpdate struct {
	// ModelID re-points the rule (nil keeps).
	ModelID *string `json:"model_id,omitempty"`
	// Enabled flips the rule on/off (nil keeps).
	Enabled *bool `json:"enabled,omitempty"`
}

// PromptVersion is one immutable version of a prompt template override
// (KnowAxis V3.0 §16.5). It overlays config/prompt_templates/: template_key
// is the YAML file name without the .yaml suffix (system_prompt, rewrite,
// graph_extraction, ...). Content is the raw prompt body; how a multi-
// template YAML file maps onto one key is the loader's concern (v1 contract:
// one key = one content body; per-template-id refinement is future work).
//
// Versions are append-only: SaveVersion always assigns version = max+1 and
// dedupes when the content equals the latest version. No UPDATE, no DELETE.
type PromptVersion struct {
	// ID of the version row.
	ID string `json:"id" gorm:"type:varchar(36);primaryKey"`
	// TenantID owning the override; PlatformLevelTenantID (0) marks a
	// built-in template version that every tenant sees.
	TenantID uint64 `json:"tenant_id" gorm:"not null;default:0"`
	// TemplateKey is the prompt_templates file name without suffix, paired
	// with TenantID + Version for the UNIQUE constraint (migration 000149).
	TemplateKey string `json:"template_key" gorm:"type:varchar(64);not null"`
	// Version number, 1-based, monotonically increasing per (tenant, key).
	Version int `json:"version" gorm:"not null"`
	// Content is the prompt body. NOT empty by contract (service-validated).
	Content string `json:"content" gorm:"type:text;not null"`
	// ChangeNote is the human-authored "why this version exists" note.
	ChangeNote string `json:"change_note" gorm:"type:text;not null;default:''"`
	// CreatedBy records who saved the version (audit identity).
	CreatedBy string `json:"created_by" gorm:"type:varchar(36);not null;default:''"`
	// CreatedAt of the version.
	CreatedAt time.Time `json:"created_at"`
}

// TableName pins the GORM table name.
func (PromptVersion) TableName() string { return "prompt_versions" }

// BeforeCreate assigns an ID before persistence.
func (v *PromptVersion) BeforeCreate(_ *gorm.DB) error {
	if v.ID == "" {
		v.ID = uuid.NewString()
	}
	return nil
}

// ModelUsageDaily accumulates one tenant's token spend and call count for one
// model on one day (KnowAxis V3.0 §16.5 成本记录 v1). RecordUsage upserts
// against UNIQUE(tenant_id, day, model_id) by adding to the counters, so the
// row is the running total, not a per-call log.
type ModelUsageDaily struct {
	// ID of the row.
	ID string `json:"id" gorm:"type:varchar(36);primaryKey"`
	// TenantID whose calls are counted.
	TenantID uint64 `json:"tenant_id" gorm:"not null"`
	// Day is the UTC calendar day (DATE column).
	Day time.Time `json:"day" gorm:"type:date;not null"`
	// ModelID is the models table ID the calls landed on. No FK: usage
	// history survives model deletion (成本对账不受模型下线影响).
	ModelID string `json:"model_id" gorm:"type:varchar(64);not null"`
	// PromptTokens accumulated prompt (input) token count.
	PromptTokens int64 `json:"prompt_tokens" gorm:"not null;default:0"`
	// CompletionTokens accumulated completion (output) token count.
	CompletionTokens int64 `json:"completion_tokens" gorm:"not null;default:0"`
	// CallCount accumulated number of recorded calls.
	CallCount int64 `json:"call_count" gorm:"not null;default:0"`
}

// TableName pins the GORM table name.
func (ModelUsageDaily) TableName() string { return "model_usage_daily" }

// BeforeCreate assigns an ID before persistence.
func (u *ModelUsageDaily) BeforeCreate(_ *gorm.DB) error {
	if u.ID == "" {
		u.ID = uuid.NewString()
	}
	return nil
}

// ModelUsageSummaryRow is one day/model bucket of the usage summary. Day is
// rendered as YYYY-MM-DD so API consumers do not have to parse RFC3339
// timestamps for a plain calendar date.
type ModelUsageSummaryRow struct {
	// Day in YYYY-MM-DD (UTC).
	Day string `json:"day"`
	// ModelID the calls landed on.
	ModelID string `json:"model_id"`
	// PromptTokens accumulated for the day/model.
	PromptTokens int64 `json:"prompt_tokens"`
	// CompletionTokens accumulated for the day/model.
	CompletionTokens int64 `json:"completion_tokens"`
	// CallCount accumulated for the day/model.
	CallCount int64 `json:"call_count"`
}

// ============================================================================
// 预留（本垂直不实现，V3.0 §16.5 后续阶段）：
//   - 调用链强制路由：GetChatModel/GetEmbeddingModel 的接线由主会话完成；
//     密级维度需要调用链把 classification 放进 context 后才有值。
//   - 成本限额：ModelUsageDaily 已有按天累计口径，限额判断（超限熔断/
//     降级）在此之上实现，不需要动表。
//   - 注入防护：Prompt 版本化是审计与回滚的基础；提示词注入检测是独立
//     的安全层，不进本表。
// ============================================================================
