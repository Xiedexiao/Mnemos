package types

import (
	"time"
)

// Feedback & quality-task domain (KnowAxis V3.0 §13.8, §14.3, §14.5).
//
// Two tables back the vertical:
//   - message_feedback: one row per (message, user). A user re-rating the
//     same answer overwrites the previous verdict instead of piling up rows,
//     so "how many users found this useless" counts people, not clicks.
//   - quality_tasks: governance work items. Opened by user reports
//     (correction / stale_report) or automatically by the useless-rating
//     threshold sweep (useless_threshold); triaged by Admins.

// FeedbackRating is the verdict a user gives one answer.
type FeedbackRating string

const (
	// FeedbackRatingUseful marks the answer as helpful.
	FeedbackRatingUseful FeedbackRating = "useful"
	// FeedbackRatingUseless marks the answer as unhelpful. Useless ratings
	// are the only signal that feeds the §14.5 threshold sweep.
	FeedbackRatingUseless FeedbackRating = "useless"
)

// Valid reports whether the value is a known rating.
func (r FeedbackRating) Valid() bool {
	switch r {
	case FeedbackRatingUseful, FeedbackRatingUseless:
		return true
	default:
		return false
	}
}

// String renders the rating for logs and audit payloads.
func (r FeedbackRating) String() string { return string(r) }

// QualityTaskType names one class of governance work item.
type QualityTaskType string

const (
	// QualityTaskTypeUselessThreshold is opened automatically when one
	// knowledge collects >= 3 useless message ratings within the trailing
	// window (V3.0 §14.5). System-generated only — users cannot file it.
	QualityTaskTypeUselessThreshold QualityTaskType = "useless_threshold"
	// QualityTaskTypeCorrection is filed by a user reporting a content error.
	QualityTaskTypeCorrection QualityTaskType = "correction"
	// QualityTaskTypeStaleReport is filed by a user reporting outdated content.
	QualityTaskTypeStaleReport QualityTaskType = "stale_report"
	// QualityTaskTypeLowRating is reserved for aggregate low-score signals;
	// no producer ships in V1 but the value is legal in storage.
	QualityTaskTypeLowRating QualityTaskType = "low_rating"
	// QualityTaskTypeSamplingFailed is opened when a §6.6 sampling inspection
	// fails: a published document was found wrong after release, which needs
	// correction and a look at why approval missed it.
	QualityTaskTypeSamplingFailed QualityTaskType = "sampling_failed"
)

// Valid reports whether the value is a known task type.
func (t QualityTaskType) Valid() bool {
	switch t {
	case QualityTaskTypeUselessThreshold, QualityTaskTypeCorrection,
		QualityTaskTypeStaleReport, QualityTaskTypeLowRating,
		QualityTaskTypeSamplingFailed:
		return true
	default:
		return false
	}
}

// Reportable reports whether a user may file the type directly through
// POST /knowledge/:id/feedback. useless_threshold stays system-only so a
// caller cannot fabricate threshold evidence; low_rating has no producer yet.
func (t QualityTaskType) Reportable() bool {
	return t == QualityTaskTypeCorrection || t == QualityTaskTypeStaleReport
}

// SystemOnly reports whether the type is produced by the platform only.
//
// sampling_failed 只能由抽检失败产生：允许用户直接提交它，等于让任何人凭空
// 给一篇内容贴上"抽检不通过"的结论。
func (t QualityTaskType) SystemOnly() bool {
	return t == QualityTaskTypeUselessThreshold || t == QualityTaskTypeSamplingFailed
}

// String renders the task type for logs and audit payloads.
func (t QualityTaskType) String() string { return string(t) }

// QualityTaskSourceType names the resource a quality task points at.
type QualityTaskSourceType string

const (
	// QualityTaskSourceMessage tasks point at one chat message
	// (source_id = message id).
	QualityTaskSourceMessage QualityTaskSourceType = "message"
	// QualityTaskSourceKnowledge tasks point at one knowledge entry
	// (source_id = knowledge id).
	QualityTaskSourceKnowledge QualityTaskSourceType = "knowledge"
)

// Valid reports whether the value is a known source type.
func (s QualityTaskSourceType) Valid() bool {
	switch s {
	case QualityTaskSourceMessage, QualityTaskSourceKnowledge:
		return true
	default:
		return false
	}
}

// String renders the source type for logs and audit payloads.
func (s QualityTaskSourceType) String() string { return string(s) }

// QualityTaskStatus is the triage state of a quality task. Like the
// lifecycle machine, every status change must go through QualityTaskService
// so the transition stays validated and audited.
type QualityTaskStatus string

const (
	// QualityTaskStatusOpen 新建待处理。
	QualityTaskStatusOpen QualityTaskStatus = "open"
	// QualityTaskStatusProcessing 已认领/处理中。
	QualityTaskStatusProcessing QualityTaskStatus = "processing"
	// QualityTaskStatusDone 已处理完成（需 resolution_note）。
	QualityTaskStatusDone QualityTaskStatus = "done"
	// QualityTaskStatusWaived 已豁免（有意不处理，需理由）。
	QualityTaskStatusWaived QualityTaskStatus = "waived"
)

// qualityTaskStatusTransitions lists the legal moves. done/waived can be
// reopened — a waived item that later turns out to matter must be able to
// come back without editing the database by hand.
var qualityTaskStatusTransitions = map[QualityTaskStatus][]QualityTaskStatus{
	QualityTaskStatusOpen:       {QualityTaskStatusProcessing, QualityTaskStatusDone, QualityTaskStatusWaived},
	QualityTaskStatusProcessing: {QualityTaskStatusOpen, QualityTaskStatusDone, QualityTaskStatusWaived},
	QualityTaskStatusDone:       {QualityTaskStatusOpen, QualityTaskStatusWaived},
	QualityTaskStatusWaived:     {QualityTaskStatusOpen, QualityTaskStatusDone},
}

// CanTransitionTo reports whether moving from one status to another is legal.
func (s QualityTaskStatus) CanTransitionTo(next QualityTaskStatus) bool {
	if s == next {
		return false
	}
	for _, allowed := range qualityTaskStatusTransitions[s] {
		if allowed == next {
			return true
		}
	}
	return false
}

// Closed reports whether the status is a terminal resolution (done/waived).
// Closing a task demands a resolution note (V3.0 §14.5 流转校验).
func (s QualityTaskStatus) Closed() bool {
	return s == QualityTaskStatusDone || s == QualityTaskStatusWaived
}

// Valid reports whether the value is a known status.
func (s QualityTaskStatus) Valid() bool {
	switch s {
	case QualityTaskStatusOpen, QualityTaskStatusProcessing,
		QualityTaskStatusDone, QualityTaskStatusWaived:
		return true
	default:
		return false
	}
}

// String renders the status for logs and audit payloads.
func (s QualityTaskStatus) String() string { return string(s) }

// Audit actions for the feedback / quality-task vertical. Defined here (not
// in types/audit_log.go) so the vertical owns its own action namespace, the
// same way the lifecycle service owns knowledge.lifecycle_*.
const (
	// AuditActionFeedbackRecorded fires when a user rates a message
	// (including overwriting an earlier rating).
	AuditActionFeedbackRecorded AuditAction = "feedback.recorded"
	// AuditActionQualityTaskCreated fires when a quality task is created,
	// either by a user report or by the threshold sweep.
	AuditActionQualityTaskCreated AuditAction = "quality_task.created"
	// AuditActionQualityTaskUpdated fires on every accepted status /
	// assignee / note change of a quality task.
	AuditActionQualityTaskUpdated AuditAction = "quality_task.updated"
)

// MessageFeedback is one user's rating of one chat answer. reason_codes is a
// JSON array of machine-readable reason identifiers (e.g. "outdated",
// "wrong", "not_relevant"); comment carries the free-text explanation.
type MessageFeedback struct {
	ID          string         `json:"id"          gorm:"type:varchar(36);primaryKey"`
	TenantID    uint64         `json:"tenant_id"   gorm:"not null"`
	MessageID   string         `json:"message_id"  gorm:"type:varchar(36);not null;uniqueIndex:uq_message_feedback_message_user,priority:1"`
	SessionID   string         `json:"session_id"  gorm:"type:varchar(36);not null;default:''"`
	UserID      string         `json:"user_id"     gorm:"type:varchar(36);not null;uniqueIndex:uq_message_feedback_message_user,priority:2"`
	Rating      FeedbackRating `json:"rating"      gorm:"type:varchar(16);not null"`
	ReasonCodes JSON           `json:"reason_codes" gorm:"type:jsonb;not null;default:'[]'"`
	Comment     string         `json:"comment"     gorm:"type:text;not null;default:''"`
	CreatedAt   time.Time      `json:"created_at"`
}

// TableName pins the GORM table name.
func (MessageFeedback) TableName() string { return "message_feedback" }

// QualityTask is one governance work item. detail carries producer-specific
// context (threshold evidence, reporter comment, ...) without another
// migration; source_type + source_id identify the flagged resource.
type QualityTask struct {
	ID             string                `json:"id"             gorm:"type:varchar(36);primaryKey"`
	TenantID       uint64                `json:"tenant_id"      gorm:"not null"`
	TaskType       QualityTaskType       `json:"task_type"      gorm:"type:varchar(24);not null"`
	SourceType     QualityTaskSourceType `json:"source_type"    gorm:"type:varchar(16);not null"`
	SourceID       string                `json:"source_id"      gorm:"type:varchar(36);not null;default:''"`
	KnowledgeID    string                `json:"knowledge_id"   gorm:"type:varchar(36);not null;default:'';index:idx_quality_tasks_knowledge"`
	ChunkID        string                `json:"chunk_id"       gorm:"type:varchar(36);not null;default:''"`
	Detail         JSON                  `json:"detail"         gorm:"type:jsonb;not null;default:'{}'"`
	Status         QualityTaskStatus     `json:"status"         gorm:"type:varchar(16);not null;default:'open';index:idx_quality_tasks_tenant_status,priority:2"`
	AssigneeID     string                `json:"assignee_id"    gorm:"type:varchar(36);not null;default:''"`
	ResolutionNote string                `json:"resolution_note" gorm:"type:text;not null;default:''"`
	CreatedAt      time.Time             `json:"created_at"     gorm:"index:idx_quality_tasks_tenant_status,priority:1"`
	UpdatedAt      time.Time             `json:"updated_at"`
	ResolvedAt     *time.Time            `json:"resolved_at"`
}

// TableName pins the GORM table name.
func (QualityTask) TableName() string { return "quality_tasks" }

// QualityTaskUpdateInput carries one partial quality-task update. Pointers
// distinguish "field unset" from zero: only provided fields change, and a
// provided-but-empty ResolutionNote is what lets an operator explicitly
// clear a note before reopening.
type QualityTaskUpdateInput struct {
	// Status moves the task along the transition machine; nil keeps it.
	Status *QualityTaskStatus
	// AssigneeID reassigns the task; nil keeps the current assignee.
	AssigneeID *string
	// ResolutionNote replaces the resolution note; nil keeps the stored one.
	ResolutionNote *string
}
