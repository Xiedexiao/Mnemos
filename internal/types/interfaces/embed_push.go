package interfaces

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// EmbedPushRepository persists the §15.5 contextual-push surfaces: rules,
// per-user preferences and interaction events.
type EmbedPushRepository interface {
	// CreateRule inserts one push rule.
	CreateRule(ctx context.Context, rule *types.EmbedPushRule) error
	// GetRule returns one rule in the tenant.
	GetRule(ctx context.Context, tenantID uint64, ruleID string) (*types.EmbedPushRule, error)
	// ListRules returns the tenant's rules, optionally only enabled ones.
	ListRules(ctx context.Context, tenantID uint64, enabledOnly bool) ([]*types.EmbedPushRule, error)
	// UpdateRule persists the full mutable state of a loaded rule.
	UpdateRule(ctx context.Context, rule *types.EmbedPushRule) error
	// DeleteRule removes one rule.
	DeleteRule(ctx context.Context, tenantID uint64, ruleID string) error
	// MatchKnowledgeByTags returns knowledge carrying any of the tag names.
	MatchKnowledgeByTags(
		ctx context.Context, tenantID uint64, tagNames []string,
	) ([]types.EmbedPushKnowledgeMatch, error)

	// GetPreference returns the user's switch, or (nil, nil) when the user has
	// never changed it (the default is enabled).
	GetPreference(ctx context.Context, tenantID uint64, userID string) (*types.EmbedPushPreference, error)
	// UpsertPreference writes the switch through (tenant, user) uniqueness.
	UpsertPreference(ctx context.Context, preference *types.EmbedPushPreference) error

	// CreateEvent appends one interaction row.
	CreateEvent(ctx context.Context, event *types.EmbedPushEvent) error
	// ListDismissals returns the cards the user dismissed since the given time.
	ListDismissals(
		ctx context.Context, tenantID uint64, userID string, since time.Time,
	) ([]types.EmbedPushDismissal, error)
	// RuleStats aggregates impressions/clicks/dismissals per rule since the
	// given time.
	RuleStats(
		ctx context.Context, tenantID uint64, since time.Time,
	) ([]types.EmbedPushRuleStats, error)
}

// EmbedPushService is the §15.5 embedded contextual-push surface.
type EmbedPushService interface {
	// Resolve maps a business context onto permission-filtered knowledge
	// cards. It records impression events for what it returns.
	Resolve(
		ctx context.Context, tenantID uint64, userID string,
		context types.EmbedPushContext, limit int,
	) (*types.EmbedPushResult, error)
	// GetPreference returns the effective switch (default enabled).
	GetPreference(
		ctx context.Context, tenantID uint64, userID string,
	) (*types.EmbedPushPreference, error)
	// SetPreference turns contextual push on or off for the user.
	SetPreference(
		ctx context.Context, tenantID uint64, userID string, enabled bool,
	) (*types.EmbedPushPreference, error)
	// RecordEvent records a click or dismiss (impressions are written by
	// Resolve). Dismissals suppress the same card for a while.
	RecordEvent(
		ctx context.Context, tenantID uint64, userID string,
		ruleID, knowledgeID string, eventType types.EmbedPushEventType,
		businessContext types.EmbedPushContext,
	) error

	// CreateRule adds a push rule (Admin).
	CreateRule(
		ctx context.Context, tenantID uint64, actorID string,
		input types.EmbedPushRuleInput,
	) (*types.EmbedPushRule, error)
	// UpdateRule edits a push rule (Admin).
	UpdateRule(
		ctx context.Context, tenantID uint64, ruleID, actorID string,
		input types.EmbedPushRuleInput,
	) (*types.EmbedPushRule, error)
	// DeleteRule removes a push rule (Admin).
	DeleteRule(ctx context.Context, tenantID uint64, ruleID, actorID string) error
	// ListRules lists push rules (Admin).
	ListRules(ctx context.Context, tenantID uint64) ([]*types.EmbedPushRule, error)
	// RuleStats returns per-rule interaction counts for the last days.
	RuleStats(ctx context.Context, tenantID uint64, days int) ([]types.EmbedPushRuleStats, error)
}
