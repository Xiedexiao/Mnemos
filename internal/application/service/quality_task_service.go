package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// Sentinel errors the handler maps to HTTP status codes.
var (
	// ErrQualityTaskNotFound — the task does not exist in this tenant.
	ErrQualityTaskNotFound = errors.New("quality task not found")
	// ErrQualityTaskInvalidStatus — an unknown status filter or target.
	ErrQualityTaskInvalidStatus = errors.New("invalid quality task status")
	// ErrQualityTaskIllegalTransition — the status machine rejected the move.
	ErrQualityTaskIllegalTransition = errors.New("illegal quality task status transition")
	// ErrQualityTaskResolutionNoteRequired — closing (done) or waiving a
	// task demands resolution evidence; the waived reason lives in the
	// same resolution_note field (V3.0 §14.5 流转校验).
	ErrQualityTaskResolutionNoteRequired = errors.New("resolution note is required to close or waive a quality task")
)

// qualityTaskService implements interfaces.QualityTaskService. It is the
// single door through which quality tasks change: transitions are validated
// against the status machine and every accepted move writes an audit event,
// mirroring the knowledge lifecycle service's hard rules.
type qualityTaskService struct {
	taskRepo interfaces.QualityTaskRepository
	audit    interfaces.AuditLogService
	now      func() time.Time
}

// NewQualityTaskService constructs the production quality task service.
// audit may be nil in degraded assemblies: triage still works, the audit
// trail just skips.
func NewQualityTaskService(
	taskRepo interfaces.QualityTaskRepository,
	audit interfaces.AuditLogService,
) interfaces.QualityTaskService {
	return &qualityTaskService{
		taskRepo: taskRepo,
		audit:    audit,
		now:      time.Now,
	}
}

// List returns the tenant's tasks newest-first. statusFilter accepts ""
// (all) or one of the four known statuses.
func (s *qualityTaskService) List(
	ctx context.Context,
	tenantID uint64,
	statusFilter types.QualityTaskStatus,
) ([]*types.QualityTask, error) {
	if tenantID == 0 {
		return nil, fmt.Errorf("quality task: tenant id is required")
	}
	if statusFilter != "" && !statusFilter.Valid() {
		return nil, ErrQualityTaskInvalidStatus
	}
	return s.taskRepo.List(ctx, tenantID, statusFilter)
}

// Update applies a partial update after validating the status transition.
//
// Rules (V3.0 §14.5):
//   - only provided fields change (pointer semantics on the input);
//   - the status machine gates the move (illegal moves are audited as
//     denied, like lifecycle transitions);
//   - moving to done or waived requires a non-empty resolution note —
//     the note already on the task counts, so "processing -> done" can
//     rely on a note set in an earlier call;
//   - entering a terminal state stamps resolved_at, leaving one clears it.
func (s *qualityTaskService) Update(
	ctx context.Context,
	tenantID uint64,
	taskID string,
	input *types.QualityTaskUpdateInput,
) (*types.QualityTask, error) {
	if tenantID == 0 {
		return nil, fmt.Errorf("quality task: tenant id is required")
	}
	if taskID == "" {
		return nil, ErrQualityTaskNotFound
	}
	if input == nil {
		input = &types.QualityTaskUpdateInput{}
	}
	task, err := s.taskRepo.GetByID(ctx, tenantID, taskID)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, ErrQualityTaskNotFound
	}

	before := task.Status
	target := before
	if input.Status != nil {
		if !input.Status.Valid() {
			return nil, ErrQualityTaskInvalidStatus
		}
		target = *input.Status
	}

	// The note the task will hold after this update.
	note := task.ResolutionNote
	if input.ResolutionNote != nil {
		note = *input.ResolutionNote
	}

	if target != before {
		if !before.CanTransitionTo(target) {
			s.logTaskRejected(ctx, task, before, target)
			return nil, fmt.Errorf("%w: %s -> %s", ErrQualityTaskIllegalTransition, before, target)
		}
		if target.Closed() && strings.TrimSpace(note) == "" {
			return nil, ErrQualityTaskResolutionNoteRequired
		}
	}

	if input.AssigneeID != nil {
		task.AssigneeID = *input.AssigneeID
	}
	task.ResolutionNote = note
	task.Status = target
	switch {
	case target.Closed() && !before.Closed():
		resolvedAt := s.now()
		task.ResolvedAt = &resolvedAt
	case !target.Closed():
		task.ResolvedAt = nil
	}

	if err := s.taskRepo.Update(ctx, task); err != nil {
		return nil, err
	}
	s.logTaskUpdated(ctx, task, before)
	return task, nil
}

// actorID resolves the authenticated caller for audit rows; empty when the
// context carries none (system paths).
func (s *qualityTaskService) actorID(ctx context.Context) string {
	id, _ := types.UserIDFromContext(ctx)
	return id
}

// logTaskUpdated writes the quality_task.updated audit event.
func (s *qualityTaskService) logTaskUpdated(
	ctx context.Context, task *types.QualityTask, before types.QualityTaskStatus,
) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Log(ctx, &types.AuditLog{
		TenantID:    task.TenantID,
		ActorUserID: s.actorID(ctx),
		Action:      types.AuditActionQualityTaskUpdated,
		ScopeType:   string(task.SourceType),
		ScopeID:     task.SourceID,
		TargetType:  "quality_task",
		TargetID:    task.ID,
		Outcome:     types.AuditOutcomeSuccess,
		Details: mustDetailsJSON(map[string]interface{}{
			"task_type":     string(task.TaskType),
			"from_status":   string(before),
			"to_status":     string(task.Status),
			"assignee_id":   task.AssigneeID,
			"has_note":      task.ResolutionNote != "",
			"knowledge_id":  task.KnowledgeID,
		}),
	})
}

// logTaskRejected writes a denied audit row for an illegal transition —
// probing the queue with invalid moves is a governance signal worth keeping.
func (s *qualityTaskService) logTaskRejected(
	ctx context.Context,
	task *types.QualityTask,
	before, target types.QualityTaskStatus,
) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Log(ctx, &types.AuditLog{
		TenantID:    task.TenantID,
		ActorUserID: s.actorID(ctx),
		Action:      types.AuditActionQualityTaskUpdated,
		ScopeType:   string(task.SourceType),
		ScopeID:     task.SourceID,
		TargetType:  "quality_task",
		TargetID:    task.ID,
		Outcome:     types.AuditOutcomeDenied,
		Details: mustDetailsJSON(map[string]interface{}{
			"task_type":   string(task.TaskType),
			"from_status": string(before),
			"to_status":   string(target),
			"action":      "illegal_transition",
		}),
	})
}
