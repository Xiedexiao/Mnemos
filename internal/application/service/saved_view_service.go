package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// savedViewService implements interfaces.SavedViewService
// (KnowAxis V3.0 §12.2 "筛选条件可保存为我的视图").
//
// 视图是个人资产：每一次读写都带调用者身份，不存在"按租户列出全部视图"的
// 路径。这不是防越权的额外加固，而是功能定义——"我的视图"里出现别人的视图
// 就不叫这个名字了。
type savedViewService struct {
	repo interfaces.SavedViewRepository
	now  func() time.Time
}

// NewSavedViewService constructs the service.
func NewSavedViewService(repo interfaces.SavedViewRepository) interfaces.SavedViewService {
	return &savedViewService{repo: repo, now: time.Now}
}

// List returns the caller's views plus which one is the default.
func (s *savedViewService) List(
	ctx context.Context, tenantID uint64, userID string,
) (*types.SavedViewList, error) {
	if err := requireSavedViewOwner(tenantID, userID); err != nil {
		return nil, err
	}
	views, err := s.repo.List(ctx, tenantID, userID)
	if err != nil {
		return nil, err
	}
	list := &types.SavedViewList{Views: views}
	if list.Views == nil {
		list.Views = []*types.SavedKnowledgeView{}
	}
	for _, view := range list.Views {
		if view != nil && view.IsDefault && list.DefaultViewID == "" {
			list.DefaultViewID = view.ID
		}
	}
	return list, nil
}

// Get returns one view.
func (s *savedViewService) Get(
	ctx context.Context, tenantID uint64, userID, viewID string,
) (*types.SavedKnowledgeView, error) {
	if err := requireSavedViewOwner(tenantID, userID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(viewID) == "" {
		return nil, apperrors.NewBadRequestError("view id is required")
	}
	view, err := s.repo.Get(ctx, tenantID, userID, viewID)
	if err != nil {
		return nil, mapSavedViewError(err)
	}
	return view, nil
}

// Create saves a new view.
func (s *savedViewService) Create(
	ctx context.Context, tenantID uint64, userID string, input types.SavedViewInput,
) (*types.SavedKnowledgeView, error) {
	if err := requireSavedViewOwner(tenantID, userID); err != nil {
		return nil, err
	}
	name, filter, err := prepareSavedView(input)
	if err != nil {
		return nil, err
	}
	// 上限检查放在插入前：并发创建仍可能略超，但那是个人偏好而非安全边界，
	// 不值得为它上一把锁。
	count, err := s.repo.Count(ctx, tenantID, userID)
	if err != nil {
		return nil, err
	}
	if count >= types.MaxSavedViewsPerUser {
		return nil, apperrors.NewBadRequestError(
			"too many saved views; delete one before saving another")
	}

	view := &types.SavedKnowledgeView{
		TenantID:        tenantID,
		UserID:          userID,
		Name:            name,
		KnowledgeBaseID: strings.TrimSpace(input.KnowledgeBaseID),
		CreatedAt:       s.now().UTC(),
		UpdatedAt:       s.now().UTC(),
	}
	if err := view.SetFilter(filter); err != nil {
		return nil, apperrors.NewBadRequestError("filter is not serialisable")
	}
	if input.IsDefault {
		// 先清旧的默认再插新的：先插会让 (user, kb) 短暂出现两个默认，
		// 而并发的读取方可能正好读到那一瞬。
		if err := s.repo.ClearDefault(ctx, tenantID, userID, view.KnowledgeBaseID); err != nil {
			return nil, err
		}
		view.IsDefault = true
	}
	if err := s.repo.Create(ctx, view); err != nil {
		return nil, mapSavedViewError(err)
	}
	return view, nil
}

// Update replaces the mutable fields of one view.
func (s *savedViewService) Update(
	ctx context.Context, tenantID uint64, userID, viewID string, input types.SavedViewInput,
) (*types.SavedKnowledgeView, error) {
	if err := requireSavedViewOwner(tenantID, userID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(viewID) == "" {
		return nil, apperrors.NewBadRequestError("view id is required")
	}
	name, filter, err := prepareSavedView(input)
	if err != nil {
		return nil, err
	}
	existing, err := s.repo.Get(ctx, tenantID, userID, viewID)
	if err != nil {
		return nil, mapSavedViewError(err)
	}
	existing.Name = name
	existing.KnowledgeBaseID = strings.TrimSpace(input.KnowledgeBaseID)
	if err := existing.SetFilter(filter); err != nil {
		return nil, apperrors.NewBadRequestError("filter is not serialisable")
	}
	if err := s.repo.Update(ctx, existing); err != nil {
		return nil, mapSavedViewError(err)
	}
	if input.IsDefault && !existing.IsDefault {
		updated, defaultErr := s.SetDefault(ctx, tenantID, userID, viewID)
		if defaultErr != nil {
			return nil, defaultErr
		}
		return updated, nil
	}
	return s.repo.Get(ctx, tenantID, userID, viewID)
}

// Delete removes one view.
func (s *savedViewService) Delete(
	ctx context.Context, tenantID uint64, userID, viewID string,
) error {
	if err := requireSavedViewOwner(tenantID, userID); err != nil {
		return err
	}
	if strings.TrimSpace(viewID) == "" {
		return apperrors.NewBadRequestError("view id is required")
	}
	return mapSavedViewError(s.repo.Delete(ctx, tenantID, userID, viewID))
}

// SetDefault promotes one view to the space default.
//
// 同一空间只有一个默认是硬约束（见接口注释）；这里的顺序是"先读、再清、
// 最后设"，任一步失败都不会留下两个默认。
func (s *savedViewService) SetDefault(
	ctx context.Context, tenantID uint64, userID, viewID string,
) (*types.SavedKnowledgeView, error) {
	if err := requireSavedViewOwner(tenantID, userID); err != nil {
		return nil, err
	}
	view, err := s.repo.Get(ctx, tenantID, userID, viewID)
	if err != nil {
		return nil, mapSavedViewError(err)
	}
	if err := s.repo.ClearDefault(ctx, tenantID, userID, view.KnowledgeBaseID); err != nil {
		return nil, err
	}
	if err := s.repo.SetDefault(ctx, tenantID, userID, viewID); err != nil {
		return nil, mapSavedViewError(err)
	}
	view.IsDefault = true
	return view, nil
}

// prepareSavedView normalizes and validates a create/update payload.
func prepareSavedView(input types.SavedViewInput) (string, types.SavedViewFilter, error) {
	name, err := types.ValidateSavedViewName(input.Name)
	if err != nil {
		return "", types.SavedViewFilter{}, apperrors.NewBadRequestError(err.Error())
	}
	filter := input.Filter
	filter.Normalize()
	// 读取时再校验一次的理由写在 types 里：存了三年后某个枚举值可能已废弃，
	// 保存时校验只能挡住"当时写错"的情况。
	if err := types.ValidateSavedViewFilter(filter); err != nil {
		return "", types.SavedViewFilter{}, apperrors.NewBadRequestError(err.Error())
	}
	return name, filter, nil
}

// requireSavedViewOwner guards the identity requirement.
func requireSavedViewOwner(tenantID uint64, userID string) error {
	if tenantID == 0 {
		return apperrors.NewUnauthorizedError("tenant context is required")
	}
	if strings.TrimSpace(userID) == "" {
		return apperrors.NewUnauthorizedError("caller identity is required")
	}
	return nil
}

// mapSavedViewError maps repository misses and conflicts onto the API surface.
func mapSavedViewError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, repository.ErrSavedViewNameTaken):
		// 409 而不是 400：同名不是格式错误，是冲突，客户端应提示"换个名字"。
		return apperrors.NewConflictError("a saved view with this name already exists")
	default:
		// gorm.ErrRecordNotFound 也走这里：视图不存在、或属于别人，对外是
		// 同一个答案。区分两者会让"这个 ID 是否存在"变成可探测的信息。
		return apperrors.NewNotFoundError("saved view not found")
	}
}

// compile-time guard.
var _ interfaces.SavedViewService = (*savedViewService)(nil)
