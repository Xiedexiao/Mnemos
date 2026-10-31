package interfaces

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// FeedbackRepository is the storage primitive for per-user message ratings.
// Rows are keyed by (message_id, user_id): re-rating upserts, it never
// accumulates.
type FeedbackRepository interface {
	// Upsert inserts the feedback row or, when the same (message, user)
	// pair already exists, overwrites session_id / rating / reason_codes /
	// comment in place. The struct's ID is normalised to the stored row's
	// id so callers can echo it back safely.
	Upsert(ctx context.Context, feedback *types.MessageFeedback) error
	// ResolveMessageContext resolves the chat context of a message for the
	// feedback flow: the session it belongs to and the knowledge ids its
	// answer cited. Best-effort contract ("若可取到"): a missing message,
	// a pruned session, or an answer without citations returns zero values
	// and no error, so rating still succeeds and the threshold sweep
	// degrades to "skip" instead of failing the rating.
	ResolveMessageContext(
		ctx context.Context, tenantID uint64, messageID string,
	) (sessionID string, knowledgeIDs []string, err error)
	// CountRecentUselessByKnowledge counts useless ratings recorded since
	// the given instant against messages that cite the knowledge. This is
	// the threshold-sweep counter (V3.0 §14.5): >= 3 within the trailing
	// window opens a useless_threshold task.
	CountRecentUselessByKnowledge(
		ctx context.Context, tenantID uint64, knowledgeID string, since time.Time,
	) (int64, error)
}

// QualityTaskRepository is the storage primitive for governance tasks.
type QualityTaskRepository interface {
	// Create inserts one task. Status defaults to open when empty.
	Create(ctx context.Context, task *types.QualityTask) error
	// GetByID returns the tenant-scoped task, or nil when it does not
	// exist in the tenant.
	GetByID(ctx context.Context, tenantID uint64, taskID string) (*types.QualityTask, error)
	// List returns the tenant's tasks, newest first, optionally filtered
	// by status (empty status = all). The implementation caps the page.
	List(ctx context.Context, tenantID uint64, status types.QualityTaskStatus) ([]*types.QualityTask, error)
	// Update persists the full task row (status, assignee, note,
	// resolved_at). The service owns transition validation; the repo
	// writes what it is given.
	Update(ctx context.Context, task *types.QualityTask) error
	// HasOpenByKnowledgeAndType reports whether an open task of the given
	// type already exists for the knowledge — the dedup guard that keeps
	// the threshold sweep from re-opening the same governance item on
	// every new useless rating.
	HasOpenByKnowledgeAndType(
		ctx context.Context, tenantID uint64, taskType types.QualityTaskType, knowledgeID string,
	) (bool, error)
}

// FeedbackService is the user-facing interaction API (V3.0 §14.3): rate an
// answer, report a knowledge entry. Both write audit events; the useless
// path additionally drives the §14.5 threshold sweep.
type FeedbackService interface {
	// RateMessage records (or overwrites) one user's rating of one message
	// and emits feedback.recorded. When the rating is useless and the
	// message's cited knowledge is resolvable, the trailing-window useless
	// count is checked and a useless_threshold task auto-opens at the
	// threshold unless one is already open for that knowledge.
	RateMessage(
		ctx context.Context,
		tenantID uint64,
		messageID string,
		userID string,
		rating types.FeedbackRating,
		reasonCodes []string,
		comment string,
	) (*types.MessageFeedback, error)
	// ReportKnowledge files a correction / stale_report quality task
	// against a knowledge entry and emits quality_task.created.
	ReportKnowledge(
		ctx context.Context,
		tenantID uint64,
		knowledgeID string,
		userID string,
		taskType types.QualityTaskType,
		chunkID string,
		comment string,
	) (*types.QualityTask, error)
}

// QualityTaskService is the Admin-side triage API (V3.0 §14.5): list the
// queue, move tasks along the status machine with resolution evidence.
type QualityTaskService interface {
	// List returns the tenant's tasks, newest first. statusFilter accepts
	// "" (all) or one of the four known statuses; anything else is a
	// validation error.
	List(
		ctx context.Context, tenantID uint64, statusFilter types.QualityTaskStatus,
	) ([]*types.QualityTask, error)
	// Update applies a partial update (status / assignee / note) after
	// validating the status transition. Moving to done or waived requires
	// a non-empty resolution note (the waived reason lives in the same
	// field); reopening clears resolved_at.
	Update(
		ctx context.Context,
		tenantID uint64,
		taskID string,
		input *types.QualityTaskUpdateInput,
	) (*types.QualityTask, error)
}
