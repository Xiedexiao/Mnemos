package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// Sentinel errors the handler maps to HTTP status codes.
var (
	// ErrFeedbackInvalidRating — rating must be useful|useless.
	ErrFeedbackInvalidRating = errors.New("invalid feedback rating")
	// ErrFeedbackMissingMessage — the message id is required.
	ErrFeedbackMissingMessage = errors.New("message id is required")
	// ErrKnowledgeFeedbackEmptyID — the knowledge id is required.
	ErrKnowledgeFeedbackEmptyID = errors.New("knowledge id is required")
	// ErrKnowledgeFeedbackType — only correction / stale_report may be
	// filed by users; useless_threshold is system-generated and
	// low_rating has no producer yet.
	ErrKnowledgeFeedbackType = errors.New("quality task type must be correction or stale_report")
)

// Threshold sweep parameters (V3.0 §14.5): when one knowledge collects at
// least uselessFeedbackThreshold useless ratings within
// uselessFeedbackWindow, a governance task auto-opens — unless one of the
// same type is already open for that knowledge.
const (
	uselessFeedbackThreshold = 3
	uselessFeedbackWindow    = 30 * 24 * time.Hour
)

// feedbackService implements interfaces.FeedbackService.
//
// Failure philosophy mirrors the audit service: the rating itself is the
// primary write and must not fail because peripheral lookups (message
// context, threshold count, audit trail) hiccup. Those degrade to a warning
// and the feedback still lands.
type feedbackService struct {
	feedbackRepo interfaces.FeedbackRepository
	taskRepo     interfaces.QualityTaskRepository
	audit        interfaces.AuditLogService
	now          func() time.Time
}

// NewFeedbackService constructs the production feedback service. taskRepo
// and audit may be nil in degraded assemblies (lite mode): the rating still
// records, the threshold sweep and audit trail just skip.
func NewFeedbackService(
	feedbackRepo interfaces.FeedbackRepository,
	taskRepo interfaces.QualityTaskRepository,
	audit interfaces.AuditLogService,
) interfaces.FeedbackService {
	return &feedbackService{
		feedbackRepo: feedbackRepo,
		taskRepo:     taskRepo,
		audit:        audit,
		now:          time.Now,
	}
}

// RateMessage records (or overwrites) one user's rating of one message.
//
// Flow: resolve the message context (session + cited knowledge, best
// effort) -> upsert the feedback row -> audit -> when the rating is useless,
// run the §14.5 threshold sweep per cited knowledge.
func (s *feedbackService) RateMessage(
	ctx context.Context,
	tenantID uint64,
	messageID string,
	userID string,
	rating types.FeedbackRating,
	reasonCodes []string,
	comment string,
) (*types.MessageFeedback, error) {
	if tenantID == 0 {
		return nil, fmt.Errorf("feedback: tenant id is required")
	}
	if messageID == "" || userID == "" {
		return nil, ErrFeedbackMissingMessage
	}
	if !rating.Valid() {
		return nil, ErrFeedbackInvalidRating
	}

	feedback := &types.MessageFeedback{
		ID:          uuid.NewString(),
		TenantID:    tenantID,
		MessageID:   messageID,
		UserID:      userID,
		Rating:      rating,
		ReasonCodes: reasonCodesJSON(reasonCodes),
		Comment:     comment,
	}

	// 若可取到: backfill the session and cited knowledge from the stored
	// message. A missing/pruned message or a lookup error only costs the
	// session column and the threshold sweep, never the rating.
	knowledgeIDs, err := s.resolveCitedKnowledge(ctx, tenantID, messageID, feedback)
	if err != nil {
		logger.WarnWithFields(ctx, logger.Fields{
			"error":      err.Error(),
			"message_id": messageID,
			"tenant_id":  tenantID,
		}, "feedback: resolve message context failed; threshold sweep skipped")
	}

	if err := s.feedbackRepo.Upsert(ctx, feedback); err != nil {
		return nil, err
	}
	s.logFeedbackRecorded(ctx, feedback)

	if rating == types.FeedbackRatingUseless {
		s.sweepUselessThreshold(ctx, tenantID, messageID, userID, knowledgeIDs)
	}
	return feedback, nil
}

// ReportKnowledge files a correction / stale_report task against one
// knowledge entry. The route guard already verified read access to the
// knowledge's KB and rewrote the tenant context to the owner workspace, so
// the task lands with the content's governance owner.
func (s *feedbackService) ReportKnowledge(
	ctx context.Context,
	tenantID uint64,
	knowledgeID string,
	userID string,
	taskType types.QualityTaskType,
	chunkID string,
	comment string,
) (*types.QualityTask, error) {
	if tenantID == 0 {
		return nil, fmt.Errorf("knowledge feedback: tenant id is required")
	}
	if knowledgeID == "" {
		return nil, ErrKnowledgeFeedbackEmptyID
	}
	if !taskType.Reportable() {
		return nil, ErrKnowledgeFeedbackType
	}

	task := &types.QualityTask{
		ID:         uuid.NewString(),
		TenantID:   tenantID,
		TaskType:   taskType,
		SourceType: types.QualityTaskSourceKnowledge,
		SourceID:   knowledgeID,
		KnowledgeID: knowledgeID,
		ChunkID:    chunkID,
		Detail: mustDetailsJSON(map[string]interface{}{
			"comment":     comment,
			"reporter_id": userID,
		}),
		Status: types.QualityTaskStatusOpen,
	}
	if s.taskRepo == nil {
		return nil, fmt.Errorf("knowledge feedback: quality task storage unavailable")
	}
	if err := s.taskRepo.Create(ctx, task); err != nil {
		return nil, err
	}
	s.logQualityTaskCreated(ctx, task, userID)
	return task, nil
}

// resolveCitedKnowledge fills feedback.SessionID and returns the distinct
// knowledge ids the answer cited. The repository's best-effort contract
// ("若可取到") means a missing message returns zero values and no error.
func (s *feedbackService) resolveCitedKnowledge(
	ctx context.Context,
	tenantID uint64,
	messageID string,
	feedback *types.MessageFeedback,
) ([]string, error) {
	sessionID, knowledgeIDs, err := s.feedbackRepo.ResolveMessageContext(ctx, tenantID, messageID)
	if err != nil {
		return nil, err
	}
	feedback.SessionID = sessionID
	return knowledgeIDs, nil
}

// sweepUselessThreshold runs the §14.5 auto-task check per cited knowledge:
// count >= 3 useless ratings in the trailing 30 days and no open
// useless_threshold task for that knowledge yet -> open one. Every failure
// inside the sweep is logged and skipped: it must never fail the rating
// that triggered it.
func (s *feedbackService) sweepUselessThreshold(
	ctx context.Context,
	tenantID uint64,
	messageID string,
	userID string,
	knowledgeIDs []string,
) {
	if s.taskRepo == nil || len(knowledgeIDs) == 0 {
		return
	}
	since := s.now().Add(-uselessFeedbackWindow)
	for _, knowledgeID := range knowledgeIDs {
		if knowledgeID == "" {
			continue
		}
		count, err := s.feedbackRepo.CountRecentUselessByKnowledge(ctx, tenantID, knowledgeID, since)
		if err != nil {
			logger.WarnWithFields(ctx, logger.Fields{
				"error":        err.Error(),
				"knowledge_id": knowledgeID,
				"tenant_id":    tenantID,
			}, "feedback: useless threshold count failed")
			continue
		}
		if count < uselessFeedbackThreshold {
			continue
		}
		exists, err := s.taskRepo.HasOpenByKnowledgeAndType(
			ctx, tenantID, types.QualityTaskTypeUselessThreshold, knowledgeID)
		if err != nil {
			logger.WarnWithFields(ctx, logger.Fields{
				"error":        err.Error(),
				"knowledge_id": knowledgeID,
				"tenant_id":    tenantID,
			}, "feedback: open task dedup check failed")
			continue
		}
		if exists {
			continue
		}
		task := &types.QualityTask{
			ID:         uuid.NewString(),
			TenantID:   tenantID,
			TaskType:   types.QualityTaskTypeUselessThreshold,
			SourceType: types.QualityTaskSourceMessage,
			SourceID:   messageID,
			KnowledgeID: knowledgeID,
			Detail: mustDetailsJSON(map[string]interface{}{
				"useless_count":    count,
				"threshold":        uselessFeedbackThreshold,
				"window_days":      int(uselessFeedbackWindow / (24 * time.Hour)),
				"triggered_by":     messageID,
				"latest_rater_id":  userID,
			}),
			Status: types.QualityTaskStatusOpen,
		}
		if err := s.taskRepo.Create(ctx, task); err != nil {
			logger.WarnWithFields(ctx, logger.Fields{
				"error":        err.Error(),
				"knowledge_id": knowledgeID,
				"tenant_id":    tenantID,
			}, "feedback: auto quality task creation failed")
			continue
		}
		// Actor is empty on system-generated tasks (the sweep is the
		// actor), matching the system-audit convention.
		s.logQualityTaskCreated(ctx, task, "")
	}
}

// logFeedbackRecorded writes the feedback.recorded audit event. Audit
// failure never blocks the rating (same contract as the audit service).
func (s *feedbackService) logFeedbackRecorded(ctx context.Context, feedback *types.MessageFeedback) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Log(ctx, &types.AuditLog{
		TenantID:    feedback.TenantID,
		ActorUserID: feedback.UserID,
		Action:      types.AuditActionFeedbackRecorded,
		ScopeType:   "session",
		ScopeID:     feedback.SessionID,
		TargetType:  "message",
		TargetID:    feedback.MessageID,
		Outcome:     types.AuditOutcomeSuccess,
		Details: mustDetailsJSON(map[string]interface{}{
			"rating":       string(feedback.Rating),
			"reason_codes": feedback.ReasonCodes,
			"has_comment":  feedback.Comment != "",
		}),
	})
}

// logQualityTaskCreated writes the quality_task.created audit event.
// actorID is the reporting user for user-filed tasks and empty for
// system-generated ones.
func (s *feedbackService) logQualityTaskCreated(
	ctx context.Context, task *types.QualityTask, actorID string,
) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Log(ctx, &types.AuditLog{
		TenantID:    task.TenantID,
		ActorUserID: actorID,
		Action:      types.AuditActionQualityTaskCreated,
		ScopeType:   string(task.SourceType),
		ScopeID:     task.SourceID,
		TargetType:  "quality_task",
		TargetID:    task.ID,
		Outcome:     types.AuditOutcomeSuccess,
		Details: mustDetailsJSON(map[string]interface{}{
			"task_type":    string(task.TaskType),
			"knowledge_id": task.KnowledgeID,
			"chunk_id":     task.ChunkID,
			"detail":       task.Detail,
		}),
	})
}

// reasonCodesJSON marshals the reason list; an empty list serialises to []
// rather than SQL NULL so the column keeps its NOT NULL contract.
func reasonCodesJSON(reasonCodes []string) types.JSON {
	if len(reasonCodes) == 0 {
		return types.JSON([]byte("[]"))
	}
	data, err := json.Marshal(reasonCodes)
	if err != nil {
		return types.JSON([]byte("[]"))
	}
	return types.JSON(data)
}

// mustDetailsJSON marshals an audit/task detail payload, falling back to an
// empty object on failure so logging never blocks a feedback write. Kept
// local to the vertical instead of reusing the lifecycle helper so the two
// verticals stay independently maintainable.
func mustDetailsJSON(v interface{}) types.JSON {
	data, err := json.Marshal(v)
	if err != nil {
		return types.JSON([]byte("{}"))
	}
	return types.JSON(data)
}
