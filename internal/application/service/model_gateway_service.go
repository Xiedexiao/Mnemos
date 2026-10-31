package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Model gateway audit actions (KnowAxis V3.0 §16.5: 路由与 Prompt 版本都是
// 平台治理面的一等事件；usage 记录是遥测，不进审计流).
const (
	auditActionModelGatewayRuleCreated   = "model_gateway.rule_created"
	auditActionModelGatewayRuleUpdated   = "model_gateway.rule_updated"
	auditActionModelGatewayRuleDeleted   = "model_gateway.rule_deleted"
	auditActionModelGatewayPromptCreated = "model_gateway.prompt_version_created"
)

// Length caps mirroring the migration's column widths, plus content caps that
// keep a single admin request from writing a pathological row.
const (
	maxModelGatewayScopeValueLen    = 32
	maxModelGatewayModelIDLen       = 64
	maxModelGatewayTemplateKeyLen   = 64
	maxModelGatewayChangeNoteLen    = 2000
	maxModelGatewayPromptContentLen = 200000
	// maxModelGatewaySummaryDays bounds UsageSummary so a ?days=100000
	// request cannot turn into a full-table scan.
	maxModelGatewaySummaryDays = 366
)

// modelGatewayService implements interfaces.ModelGatewayService.
type modelGatewayService struct {
	repo  interfaces.ModelGatewayRepository
	audit interfaces.AuditLogService
	now   func() time.Time
}

// NewModelGatewayService constructs the model gateway service.
func NewModelGatewayService(
	repo interfaces.ModelGatewayRepository,
	audit interfaces.AuditLogService,
) interfaces.ModelGatewayService {
	return &modelGatewayService{repo: repo, audit: audit, now: time.Now}
}

// modelGatewayDetailsJSON marshals an audit detail payload, falling back to
// an empty object on failure so logging never blocks a gateway write. Local
// to this vertical and deliberately not named like the glossary/lifecycle
// helpers (keep-verticals-independent rationale, 同 glossary_service.go).
func modelGatewayDetailsJSON(v interface{}) types.JSON {
	data, err := json.Marshal(v)
	if err != nil {
		return types.JSON([]byte("{}"))
	}
	return types.JSON(data)
}

// truncModelGatewayDay folds an instant onto its UTC calendar day, the
// bucketing key of model_usage_daily.
func truncModelGatewayDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// ---------------------------------------------------------------------------
// Routing rules
// ---------------------------------------------------------------------------

// CreateRule creates one routing rule. New rules must carry a valid scope
// and an existing-shape model ID (无外键，这里只校验形状，不查 models 表——
// 路由到的模型不存在时调用方回退默认模型，不会阻断请求).
func (s *modelGatewayService) CreateRule(
	ctx context.Context, rule *types.ModelRoutingRule,
) (*types.ModelRoutingRule, error) {
	if rule == nil {
		return nil, apperrors.NewBadRequestError("rule body is required")
	}
	if err := validateModelRoutingRule(rule); err != nil {
		return nil, err
	}
	if rule.ID == "" {
		rule.ID = uuid.NewString()
	}
	if existing, err := s.repo.FindRuleByScope(ctx, rule.TenantID, rule.Scope, rule.ScopeValue); err == nil && existing != nil {
		return nil, apperrors.NewBadRequestError(
			"a routing rule already exists for this scope value; update it instead")
	} else if err != nil {
		return nil, err
	}
	if err := s.repo.CreateRule(ctx, rule); err != nil {
		return nil, err
	}
	s.logAudit(ctx, rule.TenantID, rule.CreatedBy, auditActionModelGatewayRuleCreated,
		rule.ID, map[string]interface{}{
			"scope":       rule.Scope,
			"scope_value": rule.ScopeValue,
			"model_id":    rule.ModelID,
			"enabled":     rule.Enabled,
		})
	return rule, nil
}

// UpdateRule patches model_id / enabled. Platform-level rules are shared
// configuration: only a system administrator may change them (与 builtin
// 模型的管理员语义一致).
func (s *modelGatewayService) UpdateRule(
	ctx context.Context, tenantID uint64, ruleID string,
	input *types.ModelRoutingRuleUpdate, actorID string,
) (*types.ModelRoutingRule, error) {
	if input == nil {
		return nil, apperrors.NewBadRequestError("update body is required")
	}
	rule, err := s.loadMutableRule(ctx, tenantID, ruleID)
	if err != nil {
		return nil, err
	}
	if input.ModelID != nil {
		modelID := strings.TrimSpace(*input.ModelID)
		if modelID == "" {
			return nil, apperrors.NewBadRequestError("model_id cannot be empty")
		}
		if utf8.RuneCountInString(modelID) > maxModelGatewayModelIDLen {
			return nil, apperrors.NewBadRequestError("model_id is too long (max 64 chars)")
		}
		rule.ModelID = modelID
	}
	if input.Enabled != nil {
		rule.Enabled = *input.Enabled
	}
	if err := s.repo.UpdateRule(ctx, rule); err != nil {
		return nil, err
	}
	s.logAudit(ctx, tenantID, actorID, auditActionModelGatewayRuleUpdated,
		ruleID, map[string]interface{}{
			"scope":       rule.Scope,
			"scope_value": rule.ScopeValue,
			"model_id":    rule.ModelID,
			"enabled":     rule.Enabled,
		})
	return rule, nil
}

// DeleteRule removes one rule (physical delete — 路由是配置不是生命周期数据).
func (s *modelGatewayService) DeleteRule(ctx context.Context, tenantID uint64, ruleID, actorID string) error {
	rule, err := s.loadMutableRule(ctx, tenantID, ruleID)
	if err != nil {
		return err
	}
	if err := s.repo.DeleteRule(ctx, rule.TenantID, ruleID); err != nil {
		return err
	}
	s.logAudit(ctx, tenantID, actorID, auditActionModelGatewayRuleDeleted,
		ruleID, map[string]interface{}{
			"scope":       rule.Scope,
			"scope_value": rule.ScopeValue,
			"model_id":    rule.ModelID,
		})
	return nil
}

// GetRule resolves one rule by ID (own tenant's or platform-level).
func (s *modelGatewayService) GetRule(ctx context.Context, tenantID uint64, ruleID string) (*types.ModelRoutingRule, error) {
	return s.loadRule(ctx, tenantID, ruleID)
}

// ListRules lists the tenant's rules plus the platform-level ones, so an
// admin sees the whole effective routing picture in one call.
func (s *modelGatewayService) ListRules(ctx context.Context, tenantID uint64) ([]*types.ModelRoutingRule, error) {
	return s.repo.ListRules(ctx, tenantID)
}

// ResolveModel implements the documented lookup order: classification rules
// first (密级优先于任务), then task rules; within a tier the tenant's own
// rule shadows the platform-level one. "" means "no rule matched" — the
// caller falls back to its default model selection, so an empty/erroneous
// gateway never changes existing behaviour. The repo error path also returns
// "" (with a warn log): a gateway outage must not take the chat pipeline
// down, it just degrades to the pre-gateway routing.
func (s *modelGatewayService) ResolveModel(ctx context.Context, tenantID uint64, task, classification string) string {
	if classification = strings.TrimSpace(classification); classification != "" {
		cls := types.KnowledgeClassification(classification)
		// Unknown classification values do not participate in 密级路由
		// (fall through to task rules) instead of silently matching the
		// internal rule — misrouting classified traffic is worse than
		// not routing it.
		if cls.Valid() {
			if modelID := s.lookupRule(ctx, tenantID, string(types.ModelRoutingScopeClassification), string(cls)); modelID != "" {
				return modelID
			}
		}
	}
	if task = strings.ToLower(strings.TrimSpace(task)); task != "" {
		if modelID := s.lookupRule(ctx, tenantID, string(types.ModelRoutingScopeTask), task); modelID != "" {
			return modelID
		}
	}
	return ""
}

// lookupRule resolves one (tenant → platform) rule pair and returns the
// routed model ID, "" on miss or error.
func (s *modelGatewayService) lookupRule(ctx context.Context, tenantID uint64, scope, scopeValue string) string {
	rule, err := s.repo.FindEnabledRule(ctx, tenantID, scope, scopeValue)
	if err != nil {
		logger.Warnf(ctx, "model gateway: rule lookup failed (scope=%s value=%s): %v", scope, scopeValue, err)
		return ""
	}
	if rule != nil && rule.Enabled && rule.ModelID != "" {
		return rule.ModelID
	}
	rule, err = s.repo.FindEnabledRule(ctx, types.PlatformLevelTenantID, scope, scopeValue)
	if err != nil {
		logger.Warnf(ctx, "model gateway: platform rule lookup failed (scope=%s value=%s): %v", scope, scopeValue, err)
		return ""
	}
	if rule != nil && rule.Enabled && rule.ModelID != "" {
		return rule.ModelID
	}
	return ""
}

// loadRule resolves a rule visible to the tenant (own or platform-level),
// translating record-not-found into the domain error the handler maps to 404.
func (s *modelGatewayService) loadRule(ctx context.Context, tenantID uint64, ruleID string) (*types.ModelRoutingRule, error) {
	rule, err := s.repo.GetRule(ctx, tenantID, ruleID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperrors.NewNotFoundError("routing rule not found")
		}
		return nil, err
	}
	return rule, nil
}

// loadMutableRule is loadRule + the write-path platform guard: a non-system-admin
// may never mutate platform-level (tenant 0) rules.
func (s *modelGatewayService) loadMutableRule(ctx context.Context, tenantID uint64, ruleID string) (*types.ModelRoutingRule, error) {
	rule, err := s.loadRule(ctx, tenantID, ruleID)
	if err != nil {
		return nil, err
	}
	if rule.IsPlatformLevel() && tenantID != types.PlatformLevelTenantID && !types.IsSystemAdminFromContext(ctx) {
		return nil, apperrors.NewForbiddenError(
			"only system administrators can modify platform-level routing rules")
	}
	return rule, nil
}

// validateModelRoutingRule checks the shape of a rule before insert.
func validateModelRoutingRule(rule *types.ModelRoutingRule) error {
	scope := strings.TrimSpace(rule.Scope)
	if !types.ModelRoutingScope(scope).Valid() {
		return apperrors.NewBadRequestError("scope must be task or classification")
	}
	rule.Scope = scope
	rule.ScopeValue = strings.TrimSpace(rule.ScopeValue)
	if rule.ScopeValue == "" {
		return apperrors.NewBadRequestError("scope_value is required")
	}
	if utf8.RuneCountInString(rule.ScopeValue) > maxModelGatewayScopeValueLen {
		return apperrors.NewBadRequestError("scope_value is too long (max 32 chars)")
	}
	if types.ModelRoutingScope(scope) == types.ModelRoutingScopeClassification &&
		!types.KnowledgeClassification(rule.ScopeValue).Valid() {
		return apperrors.NewBadRequestError(
			"scope_value must be a known classification (public/internal/secret/confidential)")
	}
	rule.ModelID = strings.TrimSpace(rule.ModelID)
	if rule.ModelID == "" {
		return apperrors.NewBadRequestError("model_id is required")
	}
	if utf8.RuneCountInString(rule.ModelID) > maxModelGatewayModelIDLen {
		return apperrors.NewBadRequestError("model_id is too long (max 64 chars)")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Prompt versions
// ---------------------------------------------------------------------------

// SaveVersion appends one prompt template version. version = 当前最大 + 1;
// content identical to the latest version dedupes (idempotent save — the
// admin re-submits an unchanged template and gets the existing version back,
// no new row). Versions are append-only: no update, no delete.
func (s *modelGatewayService) SaveVersion(
	ctx context.Context, version *types.PromptVersion,
) (*types.PromptVersion, error) {
	if version == nil {
		return nil, apperrors.NewBadRequestError("version body is required")
	}
	version.TemplateKey = strings.TrimSpace(version.TemplateKey)
	if version.TemplateKey == "" {
		return nil, apperrors.NewBadRequestError("template_key is required")
	}
	if utf8.RuneCountInString(version.TemplateKey) > maxModelGatewayTemplateKeyLen {
		return nil, apperrors.NewBadRequestError("template_key is too long (max 64 chars)")
	}
	if strings.TrimSpace(version.Content) == "" {
		return nil, apperrors.NewBadRequestError("content is required")
	}
	if utf8.RuneCountInString(version.Content) > maxModelGatewayPromptContentLen {
		return nil, apperrors.NewBadRequestError("content is too long")
	}
	if utf8.RuneCountInString(version.ChangeNote) > maxModelGatewayChangeNoteLen {
		return nil, apperrors.NewBadRequestError("change_note is too long (max 2000 chars)")
	}
	if version.ID == "" {
		version.ID = uuid.NewString()
	}

	latest, err := s.repo.LatestPromptVersion(ctx, version.TenantID, version.TemplateKey)
	if err != nil {
		return nil, err
	}
	if latest != nil && latest.Content == version.Content {
		// 内容相同去重：与最新版一致则不产生新版本（幂等保存）。
		return latest, nil
	}
	if latest != nil {
		version.Version = latest.Version + 1
	} else {
		version.Version = 1
	}
	if err := s.repo.CreatePromptVersion(ctx, version); err != nil {
		return nil, err
	}
	s.logAudit(ctx, version.TenantID, version.CreatedBy, auditActionModelGatewayPromptCreated,
		version.TemplateKey, map[string]interface{}{
			"version": version.Version,
			"bytes":   len(version.Content),
		})
	return version, nil
}

// ListVersions lists all versions of one template key, newest first.
func (s *modelGatewayService) ListVersions(
	ctx context.Context, tenantID uint64, templateKey string,
) ([]*types.PromptVersion, error) {
	return s.repo.ListPromptVersions(ctx, tenantID, strings.TrimSpace(templateKey))
}

// GetVersion resolves one exact version (404 when absent).
func (s *modelGatewayService) GetVersion(
	ctx context.Context, tenantID uint64, templateKey string, version int,
) (*types.PromptVersion, error) {
	row, err := s.repo.GetPromptVersion(ctx, tenantID, strings.TrimSpace(templateKey), version)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperrors.NewNotFoundError("prompt version not found")
		}
		return nil, err
	}
	return row, nil
}

// LatestVersion returns the highest version of one template key, or
// (nil, nil) when the key has no versions yet — the template loader's
// fall-back-to-YAML path (加载器接线建议见交付报告；本垂直不改加载器).
func (s *modelGatewayService) LatestVersion(
	ctx context.Context, tenantID uint64, templateKey string,
) (*types.PromptVersion, error) {
	return s.repo.LatestPromptVersion(ctx, tenantID, strings.TrimSpace(templateKey))
}

// ---------------------------------------------------------------------------
// Usage
// ---------------------------------------------------------------------------

// RecordUsage adds one model call's tokens to the UTC-day accumulator.
// Negative counters are clamped to 0; the call is still counted (call_count
// +1) even when the provider reports no usage. Callers may ignore the
// returned error (记录失败不应阻断业务调用), the service never panics.
func (s *modelGatewayService) RecordUsage(
	ctx context.Context, tenantID uint64, modelID string, promptTokens, completionTokens int64,
) error {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return apperrors.NewBadRequestError("model_id is required")
	}
	if promptTokens < 0 {
		promptTokens = 0
	}
	if completionTokens < 0 {
		completionTokens = 0
	}
	err := s.repo.UpsertUsage(ctx, truncModelGatewayDay(s.now()), tenantID, modelID, promptTokens, completionTokens)
	if err != nil {
		// 用量记录失败只记日志：成本遥测永远不能反噬调用链。
		logger.Warnf(ctx, "model gateway: record usage failed (tenant=%d model=%s): %v", tenantID, modelID, err)
	}
	return err
}

// UsageSummary aggregates the last `days` days (UTC, inclusive of today) by
// day and model, day ascending. days <= 0 defaults to 30, above 366 clamps.
func (s *modelGatewayService) UsageSummary(
	ctx context.Context, tenantID uint64, days int,
) ([]*types.ModelUsageSummaryRow, error) {
	if days <= 0 {
		days = 30
	}
	if days > maxModelGatewaySummaryDays {
		days = maxModelGatewaySummaryDays
	}
	since := truncModelGatewayDay(s.now()).AddDate(0, 0, -(days - 1))
	rows, err := s.repo.ListUsage(ctx, tenantID, since)
	if err != nil {
		return nil, err
	}
	summary := make([]*types.ModelUsageSummaryRow, 0, len(rows))
	for _, row := range rows {
		summary = append(summary, &types.ModelUsageSummaryRow{
			Day:              truncModelGatewayDay(row.Day).Format("2006-01-02"),
			ModelID:          row.ModelID,
			PromptTokens:     row.PromptTokens,
			CompletionTokens: row.CompletionTokens,
			CallCount:        row.CallCount,
		})
	}
	return summary, nil
}

// logAudit writes one model gateway audit event; audit failures never block
// the business write (same policy as the other verticals).
func (s *modelGatewayService) logAudit(
	ctx context.Context, tenantID uint64, actorID, action, targetID string,
	details map[string]interface{},
) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Log(ctx, &types.AuditLog{
		TenantID:    tenantID,
		ActorUserID: actorID,
		Action:      types.AuditAction(action),
		ScopeType:   "model_gateway",
		TargetType:  "model_gateway_config",
		TargetID:    targetID,
		Outcome:     "success",
		Details:     modelGatewayDetailsJSON(details),
	})
}
