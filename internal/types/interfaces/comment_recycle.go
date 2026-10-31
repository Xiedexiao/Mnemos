package interfaces

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// ============================================================================
// 知识评论 + 回收站（KnowAxis V3.0 §5.2 / §5.5，v1 垂直）。
// ============================================================================

// CommentRepository persists knowledge comments (V3.0 §5.2) and answers the
// per-knowledge listing behind the thread view. Soft-deleted rows are
// invisible to every method (gorm.DeletedAt on the model).
type CommentRepository interface {
	// CreateComment inserts one comment (ID assigned by the model hook).
	CreateComment(ctx context.Context, comment *types.KnowledgeComment) error
	// GetComment resolves one comment by ID within the tenant. Returns
	// gorm.ErrRecordNotFound when the comment is gone or soft-deleted.
	GetComment(ctx context.Context, tenantID uint64, commentID string) (*types.KnowledgeComment, error)
	// ListByKnowledge returns every live comment of one knowledge entry in
	// chronological order (top level and replies mixed; the service assembles
	// the tree).
	ListByKnowledge(ctx context.Context, tenantID uint64, knowledgeID string) ([]*types.KnowledgeComment, error)
	// ResolveComment flips one comment to resolved with the resolver's ID in
	// a single UPDATE. Zero rows affected surfaces gorm.ErrRecordNotFound.
	ResolveComment(ctx context.Context, tenantID uint64, commentID, resolvedBy string) error
	// SoftDeleteComment soft-deletes one comment (deleted_at set, row kept
	// for audit). Zero rows affected surfaces gorm.ErrRecordNotFound.
	SoftDeleteComment(ctx context.Context, tenantID uint64, commentID string) error
}

// CommentService manages the collaborative discussion on knowledge entries
// (V3.0 §5.2). Moderation policy (author OR Admin) is enforced here, not in
// the handlers, so every future caller shares the same rule.
type CommentService interface {
	// AddComment creates one comment or reply (audit: comment.created).
	// Replies must reference a live top-level comment of the same knowledge.
	AddComment(ctx context.Context, comment *types.KnowledgeComment) (*types.KnowledgeComment, error)
	// ListByKnowledge returns the knowledge's comments as threads: top-level
	// comments each carrying their replies, both in chronological order.
	// Soft-deleted comments are excluded; replies whose parent was deleted
	// are promoted to top level so no surviving content disappears.
	ListByKnowledge(ctx context.Context, tenantID uint64, knowledgeID string) ([]*types.KnowledgeCommentThread, error)
	// ResolveComment marks one comment resolved (author OR Admin; audit:
	// comment.resolved). Re-resolving an already-resolved comment is a 400.
	ResolveComment(ctx context.Context, tenantID uint64, commentID, actorID string) error
	// DeleteComment soft-deletes one comment (author OR Admin; audit:
	// comment.deleted).
	DeleteComment(ctx context.Context, tenantID uint64, commentID, actorID string) error
}

// RecycleBinRepository tracks soft-deleted entities and performs the
// restore/purge row surgery on their tables (V3.0 §5.5). The knowledge-row
// statements live HERE on purpose: the recycle bin is the owner of the
// deleted_at lifecycle, and reaching into another vertical's repository from
// a service would couple them — one repository, one responsibility.
type RecycleBinRepository interface {
	// Enqueue registers (or refreshes) the entry for one soft-deleted
	// entity. UNIQUE(entity_type, entity_id) makes it an upsert: re-deleting
	// a previously restored entity resets title/deleted_by/purge_at/
	// created_at and clears restored_at, restarting the 30-day window.
	Enqueue(ctx context.Context, entry *types.RecycleBinEntry) error
	// Get resolves one entry by ID within the tenant. Returns
	// gorm.ErrRecordNotFound when absent.
	Get(ctx context.Context, tenantID uint64, recycleID string) (*types.RecycleBinEntry, error)
	// List returns the tenant's entries, newest first; entityType filters by
	// kind when non-empty ("" = all kinds, restored entries included).
	List(ctx context.Context, tenantID uint64, entityType string) ([]*types.RecycleBinEntry, error)
	// MarkRestored stamps restored_at on one entry. Zero rows affected
	// surfaces gorm.ErrRecordNotFound.
	MarkRestored(ctx context.Context, tenantID uint64, recycleID string, restoredAt time.Time) error
	// Delete physically removes one recycle-bin row (after a successful
	// restore or purge — the row has served its purpose).
	Delete(ctx context.Context, tenantID uint64, recycleID string) error
	// ListDue returns every tenant's unrestored entries whose purge_at has
	// passed (PurgeDue's work queue), oldest deadline first.
	ListDue(ctx context.Context, now time.Time) ([]*types.RecycleBinEntry, error)

	// GetKnowledgeIncludingDeleted loads one knowledge row without the
	// soft-delete filter (restore precondition: the row must still exist;
	// entries whose row was already hard-deleted cannot be restored).
	GetKnowledgeIncludingDeleted(ctx context.Context, tenantID uint64, knowledgeID string) (*types.Knowledge, error)
	// RestoreKnowledge clears the knowledge row's deleted_at (Unscoped
	// UPDATE deleted_at = NULL, tenant-scoped). Zero rows affected surfaces
	// gorm.ErrRecordNotFound.
	RestoreKnowledge(ctx context.Context, tenantID uint64, knowledgeID string) error
	// HardDeleteKnowledge physically removes one knowledge row (Unscoped
	// DELETE, tenant-scoped). Zero rows affected surfaces
	// gorm.ErrRecordNotFound — purge treats that as "already gone".
	HardDeleteKnowledge(ctx context.Context, tenantID uint64, knowledgeID string) error
}

// RecycleBinService manages the soft-delete → restore/purge lifecycle
// (V3.0 §5.5).
type RecycleBinService interface {
	// EnqueueKnowledge records one soft-deleted knowledge entry in the
	// recycle bin. Called by the knowledge delete pipeline AFTER the row's
	// deleted_at is set (enqueuing earlier would put a live entity on the
	// purge path). Best-effort by design: a missing execution tenant or a
	// repository failure is logged, never propagated — the delete itself
	// must not fail because bookkeeping did.
	EnqueueKnowledge(ctx context.Context, knowledgeID, title, deletedBy string) error
	// List returns the tenant's recycle-bin entries (newest first), filtered
	// by entity type when non-empty.
	List(ctx context.Context, tenantID uint64, entityType string) ([]*types.RecycleBinEntry, error)
	// Restore resurrects one soft-deleted knowledge entry: cleared
	// deleted_at + stamped restored_at (audit: recycle.restored). Admin-only
	// upstream; v1 caveat: the delete pipeline already released chunks,
	// vectors and files, so restore brings back the metadata row for
	// re-ingest, not the indexed payload.
	Restore(ctx context.Context, tenantID uint64, recycleID, actorID string) (*types.RecycleBinEntry, error)
	// PurgeDue physically deletes every entity whose recycle entry is past
	// purge_at and not restored, then drops the entry rows
	// (audit: recycle.purged per entity). Caller wires a scheduler in a
	// later phase; v1 ships the method only.
	PurgeDue(ctx context.Context, now time.Time) (int64, error)
}
