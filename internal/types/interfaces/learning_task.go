package interfaces

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// LearningTaskService assigns and tracks learning work, and runs exams
// (KnowAxis V3.0 §15.5).
type LearningTaskService interface {
	// AssignForKnowledge creates learning tasks for a changed document.
	AssignForKnowledge(
		ctx context.Context, tenantID uint64, knowledgeID string,
		assignees []string, source types.LearningTaskSource, dueAt *time.Time,
	) ([]*types.LearningTask, error)
	// List returns a learner's tasks.
	List(
		ctx context.Context, tenantID uint64, assigneeID string, status types.LearningTaskStatus,
	) ([]*types.LearningTask, error)
	// Complete marks one task done (assignee only).
	Complete(ctx context.Context, tenantID uint64, taskID, assigneeID string) (*types.LearningTask, error)
	// CreateExamQuestion adds one item to a path's question bank.
	CreateExamQuestion(
		ctx context.Context, question *types.LearningExamQuestion,
	) (*types.LearningExamQuestion, error)
	// ListExamQuestions returns a path's question bank.
	ListExamQuestions(
		ctx context.Context, tenantID uint64, pathID string,
	) ([]*types.LearningExamQuestion, error)
	// SubmitExam grades a sitting and records the attempt.
	SubmitExam(
		ctx context.Context, tenantID uint64, pathID, userID string, choices map[string]int,
	) (*types.LearningExamAttempt, error)
}
