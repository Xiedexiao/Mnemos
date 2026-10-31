package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

// Recycle-bin audit actions (V3.0 §5.5). The enqueue itself is bookkeeping —
// the delete pipeline already records knowledge.deleted/batch_deleted — so
// only the two recycle-bin-owned transitions are audited here.
const (
	auditActionRecycleRestored = "recycle.restored"
	auditActionRecyclePurged   = "recycle.purged"
)

// maxRecycleTitleLen mirrors the migration's varchar(255) (runes, defensive
// against multibyte titles blowing the column).
const maxRecycleTitleLen = 255

// recycleBinService implements interfaces.RecycleBinService.
type recycleBinService struct {
	repo  interfaces.RecycleBinRepository
	audit interfaces.AuditLogService
	now   func() time.Time
}

// NewRecycleBinService constructs the recycle-bin service.
func NewRecycleBinService(
	repo interfaces.RecycleBinRepository,
	audit interfaces.AuditLogService,
) interfaces.RecycleBinService {
	return &recycleBinService{repo: repo, audit: audit, now: time.Now}
}

// recycleBinDetailsJSON marshals an audit detail payload, falling back to an
// empty object on failure so logging never blocks a recycle-bin write. Local
// to this vertical and deliberately not named like the lifecycle/feedback
// helpers (same keep-verticals-independent rationale as glossaryDetailsJSON).
func recycleBinDetailsJSON(v interface{}) types.JSON {
	data, err := json.Marshal(v)
	if err != nil {
		return types.JSON([]byte("{}"))
	}
	return types.JSON(data)
}

// EnqueueKnowledge records one soft-deleted knowledge entry. MUST be called
// after the knowledge row's deleted_at is actually set (in the delete
// pipeline, not at request admission) — an entry here is a hard-delete
// sentence at purge_at, and enqueuing a live entity would make PurgeDue
// destroy content that was never deleted.
//
// Best-effort by design: a missing execution tenant (degenerate worker
// context) or a repository failure is logged and swallowed — the delete
// itself must not fail because bookkeeping did. Re-deletes upsert via the
// UNIQUE(entity_type, entity_id) constraint and restart the 30-day window.
func (s *recycleBinService) EnqueueKnowledge(ctx context.Context, knowledgeID, title, deletedBy string) error {
	if knowledgeID == "" {
		return nil
	}
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		logger.Warnf(ctx, "recycle bin enqueue skipped for knowledge %s: no execution tenant in context", knowledgeID)
		return nil
	}
	if utf8.RuneCountInString(title) > maxRecycleTitleLen {
		title = string([]rune(title)[:maxRecycleTitleLen])
	}
	now := s.now().UTC()
	entry := &types.RecycleBinEntry{
		TenantID:   tenantID,
		EntityType: string(types.RecycleEntityTypeKnowledge),
		EntityID:   knowledgeID,
		Title:      title,
		DeletedBy:  deletedBy,
		PurgeAt:    now.Add(types.RecycleBinRetentionDays * 24 * time.Hour),
		CreatedAt:  now,
	}
	if err := s.repo.Enqueue(ctx, entry); err != nil {
		logger.Warnf(ctx, "recycle bin enqueue failed for knowledge %s: %v", knowledgeID, err)
		return nil
	}
	return nil
}

// List returns the tenant's recycle-bin entries, newest first; entityType
// filters by kind when non-empty and must be a known kind.
func (s *recycleBinService) List(
	ctx context.Context, tenantID uint64, entityType string,
) ([]*types.RecycleBinEntry, error) {
	if entityType != "" && !types.RecycleEntityType(entityType).Valid() {
		return nil, apperrors.NewBadRequestError("unknown entity_type filter")
	}
	entries, err := s.repo.List(ctx, tenantID, entityType)
	if err != nil {
		return nil, err
	}
	if entries == nil {
		entries = []*types.RecycleBinEntry{}
	}
	return entries, nil
}

// Restore resurrects one soft-deleted knowledge entry: cleared deleted_at +
// stamped restored_at (audit: recycle.restored). Admin-only upstream (the
// route guard carries g.Admin()).
//
// v1 caveat (documented on the interface): the delete pipeline already
// released chunks, vectors and files, so restore brings back the metadata
// row for re-ingest — the indexed payload is not resurrected.
func (s *recycleBinService) Restore(
	ctx context.Context, tenantID uint64, recycleID, actorID string,
) (*types.RecycleBinEntry, error) {
	entry, err := s.repo.Get(ctx, tenantID, recycleID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperrors.NewNotFoundError("recycle bin entry not found")
		}
		return nil, err
	}
	if types.RecycleEntityType(entry.EntityType) != types.RecycleEntityTypeKnowledge {
		return nil, apperrors.NewBadRequestError("only knowledge entries can be restored in v1")
	}
	if entry.IsRestored() {
		return nil, apperrors.NewBadRequestError("entry is already restored")
	}
	// The row must still exist (a purged-then-listed race or manual cleanup
	// leaves the entry pointing at nothing).
	if _, err := s.repo.GetKnowledgeIncludingDeleted(ctx, tenantID, entry.EntityID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperrors.NewNotFoundError("knowledge row no longer exists; nothing to restore")
		}
		return nil, err
	}
	if err := s.repo.RestoreKnowledge(ctx, tenantID, entry.EntityID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperrors.NewNotFoundError("knowledge row no longer exists; nothing to restore")
		}
		return nil, err
	}
	restoredAt := s.now().UTC()
	if err := s.repo.MarkRestored(ctx, tenantID, recycleID, restoredAt); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperrors.NewNotFoundError("recycle bin entry not found")
		}
		return nil, err
	}
	s.logAudit(ctx, tenantID, actorID, auditActionRecycleRestored, entry,
		map[string]interface{}{
			"entity_type": entry.EntityType,
			"entity_id":   entry.EntityID,
			"title":       entry.Title,
			"deleted_by":  entry.DeletedBy,
		})
	entry.RestoredAt = &restoredAt
	return entry, nil
}

// PurgeDue physically deletes every entity whose recycle entry passed its
// purge deadline without being restored, then drops the entry rows
// (audit: recycle.purged per entity). Caller wires a scheduler in a later
// phase; v1 ships the method only. Per-entry failures are logged and
// skipped so one bad row cannot wedge the sweep; the next run retries them.
func (s *recycleBinService) PurgeDue(ctx context.Context, now time.Time) (int64, error) {
	entries, err := s.repo.ListDue(ctx, now)
	if err != nil {
		return 0, err
	}
	var purged int64
	for _, entry := range entries {
		// Knowledge rows first: a failed hard-delete keeps the entry for the
		// next sweep (deleting the entry row first would orphan the entity).
		if err := s.repo.HardDeleteKnowledge(ctx, entry.TenantID, entry.EntityID); err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				logger.Warnf(ctx, "recycle bin purge failed for knowledge %s: %v", entry.EntityID, err)
				continue
			}
			// Row already gone (manual cleanup upstream) — still retire the entry.
		}
		if err := s.repo.Delete(ctx, entry.TenantID, entry.ID); err != nil {
			logger.Warnf(ctx, "recycle bin purge failed to drop entry %s: %v", entry.ID, err)
			continue
		}
		purged++
		s.logAudit(ctx, entry.TenantID, "", auditActionRecyclePurged, entry,
			map[string]interface{}{
				"entity_type": entry.EntityType,
				"entity_id":   entry.EntityID,
				"title":       entry.Title,
				"deleted_by":  entry.DeletedBy,
				"purge_at":    entry.PurgeAt,
			})
	}
	return purged, nil
}

// logAudit writes one recycle-bin audit event; audit failures never block
// the business write (same policy as the other verticals). actorID is empty
// for scheduler-driven purges — system work stays unattributed by design.
func (s *recycleBinService) logAudit(
	ctx context.Context, tenantID uint64, actorID, action string,
	entry *types.RecycleBinEntry, details map[string]interface{},
) {
	if s.audit == nil || entry == nil {
		return
	}
	_ = s.audit.Log(ctx, &types.AuditLog{
		TenantID:    tenantID,
		ActorUserID: actorID,
		Action:      types.AuditAction(action),
		ScopeType:   "recycle_bin",
		TargetType:  "knowledge",
		TargetID:    entry.EntityID,
		Outcome:     "success",
		Details:     recycleBinDetailsJSON(details),
	})
}
