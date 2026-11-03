package interfaces

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// RewardRepository persists the §15.4 redemption surfaces: the gift catalog
// and the redemption orders. The balance/ledger tables are shared with the
// gamification vertical; the debit happens here because it must share a
// transaction with the stock decrement and the order insert.
type RewardRepository interface {
	// CreateItem inserts one catalog item.
	CreateItem(ctx context.Context, item *types.RewardCatalogItem) error
	// UpdateItem persists the mutable fields.
	UpdateItem(ctx context.Context, item *types.RewardCatalogItem) error
	// GetItem returns one item in the tenant.
	GetItem(ctx context.Context, tenantID uint64, itemID string) (*types.RewardCatalogItem, error)
	// ListItems returns the catalog; enabledOnly hides retired items.
	ListItems(ctx context.Context, tenantID uint64, enabledOnly bool) ([]*types.RewardCatalogItem, error)
	// Redeem atomically reserves stock, debits the balance, writes the ledger
	// entry and inserts the pending redemption. Returns ErrRewardOutOfStock /
	// ErrRewardInsufficientPoints (repository sentinels) when a precondition
	// fails; nothing is written in that case.
	Redeem(
		ctx context.Context, tenantID uint64, userID, itemID string,
		redemption *types.RewardRedemption, ledger *types.UserPointsLedger,
	) error
	// GetRedemption returns one order in the tenant.
	GetRedemption(ctx context.Context, tenantID uint64, redemptionID string) (*types.RewardRedemption, error)
	// ListRedemptions returns orders, newest first; userID "" means all users
	// (the Admin view).
	ListRedemptions(
		ctx context.Context, tenantID uint64, userID string,
		status types.RewardRedemptionStatus, limit int,
	) ([]*types.RewardRedemption, error)
	// ResolveRedemption moves a pending order to fulfilled/rejected/canceled.
	// Refund statuses credit the points back and restore stock in the same
	// transaction; refundLedger carries the compensating ledger row.
	ResolveRedemption(
		ctx context.Context, tenantID uint64, redemptionID string,
		status types.RewardRedemptionStatus, actorID string,
		refundLedger *types.UserPointsLedger,
	) (*types.RewardRedemption, error)
}

// RewardService is the §15.4 redemption surface.
type RewardService interface {
	// CreateItem adds a catalog item (Admin).
	CreateItem(
		ctx context.Context, tenantID uint64, actorID string,
		input types.RewardCatalogItemInput,
	) (*types.RewardCatalogItem, error)
	// UpdateItem edits a catalog item (Admin).
	UpdateItem(
		ctx context.Context, tenantID uint64, itemID, actorID string,
		input types.RewardCatalogItemInput,
	) (*types.RewardCatalogItem, error)
	// ListItems returns the catalog; enabledOnly=true is the user view.
	ListItems(ctx context.Context, tenantID uint64, enabledOnly bool) ([]*types.RewardCatalogItem, error)
	// Redeem spends the user's points on one item.
	Redeem(
		ctx context.Context, tenantID uint64, userID, itemID, note string,
	) (*types.RewardRedemption, error)
	// Cancel lets the owner cancel a pending order (points refunded).
	Cancel(
		ctx context.Context, tenantID uint64, userID, redemptionID string,
	) (*types.RewardRedemption, error)
	// ListMyRedemptions returns the caller's orders.
	ListMyRedemptions(
		ctx context.Context, tenantID uint64, userID string,
		status types.RewardRedemptionStatus, limit int,
	) ([]*types.RewardRedemption, error)
	// ListRedemptions returns all orders (Admin).
	ListRedemptions(
		ctx context.Context, tenantID uint64, status types.RewardRedemptionStatus, limit int,
	) ([]*types.RewardRedemption, error)
	// Resolve records the operator decision (fulfilled/rejected).
	Resolve(
		ctx context.Context, tenantID uint64, actorID, redemptionID string,
		status types.RewardRedemptionStatus,
	) (*types.RewardRedemption, error)
}

// KnowledgeStarRepository persists §15.4 knowledge-star awards.
type KnowledgeStarRepository interface {
	// ListCandidates ranks contributors by points earned in [start, end).
	ListCandidates(
		ctx context.Context, tenantID uint64, start, end time.Time, limit int,
	) ([]*types.KnowledgeStarCandidate, error)
	// ReplaceAwards replaces one period's award list in a transaction.
	ReplaceAwards(ctx context.Context, tenantID uint64, awards []*types.KnowledgeStarAward) error
	// ListAwards returns awards; empty period means "all periods, newest
	// first" (the public board).
	ListAwards(
		ctx context.Context, tenantID uint64, periodType, period string,
		publishedOnly bool, limit int,
	) ([]*types.KnowledgeStarAward, error)
	// PublishAwards publishes one period's list.
	PublishAwards(
		ctx context.Context, tenantID uint64, periodType, period string,
	) (int64, error)
}

// KnowledgeStarService is the §15.4 knowledge-star surface.
type KnowledgeStarService interface {
	// Candidates returns the ranked contributors for a period (Admin).
	Candidates(
		ctx context.Context, tenantID uint64,
		periodType types.KnowledgeStarPeriodType, period string, limit int,
	) ([]*types.KnowledgeStarCandidate, error)
	// Select finalises the period's winners (Admin).
	Select(
		ctx context.Context, tenantID uint64, actorID string,
		selection types.KnowledgeStarSelection,
	) ([]*types.KnowledgeStarAward, error)
	// Publish makes the period's list public (Admin).
	Publish(
		ctx context.Context, tenantID uint64, actorID string,
		periodType types.KnowledgeStarPeriodType, period string,
	) (int64, error)
	// List returns awards; readers only ever see published rows.
	List(
		ctx context.Context, tenantID uint64, periodType, period string,
		publishedOnly bool, limit int,
	) ([]*types.KnowledgeStarAward, error)
}
