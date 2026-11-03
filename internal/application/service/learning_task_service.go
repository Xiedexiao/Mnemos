package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// learningTaskService implements interfaces.LearningTaskService
// (KnowAxis V3.0 §15.5).
//
// 分配逻辑是"知识 → 学它的人"：一条 SOP 变更后，所有把这条知识作为学习
// 步骤的路径都要给相关岗位派任务。平台此前完全没有这一环，改版只能靠口头
// 通知。
type learningTaskService struct {
	db  *gorm.DB
	now func() time.Time
}

// NewLearningTaskService constructs the service.
func NewLearningTaskService(db *gorm.DB) interfaces.LearningTaskService {
	return &learningTaskService{db: db, now: time.Now}
}

// AssignForKnowledge fans a changed document out to learning tasks.
//
// 幂等：同一 (知识, 指派对象) 已有 open 任务时不再新建——一次变更可能被
// 多次触发（分析、发布、复审），重复派任务会让学习清单变成噪声。
func (s *learningTaskService) AssignForKnowledge(
	ctx context.Context, tenantID uint64, knowledgeID string,
	assignees []string, source types.LearningTaskSource, dueAt *time.Time,
) ([]*types.LearningTask, error) {
	if s.db == nil {
		return nil, apperrors.NewInternalServerError("learning task store is not configured")
	}
	if knowledgeID == "" {
		return nil, apperrors.NewBadRequestError("knowledge_id is required")
	}
	if !source.Valid() {
		return nil, apperrors.NewBadRequestError("unknown learning task source")
	}
	if len(assignees) == 0 {
		return nil, nil
	}

	var title string
	s.db.WithContext(ctx).Model(&types.Knowledge{}).
		Select("title").Where("tenant_id = ? AND id = ?", tenantID, knowledgeID).
		Scan(&title)
	if title == "" {
		title = knowledgeID
	}

	created := make([]*types.LearningTask, 0, len(assignees))
	for _, assignee := range assignees {
		if assignee == "" {
			continue
		}
		var existing int64
		s.db.WithContext(ctx).Model(&types.LearningTask{}).
			Where("tenant_id = ? AND knowledge_id = ? AND assignee_id = ? AND status = ?",
				tenantID, knowledgeID, assignee, string(types.LearningTaskOpen)).
			Count(&existing)
		if existing > 0 {
			continue
		}
		task := &types.LearningTask{
			TenantID:    tenantID,
			KnowledgeID: knowledgeID,
			AssigneeID:  assignee,
			Source:      string(source),
			Title:       title,
			Status:      string(types.LearningTaskOpen),
			DueAt:       dueAt,
		}
		if err := s.db.WithContext(ctx).Create(task).Error; err != nil {
			return created, err
		}
		created = append(created, task)
	}
	return created, nil
}

// List returns a learner's tasks.
func (s *learningTaskService) List(
	ctx context.Context, tenantID uint64, assigneeID string, status types.LearningTaskStatus,
) ([]*types.LearningTask, error) {
	if s.db == nil {
		return nil, nil
	}
	if status != "" && !status.Valid() {
		return nil, apperrors.NewBadRequestError("unknown learning task status")
	}
	query := s.db.WithContext(ctx).Where("tenant_id = ? AND assignee_id = ?", tenantID, assigneeID)
	if status != "" {
		query = query.Where("status = ?", string(status))
	}
	var rows []*types.LearningTask
	err := query.Order("created_at DESC").Limit(100).Find(&rows).Error
	return rows, err
}

// Complete marks one task done and reports whether recertification is needed.
func (s *learningTaskService) Complete(
	ctx context.Context, tenantID uint64, taskID, assigneeID string,
) (*types.LearningTask, error) {
	if s.db == nil {
		return nil, apperrors.NewInternalServerError("learning task store is not configured")
	}
	var task types.LearningTask
	if err := s.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, taskID).
		First(&task).Error; err != nil {
		return nil, apperrors.NewNotFoundError("learning task not found")
	}
	// 只能完成自己的任务：代完成会让"谁学过"的记录失真。
	if task.AssigneeID != assigneeID {
		return nil, apperrors.NewForbiddenError("only the assignee can complete this task")
	}
	now := s.now()
	updates := map[string]interface{}{
		"status":       string(types.LearningTaskDone),
		"completed_at": now,
		"updated_at":   now,
	}
	if err := s.db.WithContext(ctx).
		Model(&types.LearningTask{}).
		Where("tenant_id = ? AND id = ?", tenantID, taskID).
		Updates(updates).Error; err != nil {
		return nil, err
	}
	task.Status = string(types.LearningTaskDone)
	task.CompletedAt = &now
	return &task, nil
}

// CreateExamQuestion adds one item to a path's question bank.
func (s *learningTaskService) CreateExamQuestion(
	ctx context.Context, question *types.LearningExamQuestion,
) (*types.LearningExamQuestion, error) {
	if s.db == nil {
		return nil, apperrors.NewInternalServerError("learning task store is not configured")
	}
	if err := question.Valid(); err != nil {
		return nil, apperrors.NewBadRequestError(err.Error())
	}
	if err := s.db.WithContext(ctx).Create(question).Error; err != nil {
		return nil, err
	}
	return question, nil
}

// ListExamQuestions returns a path's question bank.
func (s *learningTaskService) ListExamQuestions(
	ctx context.Context, tenantID uint64, pathID string,
) ([]*types.LearningExamQuestion, error) {
	if s.db == nil || pathID == "" {
		return nil, nil
	}
	var rows []*types.LearningExamQuestion
	err := s.db.WithContext(ctx).
		Where("tenant_id = ? AND path_id = ?", tenantID, pathID).
		Order("created_at ASC").
		Find(&rows).Error
	return rows, err
}

// SubmitExam grades a sitting and records the attempt.
func (s *learningTaskService) SubmitExam(
	ctx context.Context, tenantID uint64, pathID, userID string, choices map[string]int,
) (*types.LearningExamAttempt, error) {
	if s.db == nil {
		return nil, apperrors.NewInternalServerError("learning task store is not configured")
	}
	if pathID == "" || userID == "" {
		return nil, apperrors.NewBadRequestError("path_id and user_id are required")
	}
	questions, err := s.ListExamQuestions(ctx, tenantID, pathID)
	if err != nil {
		return nil, err
	}
	if len(questions) == 0 {
		return nil, apperrors.NewBadRequestError("the path has no exam questions")
	}
	score, answers := types.GradeExam(questions, choices)
	raw, marshalErr := json.Marshal(answers)
	if marshalErr != nil {
		raw = []byte("[]")
	}
	attempt := &types.LearningExamAttempt{
		TenantID: tenantID,
		PathID:   pathID,
		UserID:   userID,
		Score:    score,
		Passed:   score >= types.DefaultExamPassScore,
		Answers:  types.JSON(raw),
	}
	if err := s.db.WithContext(ctx).Create(attempt).Error; err != nil {
		return nil, err
	}
	return attempt, nil
}

// latestExamAttempt returns the newest attempt for a learner+path, or nil.
func (s *learningTaskService) latestExamAttempt(
	ctx context.Context, tenantID uint64, pathID, userID string,
) (*types.LearningExamAttempt, error) {
	if s.db == nil {
		return nil, nil
	}
	var attempt types.LearningExamAttempt
	err := s.db.WithContext(ctx).
		Where("tenant_id = ? AND path_id = ? AND user_id = ?", tenantID, pathID, userID).
		Order("created_at DESC").
		First(&attempt).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &attempt, nil
}
