package interfaces

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// ModelGatewayRepository persists the model gateway state (KnowAxis V3.0
// §16.5): routing rules, prompt template versions and daily usage totals.
type ModelGatewayRepository interface {
	// CreateRule inserts one routing rule (ID assigned by the model hook).
	// The (tenant_id, scope, scope_value) uniqueness is enforced by the
	// migration's constraint; the service pre-checks and translates the
	// duplicate into a 400.
	CreateRule(ctx context.Context, rule *types.ModelRoutingRule) error
	// UpdateRule persists the full mutable state of a loaded rule
	// (model_id / enabled; scope is immutable by contract).
	UpdateRule(ctx context.Context, rule *types.ModelRoutingRule) error
	// DeleteRule physically removes one rule. Zero rows affected means the
	// rule is gone; surfaced as gorm.ErrRecordNotFound.
	DeleteRule(ctx context.Context, tenantID uint64, ruleID string) error
	// GetRule resolves one rule by ID. A platform rule (tenant 0) is
	// visible to any tenant: the repo matches (tenant_id = ? OR tenant_id = 0).
	GetRule(ctx context.Context, tenantID uint64, ruleID string) (*types.ModelRoutingRule, error)
	// ListRules returns the tenant's own rules plus the platform-level ones,
	// ordered tenant-first then by scope/scope_value, so an admin sees the
	// full effective routing picture in one list.
	ListRules(ctx context.Context, tenantID uint64) ([]*types.ModelRoutingRule, error)
	// FindEnabledRule returns the enabled rule matching (tenantID, scope,
	// scopeValue), or (nil, nil) when there is none — not-found is the
	// normal ResolveModel path, not an error.
	FindEnabledRule(
		ctx context.Context, tenantID uint64, scope, scopeValue string,
	) (*types.ModelRoutingRule, error)
	// FindRuleByScope returns the rule matching (tenantID, scope,
	// scopeValue) regardless of enabled state — the create-path duplicate
	// check (UNIQUE tenant/scope/value pre-check).
	FindRuleByScope(
		ctx context.Context, tenantID uint64, scope, scopeValue string,
	) (*types.ModelRoutingRule, error)

	// CreatePromptVersion inserts one prompt version row (version already
	// assigned by the service).
	CreatePromptVersion(ctx context.Context, version *types.PromptVersion) error
	// LatestPromptVersion returns the highest-version row for a template
	// key, or (nil, nil) when the key has no versions yet (the loader's
	// "fall back to YAML" path).
	LatestPromptVersion(
		ctx context.Context, tenantID uint64, templateKey string,
	) (*types.PromptVersion, error)
	// GetPromptVersion resolves one exact version; gorm.ErrRecordNotFound
	// when absent.
	GetPromptVersion(
		ctx context.Context, tenantID uint64, templateKey string, version int,
	) (*types.PromptVersion, error)
	// ListPromptVersions returns all versions of one key, newest first.
	ListPromptVersions(
		ctx context.Context, tenantID uint64, templateKey string,
	) ([]*types.PromptVersion, error)

	// UpsertUsage adds one call's tokens to the (tenant, day, model, scene,
	// knowledge base) accumulator row, creating it on first touch. The
	// conflict target is uq_model_usage_daily_tenant_day_model_scene_space
	// (migration 000172).
	UpsertUsage(
		ctx context.Context, day time.Time, tenantID uint64, modelID, scene, knowledgeBaseID string,
		promptTokens, completionTokens int64,
	) error
	// ListUsage returns the accumulator rows for one tenant since (and
	// including) the given day, ordered by day then model.
	ListUsage(
		ctx context.Context, tenantID uint64, sinceDay time.Time,
	) ([]*types.ModelUsageDaily, error)
}

// ModelGatewayService is the model gateway vertical (KnowAxis V3.0 §16.5):
// routing-rule CRUD, prompt template versioning and usage recording.
//
// 设计边界（本垂直不实现，见 types/model_gateway.go 尾部预留注释）：
//   - 调用链强制路由由主会话在 GetChatModel/GetEmbeddingModel 接线
//     （ResolveModel 未命中返回 ""，调用方回退默认模型）；
//   - 成本限额、注入防护是后续阶段。
type ModelGatewayService interface {
	// CreateRule creates one routing rule (audit: model_gateway.rule_created).
	CreateRule(ctx context.Context, rule *types.ModelRoutingRule) (*types.ModelRoutingRule, error)
	// UpdateRule patches model_id / enabled of one rule
	// (audit: model_gateway.rule_updated).
	UpdateRule(
		ctx context.Context, tenantID uint64, ruleID string,
		input *types.ModelRoutingRuleUpdate, actorID string,
	) (*types.ModelRoutingRule, error)
	// DeleteRule removes one rule (audit: model_gateway.rule_deleted).
	DeleteRule(ctx context.Context, tenantID uint64, ruleID, actorID string) error
	// GetRule resolves one rule (own tenant's or platform-level).
	GetRule(ctx context.Context, tenantID uint64, ruleID string) (*types.ModelRoutingRule, error)
	// ListRules lists the tenant's rules plus platform-level rules.
	ListRules(ctx context.Context, tenantID uint64) ([]*types.ModelRoutingRule, error)
	// ResolveModel maps a task (and optional data classification) to a model
	// ID. Lookup order: classification rules first (密级优先), then task
	// rules; within a tier the tenant's own rule shadows the platform-level
	// (tenant 0) rule. Returns "" when nothing matches — the caller then
	// falls back to its default model selection (既有 GetChatModel/
	// GetEmbeddingModel 的入参模型 / 工作区默认), so an empty gateway is
	// behaviourally identical to "no gateway". classification accepts the
	// KnowledgeClassification string values ("public"/"internal"/"secret"/
	// "confidential"); unknown values are normalised to internal (same
	// policy as types.ParseKnowledgeClassification). 传 "" 表示本次调用
	// 不带密级信息，只按任务路由。
	ResolveModel(ctx context.Context, tenantID uint64, task, classification string) string

	// SaveVersion appends one prompt template version: version = 当前最大+1,
	// content identical to the latest version dedupes (idempotent, returns
	// the existing row). Versions are append-only — no update, no delete
	// (audit: model_gateway.prompt_version_created).
	SaveVersion(ctx context.Context, version *types.PromptVersion) (*types.PromptVersion, error)
	// ListVersions lists all versions of one template key, newest first
	// (built-in tenant-0 versions are NOT included — pass
	// types.PlatformLevelTenantID to read those).
	ListVersions(ctx context.Context, tenantID uint64, templateKey string) ([]*types.PromptVersion, error)
	// GetVersion resolves one exact version (404 when absent).
	GetVersion(ctx context.Context, tenantID uint64, templateKey string, version int) (*types.PromptVersion, error)
	// LatestVersion returns the highest version of one template key, or
	// (nil, nil) when the key has no versions yet — the template loader's
	// "fall back to the YAML built-in" path.
	LatestVersion(ctx context.Context, tenantID uint64, templateKey string) (*types.PromptVersion, error)

	// RecordUsage adds one chat call's tokens to the day accumulator
	// (UTC day), attributed to the chat scene. Kept for callers that do not
	// know a space; new call sites should use RecordUsageDetailed.
	// Never fails the business call: callers may ignore the error (log-only),
	// which is why it returns error instead of panicking on bad input —
	// negative counters are clamped to 0.
	RecordUsage(ctx context.Context, tenantID uint64, modelID string, promptTokens, completionTokens int64) error
	// RecordUsageDetailed adds one model call's tokens with the §16.1
	// allocation dimensions: scenario and knowledge base. An empty
	// knowledgeBaseID means "not attributable to one space" and is reported
	// as unallocated rather than guessed.
	RecordUsageDetailed(
		ctx context.Context, tenantID uint64, modelID, scene, knowledgeBaseID string,
		promptTokens, completionTokens int64,
	) error
	// UsageSummary aggregates the last `days` days (UTC, inclusive of
	// today) by day and model, day ascending. days is clamped to [1, 366].
	UsageSummary(ctx context.Context, tenantID uint64, days int) ([]*types.ModelUsageSummaryRow, error)
}
