package types

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ============================================================================
// 学习任务与考试（KnowAxis V3.0 §15.5）。
//
// §15.5 要求"与 SOP 变更联动：新版本发布 → 学习任务"与"与考试/上岗资格
// 关联"。平台此前只有学习路径和逐步完成：SOP 改版不会给任何人派任务，也
// 没有题库，认证只能靠"进度到 100%"。
// ============================================================================

// LearningTaskSource names why a learning task was assigned.
type LearningTaskSource string

const (
	// LearningTaskSourceSOPChange 知识变更触发（§15.5 SOP 变更联动）。
	LearningTaskSourceSOPChange LearningTaskSource = "sop_change"
	// LearningTaskSourceReviewCycle 复审后要求重新学习。
	LearningTaskSourceReviewCycle LearningTaskSource = "review_cycle"
	// LearningTaskSourceCertification 认证前要求补齐。
	LearningTaskSourceCertification LearningTaskSource = "certification"
	// LearningTaskSourceManual 人工指派。
	LearningTaskSourceManual LearningTaskSource = "manual"
)

// Valid reports whether the source is known.
func (s LearningTaskSource) Valid() bool {
	switch s {
	case LearningTaskSourceSOPChange, LearningTaskSourceReviewCycle,
		LearningTaskSourceCertification, LearningTaskSourceManual:
		return true
	default:
		return false
	}
}

// LearningTaskStatus is the task lifecycle.
type LearningTaskStatus string

const (
	// LearningTaskOpen needs the learner to study it.
	LearningTaskOpen LearningTaskStatus = "open"
	// LearningTaskDone was completed.
	LearningTaskDone LearningTaskStatus = "done"
	// LearningTaskWaived was exempted (already qualified, role changed).
	LearningTaskWaived LearningTaskStatus = "waived"
)

// Valid reports whether the status is known.
func (s LearningTaskStatus) Valid() bool {
	switch s {
	case LearningTaskOpen, LearningTaskDone, LearningTaskWaived:
		return true
	default:
		return false
	}
}

// LearningTask is one "you need to study this" assignment.
type LearningTask struct {
	ID       string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID uint64 `json:"tenant_id" gorm:"not null"`
	// PathID / StepOrder point at the path step when one exists.
	PathID    string `json:"path_id" gorm:"type:varchar(36);not null;default:''"`
	StepOrder int    `json:"step_order" gorm:"not null;default:0"`
	// KnowledgeID is the content to study (usually the changed document).
	KnowledgeID string     `json:"knowledge_id" gorm:"type:varchar(36);not null;default:''"`
	AssigneeID  string     `json:"assignee_id" gorm:"type:varchar(36);not null"`
	Source      string     `json:"source" gorm:"type:varchar(24);not null;default:'manual'"`
	Title       string     `json:"title" gorm:"type:varchar(255);not null;default:''"`
	Status      string     `json:"status" gorm:"type:varchar(16);not null;default:'open'"`
	DueAt       *time.Time `json:"due_at"`
	CompletedAt *time.Time `json:"completed_at"`
	// Recertified records whether the learner was re-certified after finishing.
	Recertified bool      `json:"recertified" gorm:"not null;default:false"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// TableName pins the GORM table name.
func (LearningTask) TableName() string { return "learning_tasks" }

// BeforeCreate assigns an ID before persistence.
func (t *LearningTask) BeforeCreate(_ *gorm.DB) error {
	if t.ID == "" {
		t.ID = uuid.NewString()
	}
	return nil
}

// LearningExamQuestion is one exam item (§15.5 考试关联).
type LearningExamQuestion struct {
	ID       string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID uint64 `json:"tenant_id" gorm:"not null"`
	PathID   string `json:"path_id" gorm:"type:varchar(36);not null"`
	Prompt   string `json:"prompt" gorm:"type:varchar(512);not null"`
	// Options is the JSON array of answer choices.
	Options JSON `json:"options" gorm:"type:json;not null;default:'[]'"`
	// CorrectIndex is the index of the right option.
	CorrectIndex int       `json:"correct_index" gorm:"not null;default:0"`
	Explanation  string    `json:"explanation" gorm:"type:text;not null;default:''"`
	CreatedBy    string    `json:"created_by" gorm:"type:varchar(36);not null;default:''"`
	CreatedAt    time.Time `json:"created_at"`
}

// TableName pins the GORM table name.
func (LearningExamQuestion) TableName() string { return "learning_exam_questions" }

// BeforeCreate assigns an ID before persistence.
func (q *LearningExamQuestion) BeforeCreate(_ *gorm.DB) error {
	if q.ID == "" {
		q.ID = uuid.NewString()
	}
	return nil
}

// OptionsList decodes the choices; a bad blob yields an empty list.
func (q *LearningExamQuestion) OptionsList() []string {
	if q == nil || len(q.Options) == 0 {
		return nil
	}
	var options []string
	if err := json.Unmarshal(q.Options, &options); err != nil {
		return nil
	}
	return options
}

// Valid reports whether the question is well-formed.
func (q *LearningExamQuestion) Valid() error {
	if q == nil {
		return fmt.Errorf("question is required")
	}
	if q.Prompt == "" {
		return fmt.Errorf("prompt is required")
	}
	options := q.OptionsList()
	if len(options) < 2 {
		return fmt.Errorf("at least two options are required")
	}
	if q.CorrectIndex < 0 || q.CorrectIndex >= len(options) {
		return fmt.Errorf("correct_index is out of range")
	}
	return nil
}

// LearningExamAnswer is one submitted choice.
type LearningExamAnswer struct {
	QuestionID string `json:"question_id"`
	Choice     int    `json:"choice"`
	Correct    bool   `json:"correct"`
}

// LearningExamAttempt is one exam sitting.
type LearningExamAttempt struct {
	ID       string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID uint64 `json:"tenant_id" gorm:"not null"`
	PathID   string `json:"path_id" gorm:"type:varchar(36);not null"`
	UserID   string `json:"user_id" gorm:"type:varchar(36);not null"`
	Score    int    `json:"score" gorm:"not null;default:0"`
	Passed   bool   `json:"passed" gorm:"not null;default:false"`
	// Answers is the JSON array of LearningExamAnswer.
	Answers   JSON      `json:"answers" gorm:"type:json;not null;default:'[]'"`
	CreatedAt time.Time `json:"created_at"`
}

// TableName pins the GORM table name.
func (LearningExamAttempt) TableName() string { return "learning_exam_attempts" }

// BeforeCreate assigns an ID before persistence.
func (a *LearningExamAttempt) BeforeCreate(_ *gorm.DB) error {
	if a.ID == "" {
		a.ID = uuid.NewString()
	}
	return nil
}

// DefaultExamPassScore is the minimum passing score (§15.5 认证门槛).
const DefaultExamPassScore = 80

// GradeExam scores a set of answers against the question bank.
//
// 判分只依赖题库里的 correct_index，不看提交里自带的 correct 字段：允许客户端
// 声明对错等于让考生自己判卷。
func GradeExam(
	questions []*LearningExamQuestion, choices map[string]int,
) (int, []LearningExamAnswer) {
	if len(questions) == 0 {
		return 0, nil
	}
	answers := make([]LearningExamAnswer, 0, len(questions))
	correct := 0
	for _, question := range questions {
		if question == nil {
			continue
		}
		choice, answered := choices[question.ID]
		isCorrect := answered && choice == question.CorrectIndex
		if isCorrect {
			correct++
		}
		answers = append(answers, LearningExamAnswer{
			QuestionID: question.ID,
			Choice:     choice,
			Correct:    isCorrect,
		})
	}
	scored := len(answers)
	if scored == 0 {
		return 0, answers
	}
	return correct * 100 / scored, answers
}
