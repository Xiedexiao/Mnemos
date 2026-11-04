package interfaces

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// RecommendationRepository answers the read-model queries behind §15.3
// recommendations and the browse history.
type RecommendationRepository interface {
	// RecordView appends one view event unless the user already viewed this
	// document inside the dedupe window.
	RecordView(
		ctx context.Context, tenantID uint64, userID, knowledgeID, knowledgeBaseID string,
		now time.Time,
	) (bool, error)
	// ListHistory returns the user's recent views, newest first.
	ListHistory(
		ctx context.Context, tenantID uint64, userID string, limit int,
	) ([]*types.KnowledgeViewEvent, error)
	// RelatedKnowledge returns documents linked to one by a typed relation.
	RelatedKnowledge(
		ctx context.Context, tenantID uint64, knowledgeID string, limit int,
	) ([]types.RecommendationItem, error)
	// SameTagKnowledge returns documents sharing at least one tag with the
	// given one, excluding itself.
	SameTagKnowledge(
		ctx context.Context, tenantID uint64, knowledgeID string, limit int,
	) ([]types.RecommendationItem, error)
	// SameSpaceKnowledge returns other documents in the same space.
	SameSpaceKnowledge(
		ctx context.Context, tenantID uint64, knowledgeBaseID, excludeKnowledgeID string,
		limit int,
	) ([]types.RecommendationItem, error)
	// InterestTags returns the tags the user read or starred most often inside
	// the interest window, most frequent first.
	InterestTags(
		ctx context.Context, tenantID uint64, userID string, since time.Time, limit int,
	) ([]string, error)
	// KnowledgeForTags returns documents carrying any of the tags.
	KnowledgeForTags(
		ctx context.Context, tenantID uint64, tags []string, excludeKnowledgeID string,
		limit int,
	) ([]types.RecommendationItem, error)
	// LatestKnowledge returns the newest documents in the tenant (optionally
	// scoped to one space).
	LatestKnowledge(
		ctx context.Context, tenantID uint64, knowledgeBaseID string, limit int,
	) ([]types.RecommendationItem, error)
	// HotKnowledge returns the most-viewed documents since the given time.
	HotKnowledge(
		ctx context.Context, tenantID uint64, knowledgeBaseID string, since time.Time,
		limit int,
	) ([]types.RecommendationItem, error)
	// KnowledgeSummary returns id → (title, base id, status, classification,
	// updated at) for the given documents, used to build permission-filtered
	// lists without loading full rows.
	KnowledgeSummary(
		ctx context.Context, tenantID uint64, knowledgeIDs []string,
	) (map[string]types.RecommendationItem, error)
}

// RecommendationService is the §15.3 discovery surface.
type RecommendationService interface {
	// Board returns the four lists for one document (related / for-you /
	// latest / hot), each already permission-filtered.
	Board(
		ctx context.Context, tenantID uint64, knowledgeID string, limit int,
	) (*types.RecommendationBoard, error)
	// Home returns the personal board: for-you, latest and hot in the
	// caller's tenant.
	Home(
		ctx context.Context, tenantID uint64, userID string, limit int,
	) (*types.RecommendationBoard, error)
	// RecordView writes one read event (best effort) and updates history.
	RecordView(ctx context.Context, tenantID uint64, userID, knowledgeID string) error
	// History returns the caller's browse history with permission-flagged
	// visibility.
	History(
		ctx context.Context, tenantID uint64, userID string, limit int,
	) ([]types.ViewHistoryItem, error)
}

// SubscriptionRepository persists §15.3 follows.
type SubscriptionRepository interface {
	// Upsert inserts or re-enables one subscription.
	Upsert(ctx context.Context, subscription *types.Subscription) (bool, error)
	// List returns the user's subscriptions, newest first.
	List(ctx context.Context, tenantID uint64, userID string, enabledOnly bool) ([]*types.Subscription, error)
	// Get returns one subscription in the tenant.
	Get(ctx context.Context, tenantID uint64, userID, subscriptionID string) (*types.Subscription, error)
	// SetEnabled toggles one subscription.
	SetEnabled(ctx context.Context, tenantID uint64, userID, subscriptionID string, enabled bool) error
	// Delete removes one subscription.
	Delete(ctx context.Context, tenantID uint64, userID, subscriptionID string) error
	// ListDigestSources returns every enabled subscription in the tenant that
	// could contribute changes since the given time.
	ListDigestSources(ctx context.Context, tenantID uint64) ([]*types.Subscription, error)
	// ChangesSince returns documents that changed since the given time for one
	// space-scoped subscription set, newest first.
	ChangesSince(
		ctx context.Context, tenantID uint64, spaceID string, since time.Time, limit int,
	) ([]types.DigestItem, error)
	// TagChangesSince returns documents carrying one tag that changed since
	// the given time.
	TagChangesSince(
		ctx context.Context, tenantID uint64, tagName string, since time.Time, limit int,
	) ([]types.DigestItem, error)
	// AuthorChangesSince returns documents published by one user since the
	// given time.
	AuthorChangesSince(
		ctx context.Context, tenantID uint64, authorID string, since time.Time, limit int,
	) ([]types.DigestItem, error)
	// KnowledgeChangesSince returns the change rows of one document.
	KnowledgeChangesSince(
		ctx context.Context, tenantID uint64, knowledgeID string, since time.Time,
	) ([]types.DigestItem, error)
	// FolderPaths returns id → folder_path for the given documents, used to
	// scope a folder subscription to its subtree.
	FolderPaths(ctx context.Context, tenantID uint64, knowledgeIDs []string) (map[string]string, error)
	// MarkDigested stamps the subscriptions with the digest time.
	MarkDigested(ctx context.Context, tenantID uint64, subscriptionIDs []string, at time.Time) error
}

// SubscriptionService is the §15.3 follow surface.
type SubscriptionService interface {
	// Subscribe adds one follow for the caller.
	Subscribe(
		ctx context.Context, tenantID uint64, userID string, input types.SubscriptionInput,
	) (*types.Subscription, error)
	// List returns the caller's follows.
	List(ctx context.Context, tenantID uint64, userID string, enabledOnly bool) ([]*types.Subscription, error)
	// SetEnabled pauses or resumes one follow.
	SetEnabled(
		ctx context.Context, tenantID uint64, userID, subscriptionID string, enabled bool,
	) error
	// Unsubscribe removes one follow.
	Unsubscribe(ctx context.Context, tenantID uint64, userID, subscriptionID string) error
	// Digest builds the caller's follow feed since the given time.
	Digest(
		ctx context.Context, tenantID uint64, userID string, since time.Time, limit int,
	) (*types.Digest, error)
}
