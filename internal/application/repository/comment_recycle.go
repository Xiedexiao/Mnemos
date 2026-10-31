package repository

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// commentRepository implements interfaces.CommentRepository.
type commentRepository struct {
	db *gorm.DB
}

// NewCommentRepository constructs the comment repository.
func NewCommentRepository(db *gorm.DB) interfaces.CommentRepository {
	return &commentRepository{db: db}
}

// CreateComment inserts one comment. Soft-deleted siblings are untouched;
// the (tenant_id, knowledge_id, created_at) index from migration 000146
// serves the thread listing.
func (r *commentRepository) CreateComment(ctx context.Context, comment *types.KnowledgeComment) error {
	return r.db.WithContext(ctx).Create(comment).Error
}

// GetComment resolves one comment by ID within the tenant. The model's
// gorm.DeletedAt filters soft-deleted rows out automatically.
func (r *commentRepository) GetComment(ctx context.Context, tenantID uint64, commentID string) (*types.KnowledgeComment, error) {
	var comment types.KnowledgeComment
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, commentID).
		First(&comment).Error; err != nil {
		return nil, err
	}
	return &comment, nil
}

// ListByKnowledge returns every live comment of one knowledge entry in
// chronological order (top level and replies mixed; the service builds the
// tree from parent_id).
func (r *commentRepository) ListByKnowledge(ctx context.Context, tenantID uint64, knowledgeID string) ([]*types.KnowledgeComment, error) {
	var comments []*types.KnowledgeComment
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_id = ?", tenantID, knowledgeID).
		Order("created_at ASC").
		Find(&comments).Error
	return comments, err
}

// ResolveComment performs the atomic unresolved→resolved flip with the
// resolver's ID. Zero rows affected means the comment is gone or
// soft-deleted; surfaced as ErrRecordNotFound.
func (r *commentRepository) ResolveComment(ctx context.Context, tenantID uint64, commentID, resolvedBy string) error {
	result := r.db.WithContext(ctx).
		Model(&types.KnowledgeComment{}).
		Where("tenant_id = ? AND id = ?", tenantID, commentID).
		Updates(map[string]interface{}{
			"resolved":    true,
			"resolved_by": resolvedBy,
			"updated_at":  time.Now(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// SoftDeleteComment soft-deletes one comment (deleted_at set, row kept for
// audit — same policy as the glossary/knowledge verticals). Zero rows
// affected surfaces ErrRecordNotFound.
func (r *commentRepository) SoftDeleteComment(ctx context.Context, tenantID uint64, commentID string) error {
	result := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, commentID).
		Delete(&types.KnowledgeComment{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// recycleBinRepository implements interfaces.RecycleBinRepository.
type recycleBinRepository struct {
	db *gorm.DB
}

// NewRecycleBinRepository constructs the recycle-bin repository.
func NewRecycleBinRepository(db *gorm.DB) interfaces.RecycleBinRepository {
	return &recycleBinRepository{db: db}
}

// Enqueue registers (or refreshes) the entry for one soft-deleted entity.
// UNIQUE(entity_type, entity_id) turns a re-delete into an upsert: title/
// deleted_by/purge_at/created_at reset and restored_at clears, restarting
// the 30-day window. clause.Assignments keeps it portable across Postgres
// and SQLite (both supported by the gorm driver matrix here).
func (r *recycleBinRepository) Enqueue(ctx context.Context, entry *types.RecycleBinEntry) error {
	return r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "entity_type"}, {Name: "entity_id"}},
			DoUpdates: clause.Assignments(map[string]interface{}{
				"title":       entry.Title,
				"deleted_by":  entry.DeletedBy,
				"purge_at":    entry.PurgeAt,
				"restored_at": nil,
				"created_at":  entry.CreatedAt,
			}),
		}).
		Create(entry).Error
}

// Get resolves one entry by ID within the tenant.
func (r *recycleBinRepository) Get(ctx context.Context, tenantID uint64, recycleID string) (*types.RecycleBinEntry, error) {
	var entry types.RecycleBinEntry
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, recycleID).
		First(&entry).Error; err != nil {
		return nil, err
	}
	return &entry, nil
}

// List returns the tenant's entries, newest first; entityType filters by
// kind when non-empty. Restored entries stay listed (restored_at tells them
// apart) so the admin sees the full history of the window.
func (r *recycleBinRepository) List(ctx context.Context, tenantID uint64, entityType string) ([]*types.RecycleBinEntry, error) {
	query := r.db.WithContext(ctx).Where("tenant_id = ?", tenantID)
	if entityType != "" {
		query = query.Where("entity_type = ?", entityType)
	}
	var entries []*types.RecycleBinEntry
	err := query.Order("created_at DESC").Find(&entries).Error
	return entries, err
}

// MarkRestored stamps restored_at on one entry. Zero rows affected means the
// entry is gone; surfaced as ErrRecordNotFound.
func (r *recycleBinRepository) MarkRestored(ctx context.Context, tenantID uint64, recycleID string, restoredAt time.Time) error {
	result := r.db.WithContext(ctx).
		Model(&types.RecycleBinEntry{}).
		Where("tenant_id = ? AND id = ?", tenantID, recycleID).
		Update("restored_at", restoredAt)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// Delete physically removes one recycle-bin row (post-restore or post-purge).
func (r *recycleBinRepository) Delete(ctx context.Context, tenantID uint64, recycleID string) error {
	return r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, recycleID).
		Delete(&types.RecycleBinEntry{}).Error
}

// ListDue returns every tenant's unrestored entries whose purge deadline has
// passed. restored_at IS NULL is load-bearing: a restored entity is alive,
// and purging it would hard-delete live content.
func (r *recycleBinRepository) ListDue(ctx context.Context, now time.Time) ([]*types.RecycleBinEntry, error) {
	var entries []*types.RecycleBinEntry
	err := r.db.WithContext(ctx).
		Where("purge_at <= ? AND restored_at IS NULL", now).
		Order("purge_at ASC").
		Find(&entries).Error
	return entries, err
}

// GetKnowledgeIncludingDeleted loads one knowledge row without the
// soft-delete filter (restore precondition check).
func (r *recycleBinRepository) GetKnowledgeIncludingDeleted(ctx context.Context, tenantID uint64, knowledgeID string) (*types.Knowledge, error) {
	var knowledge types.Knowledge
	if err := r.db.WithContext(ctx).Unscoped().
		Where("tenant_id = ? AND id = ?", tenantID, knowledgeID).
		First(&knowledge).Error; err != nil {
		return nil, err
	}
	return &knowledge, nil
}

// RestoreKnowledge clears the knowledge row's deleted_at (Unscoped UPDATE,
// tenant-scoped). Zero rows affected means the row is physically gone;
// surfaced as ErrRecordNotFound.
func (r *recycleBinRepository) RestoreKnowledge(ctx context.Context, tenantID uint64, knowledgeID string) error {
	result := r.db.WithContext(ctx).Unscoped().
		Model(&types.Knowledge{}).
		Where("tenant_id = ? AND id = ?", tenantID, knowledgeID).
		Update("deleted_at", nil)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// HardDeleteKnowledge physically removes one knowledge row (Unscoped DELETE,
// tenant-scoped). Chunks/vectors/files were already released by the delete
// pipeline; this only retires the metadata row itself.
func (r *recycleBinRepository) HardDeleteKnowledge(ctx context.Context, tenantID uint64, knowledgeID string) error {
	result := r.db.WithContext(ctx).Unscoped().
		Where("tenant_id = ? AND id = ?", tenantID, knowledgeID).
		Delete(&types.Knowledge{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
