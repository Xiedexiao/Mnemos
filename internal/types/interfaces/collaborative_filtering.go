package interfaces

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// CollaborativeFilteringRepository supplies the user-item interaction signals.
//
// 两条查询都带时间窗：两年前的阅读不代表现在的兴趣，而不限窗口会让推荐
// 越来越难解释——用户早已忘了自己读过什么。
type CollaborativeFilteringRepository interface {
	// Interactions returns the implicit ratings inside one tenant's window.
	Interactions(
		ctx context.Context, tenantID uint64, since time.Time, limit int,
		weights types.CFInteractionWeights,
	) ([]types.CFInteraction, error)
	// UserHistory returns the knowledge items one user touched in the window.
	UserHistory(
		ctx context.Context, tenantID uint64, userID string, since time.Time, limit int,
	) ([]string, error)
}

// CollaborativeFilteringService is the §15.3 behaviour-based recommender.
type CollaborativeFilteringService interface {
	// RecommendForUser suggests items from the caller's own history.
	RecommendForUser(
		ctx context.Context, tenantID uint64, userID string, exclude []string, limit int,
	) ([]types.CFRecommendation, error)
	// SimilarItems returns items co-read with the given one (item-to-item).
	SimilarItems(
		ctx context.Context, tenantID uint64, itemID string, limit int,
	) ([]types.CFRecommendation, error)
}
