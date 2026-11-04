package repository

import (
	"context"
	"errors"
	"strings"

	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// ErrSavedViewNameTaken is returned when the (tenant, user, name) key collides.
//
// 用哨兵错误而不是把数据库的唯一约束错误往上抛：服务层要据此返回 409，
// 而"unique constraint violated"这句话会随数据库方言变化。
var ErrSavedViewNameTaken = errors.New("saved view name already exists")

// savedViewRepository implements interfaces.SavedViewRepository.
type savedViewRepository struct {
	db *gorm.DB
}

// NewSavedViewRepository constructs the repository.
func NewSavedViewRepository(db *gorm.DB) interfaces.SavedViewRepository {
	return &savedViewRepository{db: db}
}

// scope narrows every query to one owner.
//
// 单独一个方法是为了让"每个查询都必须带 user_id"这件事只有一个实现点：
// 漏掉一处就是横向越权。
func (r *savedViewRepository) scope(
	ctx context.Context, tenantID uint64, userID string,
) *gorm.DB {
	return r.db.WithContext(ctx).
		Where("tenant_id = ? AND user_id = ?", tenantID, userID)
}

// List returns one user's views, ordered by name.
func (r *savedViewRepository) List(
	ctx context.Context, tenantID uint64, userID string,
) ([]*types.SavedKnowledgeView, error) {
	var views []*types.SavedKnowledgeView
	err := r.scope(ctx, tenantID, userID).
		Order("is_default DESC, name ASC").
		Find(&views).Error
	return views, err
}

// Get returns one view.
func (r *savedViewRepository) Get(
	ctx context.Context, tenantID uint64, userID, viewID string,
) (*types.SavedKnowledgeView, error) {
	var view types.SavedKnowledgeView
	err := r.scope(ctx, tenantID, userID).
		Where("id = ?", viewID).
		First(&view).Error
	if err != nil {
		return nil, err
	}
	return &view, nil
}

// Create inserts one view.
func (r *savedViewRepository) Create(
	ctx context.Context, view *types.SavedKnowledgeView,
) error {
	err := r.db.WithContext(ctx).Create(view).Error
	if err != nil && isUniqueViolation(err) {
		return ErrSavedViewNameTaken
	}
	return err
}

// Update writes the mutable fields of one view.
func (r *savedViewRepository) Update(
	ctx context.Context, view *types.SavedKnowledgeView,
) error {
	result := r.scope(ctx, view.TenantID, view.UserID).
		Model(&types.SavedKnowledgeView{}).
		Where("id = ?", view.ID).
		Updates(map[string]interface{}{
			"name":              view.Name,
			"knowledge_base_id": view.KnowledgeBaseID,
			"filter":            view.Filter,
		})
	if result.Error != nil {
		if isUniqueViolation(result.Error) {
			return ErrSavedViewNameTaken
		}
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// Delete removes one view.
func (r *savedViewRepository) Delete(
	ctx context.Context, tenantID uint64, userID, viewID string,
) error {
	result := r.scope(ctx, tenantID, userID).
		Where("id = ?", viewID).
		Delete(&types.SavedKnowledgeView{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// Count returns how many views the user owns.
func (r *savedViewRepository) Count(
	ctx context.Context, tenantID uint64, userID string,
) (int64, error) {
	var total int64
	err := r.scope(ctx, tenantID, userID).
		Model(&types.SavedKnowledgeView{}).
		Count(&total).Error
	return total, err
}

// ClearDefault unsets the default flag for one (user, knowledge base).
func (r *savedViewRepository) ClearDefault(
	ctx context.Context, tenantID uint64, userID, knowledgeBaseID string,
) error {
	return r.scope(ctx, tenantID, userID).
		Model(&types.SavedKnowledgeView{}).
		Where("knowledge_base_id = ? AND is_default = ?", knowledgeBaseID, true).
		Update("is_default", false).Error
}

// SetDefault marks one view as the space default.
func (r *savedViewRepository) SetDefault(
	ctx context.Context, tenantID uint64, userID, viewID string,
) error {
	result := r.scope(ctx, tenantID, userID).
		Model(&types.SavedKnowledgeView{}).
		Where("id = ?", viewID).
		Update("is_default", true)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// isUniqueViolation reports whether an error is a duplicate-key failure.
//
// 两种方言的措辞不同（Postgres "duplicate key value violates unique
// constraint"、SQLite "UNIQUE constraint failed"），按子串判断足够稳，且
// 不引入驱动包的私有错误类型。
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, needle := range []string{
		"duplicate key", "unique constraint failed", "unique constraint",
	} {
		if strings.Contains(message, needle) {
			return true
		}
	}
	return false
}
