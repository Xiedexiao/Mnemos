package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// 嵌入式推送（§15.5）审计动作。
const (
	auditActionEmbedRuleCreated = "embed_push.rule_created"
	auditActionEmbedRuleUpdated = "embed_push.rule_updated"
	auditActionEmbedRuleDeleted = "embed_push.rule_deleted"
)

// 推送参数。
const (
	// embedPushDefaultLimit 是工位卡片默认条数：一次推太多会打断工作流。
	embedPushDefaultLimit = 3
	// embedPushMaxLimit 是单次推送上限。
	embedPushMaxLimit = 10
	// embedPushDismissWindow 内被忽略过的同一张卡片不再推送。
	embedPushDismissWindow = 30 * 24 * time.Hour
	// embedPushSummaryLimit 是卡片摘要的最大字符数。
	embedPushSummaryLimit = 160
)

// embedPushService implements interfaces.EmbedPushService.
type embedPushService struct {
	repo       interfaces.EmbedPushRepository
	knowledges interfaces.KnowledgeRepository
	acl        interfaces.KnowledgeACLService
	folderACL  interfaces.FolderACLRepository
	audit      interfaces.AuditLogService
	now        func() time.Time
}

// NewEmbedPushService constructs the service.
func NewEmbedPushService(
	repo interfaces.EmbedPushRepository,
	knowledges interfaces.KnowledgeRepository,
	acl interfaces.KnowledgeACLService,
	folderACL interfaces.FolderACLRepository,
	audit interfaces.AuditLogService,
) interfaces.EmbedPushService {
	return &embedPushService{
		repo:       repo,
		knowledges: knowledges,
		acl:        acl,
		folderACL:  folderACL,
		audit:      audit,
		now:        time.Now,
	}
}

// embedPushCandidate is one rule-selected knowledge item before filtering.
type embedPushCandidate struct {
	rule        *types.EmbedPushRule
	knowledgeID string
	matchedTag  string
}

// Resolve maps a business context onto permission-filtered knowledge cards.
//
// 过滤顺序是刻意的：先规则、再生命周期、最后权限。权限判定放在最后是因为
// 它最贵（跨 ACL + 目录），而且必须 fail-closed——ACL 查询失败时宁可这次
// 不推，也不能把可能无权的内容推出去。
func (s *embedPushService) Resolve(
	ctx context.Context, tenantID uint64, userID string,
	businessContext types.EmbedPushContext, limit int,
) (*types.EmbedPushResult, error) {
	if userID == "" {
		return nil, apperrors.NewUnauthorizedError("caller identity is required")
	}
	if limit <= 0 {
		limit = embedPushDefaultLimit
	}
	if limit > embedPushMaxLimit {
		limit = embedPushMaxLimit
	}

	preference, err := s.repo.GetPreference(ctx, tenantID, userID)
	if err != nil {
		return nil, err
	}
	if preference != nil && !preference.Enabled {
		return &types.EmbedPushResult{Enabled: false, Items: []types.EmbedPushItem{},
			Suppressed: "disabled"}, nil
	}

	rules, err := s.repo.ListRules(ctx, tenantID, true)
	if err != nil {
		return nil, err
	}
	matchedRules := make([]*types.EmbedPushRule, 0, len(rules))
	for _, rule := range rules {
		if embedRuleMatches(rule, businessContext) {
			matchedRules = append(matchedRules, rule)
		}
	}
	result := &types.EmbedPushResult{Enabled: true, Items: []types.EmbedPushItem{}}
	if len(matchedRules) == 0 {
		result.Suppressed = "no_match"
		return result, nil
	}

	candidates, err := s.collectCandidates(ctx, tenantID, matchedRules)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		result.Suppressed = "no_match"
		return result, nil
	}

	items, err := s.filterAndBuild(ctx, tenantID, userID, candidates)
	if err != nil {
		return nil, err
	}
	if len(items) > limit {
		items = items[:limit]
	}
	result.Items = items
	if len(items) == 0 {
		result.Suppressed = "no_match"
	}
	s.recordImpressions(ctx, tenantID, userID, items, businessContext)
	return result, nil
}

// collectCandidates expands rules into ordered, de-duplicated knowledge ids.
func (s *embedPushService) collectCandidates(
	ctx context.Context, tenantID uint64, rules []*types.EmbedPushRule,
) ([]embedPushCandidate, error) {
	tagSet := make(map[string]struct{})
	for _, rule := range rules {
		for _, tag := range rule.EmbedPushRuleTagNames() {
			tagSet[strings.ToLower(tag)] = struct{}{}
		}
	}
	tagMatches := map[string][]types.EmbedPushKnowledgeMatch{}
	if len(tagSet) > 0 {
		tags := make([]string, 0, len(tagSet))
		for tag := range tagSet {
			tags = append(tags, tag)
		}
		matches, err := s.repo.MatchKnowledgeByTags(ctx, tenantID, tags)
		if err != nil {
			return nil, err
		}
		for _, match := range matches {
			key := strings.ToLower(match.TagName)
			tagMatches[key] = append(tagMatches[key], match)
		}
	}

	seen := make(map[string]struct{})
	candidates := make([]embedPushCandidate, 0)
	appendCandidate := func(rule *types.EmbedPushRule, knowledgeID, matchedTag string) {
		knowledgeID = strings.TrimSpace(knowledgeID)
		if knowledgeID == "" {
			return
		}
		if _, ok := seen[knowledgeID]; ok {
			return
		}
		seen[knowledgeID] = struct{}{}
		candidates = append(candidates, embedPushCandidate{
			rule:        rule,
			knowledgeID: knowledgeID,
			matchedTag:  matchedTag,
		})
	}
	for _, rule := range rules {
		for _, knowledgeID := range rule.EmbedPushRuleKnowledgeIDs() {
			appendCandidate(rule, knowledgeID, "")
		}
		for _, tag := range rule.EmbedPushRuleTagNames() {
			for _, match := range tagMatches[strings.ToLower(tag)] {
				appendCandidate(rule, match.KnowledgeID, match.TagName)
			}
		}
	}
	return candidates, nil
}

// filterAndBuild applies lifecycle and permission gates, then builds cards.
func (s *embedPushService) filterAndBuild(
	ctx context.Context, tenantID uint64, userID string, candidates []embedPushCandidate,
) ([]types.EmbedPushItem, error) {
	ids := make([]string, 0, len(candidates))
	kbSet := make(map[string]struct{})
	for _, candidate := range candidates {
		ids = append(ids, candidate.knowledgeID)
	}
	knowledgeRows, err := s.knowledges.GetKnowledgeBatch(ctx, tenantID, ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]*types.Knowledge, len(knowledgeRows))
	for _, knowledge := range knowledgeRows {
		if knowledge == nil || knowledge.DeletedAt.Valid {
			continue
		}
		byID[knowledge.ID] = knowledge
		kbSet[knowledge.KnowledgeBaseID] = struct{}{}
	}
	if len(byID) == 0 {
		return nil, nil
	}
	kbIDs := make([]string, 0, len(kbSet))
	for kbID := range kbSet {
		kbIDs = append(kbIDs, kbID)
	}

	// 生命周期：草稿/过期/已下架的内容不下发。查询失败时记录并继续——
	// 这与检索引擎的处理一致（生命周期不是权限门）。
	excluded := make(map[string]struct{})
	if excludedIDs, lifecycleErr := s.knowledges.GetLifecycleExcludedKnowledgeIDs(
		ctx, kbIDs, s.now()); lifecycleErr != nil {
		logger.Warnf(ctx, "[embed-push] lifecycle gate failed: %v", lifecycleErr)
	} else {
		for _, id := range excludedIDs {
			excluded[id] = struct{}{}
		}
	}

	// 权限门（fail-closed）：文档 ACL + 目录 ACL。
	denied := make(map[string]struct{})
	if s.acl != nil {
		aclDenied, aclErr := s.acl.DeniedKnowledgeIDs(
			ctx, kbIDs, userID, types.PermissionActionView)
		if aclErr != nil {
			logger.ErrorWithFields(ctx, aclErr, map[string]interface{}{
				"fail_closed": true, "surface": "embed_push",
			})
			return nil, aclErr
		}
		for _, id := range aclDenied {
			denied[id] = struct{}{}
		}
	}
	folderDenied, folderErr := s.folderDeniedKnowledgeIDs(ctx, tenantID, kbIDs, userID)
	if folderErr != nil {
		logger.ErrorWithFields(ctx, folderErr, map[string]interface{}{
			"fail_closed": true, "surface": "embed_push_folder",
		})
		return nil, folderErr
	}
	for id := range folderDenied {
		denied[id] = struct{}{}
	}

	// 用户忽略过的卡片在窗口期内不再出现。
	dismissals, err := s.repo.ListDismissals(
		ctx, tenantID, userID, s.now().Add(-embedPushDismissWindow))
	if err != nil {
		return nil, err
	}
	dismissed := make(map[string]struct{}, len(dismissals))
	for _, dismissal := range dismissals {
		dismissed[dismissal.RuleID+"\x00"+dismissal.KnowledgeID] = struct{}{}
	}

	items := make([]types.EmbedPushItem, 0, len(candidates))
	for _, candidate := range candidates {
		knowledge, ok := byID[candidate.knowledgeID]
		if !ok {
			continue
		}
		if _, skip := excluded[knowledge.ID]; skip {
			continue
		}
		if _, skip := denied[knowledge.ID]; skip {
			continue
		}
		if _, skip := dismissed[candidate.rule.ID+"\x00"+knowledge.ID]; skip {
			continue
		}
		items = append(items, types.EmbedPushItem{
			RuleID:          candidate.rule.ID,
			KnowledgeID:     knowledge.ID,
			KnowledgeBaseID: knowledge.KnowledgeBaseID,
			Title:           knowledge.Title,
			Summary:         embedPushSummary(knowledge),
			Reason:          candidate.rule.Reason,
			MatchedTag:      candidate.matchedTag,
			Priority:        candidate.rule.Priority,
		})
	}
	return items, nil
}

// folderDeniedKnowledgeIDs folds directory denies for the caller.
func (s *embedPushService) folderDeniedKnowledgeIDs(
	ctx context.Context, tenantID uint64, kbIDs []string, userID string,
) (map[string]struct{}, error) {
	denied := make(map[string]struct{})
	if s.folderACL == nil || s.knowledges == nil {
		return denied, nil
	}
	for _, kbID := range kbIDs {
		grants, err := s.folderACL.ListByKnowledgeBase(ctx, tenantID, kbID)
		if err != nil {
			return nil, err
		}
		var denyPaths []string
		for _, grant := range grants {
			if grant == nil || grant.SubjectID != userID {
				continue
			}
			if grant.Effect != string(types.KnowledgeACLEffectDeny) {
				continue
			}
			denyPaths = append(denyPaths, grant.FolderPath)
		}
		if len(denyPaths) == 0 {
			continue
		}
		ids, err := s.knowledges.ListKnowledgeIDsUnderFolderPaths(ctx, kbID, denyPaths)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			denied[id] = struct{}{}
		}
	}
	return denied, nil
}

// recordImpressions logs what the widget was shown; failures are non-fatal.
func (s *embedPushService) recordImpressions(
	ctx context.Context, tenantID uint64, userID string,
	items []types.EmbedPushItem, businessContext types.EmbedPushContext,
) {
	payload, err := json.Marshal(businessContext)
	if err != nil {
		payload = []byte("{}")
	}
	for _, item := range items {
		event := &types.EmbedPushEvent{
			TenantID:    tenantID,
			UserID:      userID,
			RuleID:      item.RuleID,
			KnowledgeID: item.KnowledgeID,
			EventType:   string(types.EmbedPushEventImpression),
			Context:     types.JSON(payload),
			CreatedAt:   s.now().UTC(),
		}
		if err := s.repo.CreateEvent(ctx, event); err != nil {
			logger.Warnf(ctx, "[embed-push] record impression: %v", err)
			return
		}
	}
}

// GetPreference returns the effective switch (default enabled).
func (s *embedPushService) GetPreference(
	ctx context.Context, tenantID uint64, userID string,
) (*types.EmbedPushPreference, error) {
	if userID == "" {
		return nil, apperrors.NewUnauthorizedError("caller identity is required")
	}
	preference, err := s.repo.GetPreference(ctx, tenantID, userID)
	if err != nil {
		return nil, err
	}
	if preference == nil {
		return &types.EmbedPushPreference{
			TenantID: tenantID, UserID: userID, Enabled: true,
		}, nil
	}
	return preference, nil
}

// SetPreference turns contextual push on or off.
func (s *embedPushService) SetPreference(
	ctx context.Context, tenantID uint64, userID string, enabled bool,
) (*types.EmbedPushPreference, error) {
	if userID == "" {
		return nil, apperrors.NewUnauthorizedError("caller identity is required")
	}
	preference := &types.EmbedPushPreference{
		TenantID: tenantID, UserID: userID, Enabled: enabled,
	}
	if err := s.repo.UpsertPreference(ctx, preference); err != nil {
		return nil, err
	}
	return s.GetPreference(ctx, tenantID, userID)
}

// RecordEvent records a click or dismiss.
func (s *embedPushService) RecordEvent(
	ctx context.Context, tenantID uint64, userID string,
	ruleID, knowledgeID string, eventType types.EmbedPushEventType,
	businessContext types.EmbedPushContext,
) error {
	if userID == "" {
		return apperrors.NewUnauthorizedError("caller identity is required")
	}
	if eventType != types.EmbedPushEventClick && eventType != types.EmbedPushEventDismiss {
		return apperrors.NewBadRequestError("event_type must be click or dismiss")
	}
	if strings.TrimSpace(ruleID) == "" || strings.TrimSpace(knowledgeID) == "" {
		return apperrors.NewBadRequestError("rule_id and knowledge_id are required")
	}
	preference, err := s.repo.GetPreference(ctx, tenantID, userID)
	if err != nil {
		return err
	}
	if preference != nil && !preference.Enabled {
		// 关闭后不再记录任何交互；否则"我不再收到推送"会被自己的点击推翻。
		return nil
	}
	payload, err := json.Marshal(businessContext)
	if err != nil {
		payload = []byte("{}")
	}
	return s.repo.CreateEvent(ctx, &types.EmbedPushEvent{
		TenantID:    tenantID,
		UserID:      userID,
		RuleID:      strings.TrimSpace(ruleID),
		KnowledgeID: strings.TrimSpace(knowledgeID),
		EventType:   string(eventType),
		Context:     types.JSON(payload),
		CreatedAt:   s.now().UTC(),
	})
}

// CreateRule adds a push rule.
func (s *embedPushService) CreateRule(
	ctx context.Context, tenantID uint64, actorID string,
	input types.EmbedPushRuleInput,
) (*types.EmbedPushRule, error) {
	if err := types.ValidEmbedPushRuleInput(input, true); err != nil {
		return nil, apperrors.NewBadRequestError(err.Error())
	}
	rule := &types.EmbedPushRule{
		TenantID:     tenantID,
		Name:         strings.TrimSpace(input.Name),
		System:       strings.ToLower(strings.TrimSpace(input.System)),
		Scene:        strings.ToLower(strings.TrimSpace(input.Scene)),
		EntityType:   strings.ToLower(strings.TrimSpace(input.EntityType)),
		EntityValue:  strings.TrimSpace(input.EntityValue),
		Attributes:   embedPushAttributesJSON(input.Attributes),
		TagNames:     embedPushListJSON(input.TagNames),
		KnowledgeIDs: embedPushListJSON(input.KnowledgeIDs),
		Reason:       strings.TrimSpace(input.Reason),
		Priority:     input.Priority,
		Enabled:      true,
		CreatedBy:    actorID,
	}
	if input.Enabled != nil {
		rule.Enabled = *input.Enabled
	}
	if err := s.repo.CreateRule(ctx, rule); err != nil {
		return nil, err
	}
	s.logAudit(ctx, tenantID, actorID, auditActionEmbedRuleCreated, rule.ID,
		map[string]interface{}{"name": rule.Name, "priority": rule.Priority})
	return rule, nil
}

// UpdateRule edits a push rule.
func (s *embedPushService) UpdateRule(
	ctx context.Context, tenantID uint64, ruleID, actorID string,
	input types.EmbedPushRuleInput,
) (*types.EmbedPushRule, error) {
	rule, err := s.repo.GetRule(ctx, tenantID, ruleID)
	if err != nil {
		return nil, mapEmbedPushError(err)
	}
	if strings.TrimSpace(input.Name) != "" {
		rule.Name = strings.TrimSpace(input.Name)
	}
	// 上下文匹配字段允许清空（清空 = 匹配任意），所以直接赋值；空串是
	// 有效语义而不是"未提供"。
	rule.System = strings.ToLower(strings.TrimSpace(input.System))
	rule.Scene = strings.ToLower(strings.TrimSpace(input.Scene))
	rule.EntityType = strings.ToLower(strings.TrimSpace(input.EntityType))
	rule.EntityValue = strings.TrimSpace(input.EntityValue)
	if input.Attributes != nil {
		rule.Attributes = embedPushAttributesJSON(input.Attributes)
	}
	if input.TagNames != nil {
		rule.TagNames = embedPushListJSON(input.TagNames)
	}
	if input.KnowledgeIDs != nil {
		rule.KnowledgeIDs = embedPushListJSON(input.KnowledgeIDs)
	}
	if strings.TrimSpace(input.Reason) != "" {
		rule.Reason = strings.TrimSpace(input.Reason)
	}
	if input.Priority != 0 {
		rule.Priority = input.Priority
	}
	if input.Enabled != nil {
		rule.Enabled = *input.Enabled
	}
	if len(rule.EmbedPushRuleTagNames()) == 0 && len(rule.EmbedPushRuleKnowledgeIDs()) == 0 {
		return nil, apperrors.NewBadRequestError(
			"at least one tag_name or knowledge_id is required")
	}
	if err := s.repo.UpdateRule(ctx, rule); err != nil {
		return nil, mapEmbedPushError(err)
	}
	s.logAudit(ctx, tenantID, actorID, auditActionEmbedRuleUpdated, rule.ID,
		map[string]interface{}{"name": rule.Name, "priority": rule.Priority})
	return rule, nil
}

// DeleteRule removes a push rule.
func (s *embedPushService) DeleteRule(
	ctx context.Context, tenantID uint64, ruleID, actorID string,
) error {
	if err := s.repo.DeleteRule(ctx, tenantID, ruleID); err != nil {
		return mapEmbedPushError(err)
	}
	s.logAudit(ctx, tenantID, actorID, auditActionEmbedRuleDeleted, ruleID, nil)
	return nil
}

// ListRules lists push rules.
func (s *embedPushService) ListRules(
	ctx context.Context, tenantID uint64,
) ([]*types.EmbedPushRule, error) {
	return s.repo.ListRules(ctx, tenantID, false)
}

// RuleStats returns per-rule interaction counts for the last days.
func (s *embedPushService) RuleStats(
	ctx context.Context, tenantID uint64, days int,
) ([]types.EmbedPushRuleStats, error) {
	if days <= 0 {
		days = 30
	}
	if days > 365 {
		days = 365
	}
	return s.repo.RuleStats(ctx, tenantID, s.now().AddDate(0, 0, -days))
}

// embedRuleMatches reports whether a rule applies to the context.
func embedRuleMatches(rule *types.EmbedPushRule, businessContext types.EmbedPushContext) bool {
	equalFold := func(ruleValue, contextValue string) bool {
		return strings.EqualFold(strings.TrimSpace(ruleValue), strings.TrimSpace(contextValue))
	}
	if rule.System != "" && !equalFold(rule.System, businessContext.System) {
		return false
	}
	if rule.Scene != "" && !equalFold(rule.Scene, businessContext.Scene) {
		return false
	}
	if rule.EntityType != "" && !equalFold(rule.EntityType, businessContext.EntityType) {
		return false
	}
	if rule.EntityValue != "" && !equalFold(rule.EntityValue, businessContext.EntityID) {
		return false
	}
	for key, want := range rule.EmbedPushRuleAttributes() {
		got, ok := businessContext.Attributes[key]
		if !ok || got != want {
			return false
		}
	}
	return true
}

// embedPushSummary builds the card summary from the knowledge description.
func embedPushSummary(knowledge *types.Knowledge) string {
	summary := strings.TrimSpace(knowledge.Description)
	if summary == "" && knowledge.Profile != nil {
		summary = strings.TrimSpace(knowledge.Profile.Gist)
	}
	return truncateRunes(summary, embedPushSummaryLimit)
}

// embedPushAttributesJSON encodes the required attributes.
func embedPushAttributesJSON(attributes map[string]string) types.JSON {
	if attributes == nil {
		return types.JSON("{}")
	}
	raw, err := json.Marshal(attributes)
	if err != nil {
		return types.JSON("{}")
	}
	return types.JSON(raw)
}

// embedPushListJSON encodes a string list.
func embedPushListJSON(values []string) types.JSON {
	cleaned := make([]string, 0, len(values))
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
		cleaned = append(cleaned, trimmed)
	}
	raw, err := json.Marshal(cleaned)
	if err != nil {
		return types.JSON("[]")
	}
	return types.JSON(raw)
}

// mapEmbedPushError maps repository misses onto the API 404.
func mapEmbedPushError(err error) error {
	if errors.Is(err, repository.ErrEmbedPushRuleNotFound) {
		return apperrors.NewNotFoundError("push rule not found")
	}
	return err
}

// logAudit writes one embed-push audit event.
func (s *embedPushService) logAudit(
	ctx context.Context, tenantID uint64, actorID, action, ruleID string,
	details map[string]interface{},
) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Log(ctx, &types.AuditLog{
		TenantID:    tenantID,
		ActorUserID: actorID,
		Action:      types.AuditAction(action),
		ScopeType:   "embed_push",
		TargetType:  "push_rule",
		TargetID:    ruleID,
		Outcome:     "success",
		Details:     gamificationAuditJSON(details),
	})
}

// compile-time guard: the service must satisfy the interface.
var _ interfaces.EmbedPushService = (*embedPushService)(nil)
