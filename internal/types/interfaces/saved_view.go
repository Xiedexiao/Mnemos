package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// SavedViewRepository persists one user's saved filter sets (§12.2 我的视图).
//
// 所有方法都带 userID：视图是个人资产，没有"按租户列出全部视图"的入口。
// 一旦有，任何调用方都可能在某个列表页里读到他同事的筛选条件——那本身也是
// 一种行为泄露。
type SavedViewRepository interface {
	// List returns one user's views, ordered by name.
	List(ctx context.Context, tenantID uint64, userID string) ([]*types.SavedKnowledgeView, error)
	// Get returns one view, gorm.ErrRecordNotFound when it is not the caller's.
	Get(ctx context.Context, tenantID uint64, userID, viewID string) (*types.SavedKnowledgeView, error)
	// Create inserts one view. A duplicate (tenant, user, name) surfaces as
	// repository.ErrSavedViewNameTaken.
	Create(ctx context.Context, view *types.SavedKnowledgeView) error
	// Update writes name / filter / knowledge base of one view.
	Update(ctx context.Context, view *types.SavedKnowledgeView) error
	// Delete removes one view.
	Delete(ctx context.Context, tenantID uint64, userID, viewID string) error
	// Count returns how many views the user owns.
	Count(ctx context.Context, tenantID uint64, userID string) (int64, error)
	// ClearDefault unsets the default flag for (tenant, user, knowledge base).
	ClearDefault(ctx context.Context, tenantID uint64, userID, knowledgeBaseID string) error
	// SetDefault marks one view as the space default.
	SetDefault(ctx context.Context, tenantID uint64, userID, viewID string) error
}

// SavedViewService is the §12.2 saved-view surface.
type SavedViewService interface {
	// List returns the caller's views plus which one is the default.
	List(ctx context.Context, tenantID uint64, userID string) (*types.SavedViewList, error)
	// Get returns one view.
	Get(ctx context.Context, tenantID uint64, userID, viewID string) (*types.SavedKnowledgeView, error)
	// Create saves a new view after validating the name and every filter
	// dimension.
	Create(
		ctx context.Context, tenantID uint64, userID string, input types.SavedViewInput,
	) (*types.SavedKnowledgeView, error)
	// Update replaces the mutable fields of one view.
	Update(
		ctx context.Context, tenantID uint64, userID, viewID string, input types.SavedViewInput,
	) (*types.SavedKnowledgeView, error)
	// Delete removes one view.
	Delete(ctx context.Context, tenantID uint64, userID, viewID string) error
	// SetDefault promotes one view to the space default, demoting the previous
	// one so exactly one default exists per (user, knowledge base).
	SetDefault(
		ctx context.Context, tenantID uint64, userID, viewID string,
	) (*types.SavedKnowledgeView, error)
}
