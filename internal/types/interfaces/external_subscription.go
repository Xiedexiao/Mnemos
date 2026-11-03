package interfaces

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// ExternalSubscriptionRepository persists regulation/standards feeds and the
// entries already seen (KnowAxis V3.0 §7.3).
type ExternalSubscriptionRepository interface {
	// CreateFeed inserts one subscription.
	CreateFeed(ctx context.Context, feed *types.ExternalSubscriptionFeed) error
	// GetFeed returns one subscription in the tenant.
	GetFeed(ctx context.Context, tenantID uint64, feedID string) (*types.ExternalSubscriptionFeed, error)
	// ListFeeds returns subscriptions, optionally only enabled ones.
	ListFeeds(ctx context.Context, tenantID uint64, enabledOnly bool) ([]*types.ExternalSubscriptionFeed, error)
	// ListDueFeeds returns enabled feeds whose check interval has elapsed.
	ListDueFeeds(ctx context.Context, now time.Time, limit int) ([]*types.ExternalSubscriptionFeed, error)
	// UpdateFeed persists the editable fields and check state.
	UpdateFeed(ctx context.Context, feed *types.ExternalSubscriptionFeed) error
	// DeleteFeed removes a subscription and its entry history.
	DeleteFeed(ctx context.Context, tenantID uint64, feedID string) error
	// RecordEntry inserts an entry; reports false when the fingerprint is
	// already known (the feed republished identical content).
	RecordEntry(ctx context.Context, entry *types.ExternalSubscriptionEntry) (bool, error)
	// UpdateEntryOutcome records how many knowledge items and signals an entry
	// produced.
	UpdateEntryOutcome(ctx context.Context, entryID string, matchedKnowledge, signals int) error
	// DeleteEntry removes an entry record. Used to roll back the dedupe claim
	// when signal collection fails, so the next check retries the entry instead
	// of remembering it as handled.
	DeleteEntry(ctx context.Context, entryID string) error
	// ListEntries returns the newest entries of a feed.
	ListEntries(
		ctx context.Context, tenantID uint64, feedID string, limit int,
	) ([]*types.ExternalSubscriptionEntry, error)
	// MarkFeedChecked records the outcome of one check.
	MarkFeedChecked(
		ctx context.Context, tenantID uint64, feedID string, checkedAt time.Time, syncErr error,
	) error
	// MatchKnowledgeByTags returns knowledge items carrying any of the tag
	// names, restricted to the given knowledge bases when non-empty.
	MatchKnowledgeByTags(
		ctx context.Context, tenantID uint64, tagNames []string, knowledgeBaseIDs []string,
	) ([]types.ExternalSubscriptionMatchedKnowledge, error)
}

// ExternalSubscriptionService is the §7.3 external-subscription surface:
// manage feeds, check them, and expose what was seen.
type ExternalSubscriptionService interface {
	// CreateFeed subscribes a feed.
	CreateFeed(
		ctx context.Context, tenantID uint64, actorID string,
		input types.ExternalSubscriptionFeedInput,
	) (*types.ExternalSubscriptionFeed, error)
	// ListFeeds returns the tenant's subscriptions.
	ListFeeds(
		ctx context.Context, tenantID uint64, enabledOnly bool,
	) ([]*types.ExternalSubscriptionFeed, error)
	// UpdateFeed edits a subscription.
	UpdateFeed(
		ctx context.Context, tenantID uint64, feedID string,
		input types.ExternalSubscriptionFeedInput,
	) (*types.ExternalSubscriptionFeed, error)
	// DeleteFeed unsubscribes and clears the entry history.
	DeleteFeed(ctx context.Context, tenantID uint64, feedID string) error
	// SyncFeed checks one feed now and records the resulting signals.
	SyncFeed(
		ctx context.Context, tenantID uint64, feedID string,
	) (*types.ExternalSubscriptionSyncResult, error)
	// SyncDue checks every due feed. The scheduler calls this.
	SyncDue(ctx context.Context) (*types.ExternalSubscriptionSweepResult, error)
	// ListEntries returns the newest seen entries.
	ListEntries(
		ctx context.Context, tenantID uint64, feedID string, limit int,
	) ([]*types.ExternalSubscriptionEntry, error)
}
