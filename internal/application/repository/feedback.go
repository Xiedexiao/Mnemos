package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// qualityTaskListLimitMax caps one List page regardless of caller input so
// an unfiltered "everything" query cannot drag the whole table over the
// wire. Governance queues stay small; the cap is abuse headroom, not a
// pagination contract.
const qualityTaskListLimitMax = 200

// feedbackRepository persists per-user message ratings.
type feedbackRepository struct {
	db *gorm.DB
}

// NewFeedbackRepository constructs the production feedback repository
// backed by the shared GORM connection.
func NewFeedbackRepository(db *gorm.DB) interfaces.FeedbackRepository {
	return &feedbackRepository{db: db}
}

// Upsert inserts the feedback row or overwrites the existing (message,
// user) row. The unique constraint uq_message_feedback_message_user is the
// conflict target, so a re-rating replaces rating / reason_codes / comment
// (and backfills session_id) while keeping the original row id and
// created_at — "counts people, not clicks".
func (r *feedbackRepository) Upsert(ctx context.Context, feedback *types.MessageFeedback) error {
	if feedback == nil {
		return fmt.Errorf("message feedback: nil row")
	}
	if feedback.ID == "" {
		feedback.ID = uuid.NewString()
	}
	err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "message_id"}, {Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"session_id", "rating", "reason_codes", "comment",
		}),
	}).Create(feedback).Error
	if err != nil {
		return fmt.Errorf("upsert message feedback: %w", err)
	}
	// On the conflict path the row keeps its original id while Create()
	// leaves the freshly generated one on the struct; read the stored id
	// back so the caller's copy matches the database row. A failed
	// read-back is not worth failing the rating over.
	var stored types.MessageFeedback
	if err := r.db.WithContext(ctx).
		Select("id").
		Where("message_id = ? AND user_id = ?", feedback.MessageID, feedback.UserID).
		First(&stored).Error; err == nil && stored.ID != "" {
		feedback.ID = stored.ID
	}
	return nil
}

// ResolveMessageContext returns the session and cited knowledge ids of one
// message. The tenant boundary rides on the session join (messages carry no
// tenant column of their own): a message whose session lives in another
// tenant is indistinguishable from a missing one.
func (r *feedbackRepository) ResolveMessageContext(
	ctx context.Context, tenantID uint64, messageID string,
) (string, []string, error) {
	if messageID == "" {
		return "", nil, nil
	}
	var row struct {
		ID                  string           `gorm:"column:id"`
		SessionID           string           `gorm:"column:session_id"`
		KnowledgeReferences types.References `gorm:"column:knowledge_references"`
	}
	err := r.db.WithContext(ctx).
		Table("messages").
		Joins("JOIN sessions ON sessions.id = messages.session_id AND sessions.tenant_id = ?", tenantID).
		Select("messages.id", "messages.session_id", "messages.knowledge_references").
		Where("messages.id = ? AND messages.deleted_at IS NULL", messageID).
		Scan(&row).Error
	if err != nil {
		return "", nil, fmt.Errorf("resolve message feedback context: %w", err)
	}
	ids := make([]string, 0, len(row.KnowledgeReferences))
	seen := make(map[string]bool, len(row.KnowledgeReferences))
	for _, ref := range row.KnowledgeReferences {
		if ref == nil || ref.KnowledgeID == "" || seen[ref.KnowledgeID] {
			continue
		}
		seen[ref.KnowledgeID] = true
		ids = append(ids, ref.KnowledgeID)
	}
	return row.SessionID, ids, nil
}

// CountRecentUselessByKnowledge counts useless ratings since the cutoff
// against messages citing the knowledge. The (message_id, user_id) unique
// key guarantees one row per rater, so the count is "how many people said
// useless", not "how often". JSON traversal syntax branches per dialect,
// matching the pattern used by the chunk repository.
func (r *feedbackRepository) CountRecentUselessByKnowledge(
	ctx context.Context, tenantID uint64, knowledgeID string, since time.Time,
) (int64, error) {
	if knowledgeID == "" {
		return 0, nil
	}
	query := r.db.WithContext(ctx).
		Model(&types.MessageFeedback{}).
		Joins("JOIN messages m ON m.id = message_feedback.message_id AND m.deleted_at IS NULL").
		Where("message_feedback.tenant_id = ?", tenantID).
		Where("message_feedback.rating = ?", string(types.FeedbackRatingUseless)).
		Where("message_feedback.created_at >= ?", since)
	switch r.db.Dialector.Name() {
	case "postgres":
		// knowledge_references is a json array of citation objects; guard
		// the array traversal against NULL / non-array legacy values.
		query = query.Where(
			"EXISTS (SELECT 1 FROM jsonb_array_elements("+
				"CASE WHEN jsonb_typeof(m.knowledge_references::jsonb) = 'array' "+
				"THEN m.knowledge_references::jsonb ELSE '[]'::jsonb END) AS ref "+
				"WHERE ref->>'knowledge_id' = ?)",
			knowledgeID)
	case "mysql":
		containment, _ := json.Marshal(map[string]string{"knowledge_id": knowledgeID})
		query = query.Where("JSON_CONTAINS(m.knowledge_references, ?)", string(containment))
	default: // sqlite
		query = query.Where(
			"EXISTS (SELECT 1 FROM json_each("+
				"CASE WHEN m.knowledge_references IS NOT NULL AND json_valid(m.knowledge_references) "+
				"AND json_type(m.knowledge_references) = 'array' "+
				"THEN m.knowledge_references ELSE '[]' END) AS ref "+
				"WHERE json_extract(ref.value, '$.knowledge_id') = ?)",
			knowledgeID)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return 0, fmt.Errorf("count recent useless feedback: %w", err)
	}
	return count, nil
}

// qualityTaskRepository persists governance tasks.
type qualityTaskRepository struct {
	db *gorm.DB
}

// NewQualityTaskRepository constructs the production quality task
// repository backed by the shared GORM connection.
func NewQualityTaskRepository(db *gorm.DB) interfaces.QualityTaskRepository {
	return &qualityTaskRepository{db: db}
}

// Create inserts one task, defaulting id / timestamps / status when the
// producer left them zero.
func (r *qualityTaskRepository) Create(ctx context.Context, task *types.QualityTask) error {
	if task == nil {
		return fmt.Errorf("quality task: nil row")
	}
	now := time.Now()
	if task.ID == "" {
		task.ID = uuid.NewString()
	}
	if task.CreatedAt.IsZero() {
		task.CreatedAt = now
	}
	if task.UpdatedAt.IsZero() {
		task.UpdatedAt = now
	}
	if task.Status == "" {
		task.Status = types.QualityTaskStatusOpen
	}
	if err := r.db.WithContext(ctx).Create(task).Error; err != nil {
		return fmt.Errorf("create quality task: %w", err)
	}
	return nil
}

// GetByID returns the tenant-scoped task or nil when it does not exist.
func (r *qualityTaskRepository) GetByID(
	ctx context.Context, tenantID uint64, taskID string,
) (*types.QualityTask, error) {
	if taskID == "" {
		return nil, nil
	}
	var task types.QualityTask
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, taskID).
		First(&task).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get quality task: %w", err)
	}
	return &task, nil
}

// List returns the tenant's tasks newest-first, optionally filtered by
// status ("" = all). The id tiebreaker keeps same-millisecond rows in a
// stable order across pages.
func (r *qualityTaskRepository) List(
	ctx context.Context, tenantID uint64, status types.QualityTaskStatus,
) ([]*types.QualityTask, error) {
	query := r.db.WithContext(ctx).Where("tenant_id = ?", tenantID)
	if status != "" {
		query = query.Where("status = ?", status)
	}
	var tasks []*types.QualityTask
	if err := query.
		Order("created_at DESC").Order("id DESC").
		Limit(qualityTaskListLimitMax).
		Find(&tasks).Error; err != nil {
		return nil, fmt.Errorf("list quality tasks: %w", err)
	}
	return tasks, nil
}

// Update persists the full task row. GORM fills UpdatedAt automatically.
func (r *qualityTaskRepository) Update(ctx context.Context, task *types.QualityTask) error {
	if task == nil || task.ID == "" {
		return fmt.Errorf("quality task: update requires a persisted row")
	}
	if err := r.db.WithContext(ctx).Save(task).Error; err != nil {
		return fmt.Errorf("update quality task: %w", err)
	}
	return nil
}

// HasOpenByKnowledgeAndType is the threshold-sweep dedup guard: one open
// useless_threshold task per knowledge is enough to work the queue.
func (r *qualityTaskRepository) HasOpenByKnowledgeAndType(
	ctx context.Context, tenantID uint64, taskType types.QualityTaskType, knowledgeID string,
) (bool, error) {
	if knowledgeID == "" {
		return false, nil
	}
	var count int64
	err := r.db.WithContext(ctx).
		Model(&types.QualityTask{}).
		Where("tenant_id = ? AND task_type = ? AND knowledge_id = ? AND status = ?",
			tenantID, string(taskType), knowledgeID, string(types.QualityTaskStatusOpen)).
		Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("check open quality task: %w", err)
	}
	return count > 0, nil
}
