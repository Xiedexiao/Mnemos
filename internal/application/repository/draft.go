package repository

import (
	"context"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// ErrDraftNotFound is returned when a draft lookup misses.
var ErrDraftNotFound = errors.New("draft not found")

// draftRepository implements interfaces.DraftRepository (KnowAxis V3.0 §5.4).
type draftRepository struct {
	db *gorm.DB
}

// NewDraftRepository constructs the repository.
func NewDraftRepository(db *gorm.DB) interfaces.DraftRepository {
	return &draftRepository{db: db}
}

// draftOwnerColumns is the unique key that identifies one draft slot.
func draftOwnerColumns() []clause.Column {
	return []clause.Column{
		{Name: "tenant_id"}, {Name: "user_id"}, {Name: "kind"}, {Name: "scope_id"},
	}
}

// Get returns the draft for one (user, kind, scope).
func (r *draftRepository) Get(
	ctx context.Context, tenantID uint64, userID string, kind types.DraftKind, scopeID string,
) (*types.Draft, error) {
	var draft types.Draft
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND user_id = ? AND kind = ? AND scope_id = ?",
			tenantID, userID, string(kind), scopeID).
		First(&draft).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &draft, nil
}

// Save writes the draft, refusing to overwrite a newer revision.
//
// 实现方式：先读当前行，比对 revision，再在**同一个事务**里写。用事务而不是
// 条件 UPDATE 是因为需要把"当前内容"原样带回去给前端合并——只返回冲突而不
// 返回对方写了什么，前端没法合并。
func (r *draftRepository) Save(
	ctx context.Context, draft *types.Draft, expectedRevision int,
) (*types.Draft, bool, error) {
	var stored *types.Draft
	conflicted := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing types.Draft
		findErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("tenant_id = ? AND user_id = ? AND kind = ? AND scope_id = ?",
				draft.TenantID, draft.UserID, draft.Kind, draft.ScopeID).
			First(&existing).Error
		switch {
		case errors.Is(findErr, gorm.ErrRecordNotFound):
			draft.Revision = 1
			if err := tx.Create(draft).Error; err != nil {
				return err
			}
			stored = draft
			return nil
		case findErr != nil:
			return findErr
		}
		if expectedRevision > 0 && existing.Revision != expectedRevision {
			// 别人（或这个用户的另一个标签页）已经写过：把存储版本带回去。
			conflicted = true
			stored = &existing
			return nil
		}
		updates := map[string]interface{}{
			"title":      draft.Title,
			"body":       draft.Body,
			"anchor":     draft.Anchor,
			"mentions":   draft.Mentions,
			"revision":   existing.Revision + 1,
			"updated_at": draft.UpdatedAt,
		}
		if err := tx.Model(&types.Draft{}).Where("id = ?", existing.ID).
			Updates(updates).Error; err != nil {
			return err
		}
		existing.Title = draft.Title
		existing.Body = draft.Body
		existing.Anchor = draft.Anchor
		existing.Mentions = draft.Mentions
		existing.Revision++
		existing.UpdatedAt = draft.UpdatedAt
		stored = &existing
		draft.ID = existing.ID
		draft.Revision = existing.Revision
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return stored, conflicted, nil
}

// List returns the user's drafts, newest first.
func (r *draftRepository) List(
	ctx context.Context, tenantID uint64, userID string, kind string, limit int,
) ([]*types.Draft, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := r.db.WithContext(ctx).
		Where("tenant_id = ? AND user_id = ?", tenantID, userID)
	if kind != "" {
		query = query.Where("kind = ?", kind)
	}
	var drafts []*types.Draft
	if err := query.Order("updated_at DESC").Limit(limit).Find(&drafts).Error; err != nil {
		return nil, err
	}
	return drafts, nil
}

// Delete removes one draft.
func (r *draftRepository) Delete(
	ctx context.Context, tenantID uint64, userID, draftID string,
) error {
	result := r.db.WithContext(ctx).
		Where("tenant_id = ? AND user_id = ? AND id = ?", tenantID, userID, draftID).
		Delete(&types.Draft{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrDraftNotFound
	}
	return nil
}
