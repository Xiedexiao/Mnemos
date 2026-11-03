package types

import (
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ============================================================================
// 知识问答社区（KnowAxis V3.0 §15.6）。
//
// 文档评论只能"对文档说"，不能"向人问"。一线员工的隐性经验因此仍然留在
// 群聊里：没有提问、没有回答、没有采纳，更谈不上"高频问题一键转知识"。
//
// 状态机：
//
//	open ──answered──▶ answered ──accept──▶ resolved
//	  └──────────────close──────────────▶ closed
// ============================================================================

// CommunityQuestionStatus is the lifecycle of a question.
type CommunityQuestionStatus string

const (
	// CommunityQuestionOpen 已发布，尚无回答。
	CommunityQuestionOpen CommunityQuestionStatus = "open"
	// CommunityQuestionAnswered 已有回答，等待采纳。
	CommunityQuestionAnswered CommunityQuestionStatus = "answered"
	// CommunityQuestionResolved 已采纳答案。
	CommunityQuestionResolved CommunityQuestionStatus = "resolved"
	// CommunityQuestionClosed 已关闭（撤回或转化）。
	CommunityQuestionClosed CommunityQuestionStatus = "closed"
)

// Valid reports whether the status is known.
func (s CommunityQuestionStatus) Valid() bool {
	switch s {
	case CommunityQuestionOpen, CommunityQuestionAnswered,
		CommunityQuestionResolved, CommunityQuestionClosed:
		return true
	default:
		return false
	}
}

// CommunityQuestion is one question posted in the community.
type CommunityQuestion struct {
	ID       string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID uint64 `json:"tenant_id" gorm:"not null;index:idx_community_questions_tenant_status,priority:1"`
	Title    string `json:"title" gorm:"type:varchar(255);not null"`
	Body     string `json:"body" gorm:"type:text;not null;default:''"`
	// KnowledgeID links the question to the document it is about (optional).
	KnowledgeID string `json:"knowledge_id" gorm:"type:varchar(36);not null;default:''"`
	// ExpertID is the person @-mentioned as the intended answerer (optional).
	ExpertID string `json:"expert_id" gorm:"type:varchar(36);not null;default:''"`
	// Bounty is the points offered for a good answer (§15.6 悬赏).
	Bounty int    `json:"bounty" gorm:"not null;default:0"`
	Status string `json:"status" gorm:"type:varchar(16);not null;default:'open';index:idx_community_questions_tenant_status,priority:2"`
	// AcceptedAnswerID is the answer the asker adopted.
	AcceptedAnswerID string `json:"accepted_answer_id" gorm:"type:varchar(36);not null;default:''"`
	// ConvertedKnowledgeID is set when the Q&A was promoted to knowledge
	// (§15.6 一键转知识).
	ConvertedKnowledgeID string    `json:"converted_knowledge_id" gorm:"type:varchar(36);not null;default:''"`
	AskedBy              string    `json:"asked_by" gorm:"type:varchar(36);not null"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
	// Answers is filled by the service on read.
	Answers []*CommunityAnswer `json:"answers,omitempty" gorm:"-"`
}

// TableName pins the GORM table name.
func (CommunityQuestion) TableName() string { return "community_questions" }

// BeforeCreate assigns an ID before persistence.
func (q *CommunityQuestion) BeforeCreate(_ *gorm.DB) error {
	if q.ID == "" {
		q.ID = uuid.NewString()
	}
	return nil
}

// CommunityAnswer is one answer to a question.
type CommunityAnswer struct {
	ID         string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID   uint64 `json:"tenant_id" gorm:"not null"`
	QuestionID string `json:"question_id" gorm:"type:varchar(36);not null;index:idx_community_answers_question,priority:1"`
	Body       string `json:"body" gorm:"type:text;not null"`
	AnsweredBy string `json:"answered_by" gorm:"type:varchar(36);not null"`
	// Votes is the "this helped" count (§15.6 答案评价).
	Votes int `json:"votes" gorm:"not null;default:0"`
	// Accepted marks the adopted answer; at most one per question.
	Accepted  bool      `json:"accepted" gorm:"not null;default:false"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName pins the GORM table name.
func (CommunityAnswer) TableName() string { return "community_answers" }

// BeforeCreate assigns an ID before persistence.
func (a *CommunityAnswer) BeforeCreate(_ *gorm.DB) error {
	if a.ID == "" {
		a.ID = uuid.NewString()
	}
	return nil
}

// CommunityReportStatus is the moderation state of a report.
type CommunityReportStatus string

const (
	// CommunityReportOpen needs a moderator decision.
	CommunityReportOpen CommunityReportStatus = "open"
	// CommunityReportDismissed was reviewed and rejected.
	CommunityReportDismissed CommunityReportStatus = "dismissed"
	// CommunityReportRemoved took the content down.
	CommunityReportRemoved CommunityReportStatus = "removed"
)

// Valid reports whether the status is known.
func (s CommunityReportStatus) Valid() bool {
	switch s {
	case CommunityReportOpen, CommunityReportDismissed, CommunityReportRemoved:
		return true
	default:
		return false
	}
}

// CommunityReport is one moderation report (§15.6 治理).
type CommunityReport struct {
	ID       string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID uint64 `json:"tenant_id" gorm:"not null"`
	// TargetType is question or answer.
	TargetType string    `json:"target_type" gorm:"type:varchar(16);not null"`
	TargetID   string    `json:"target_id" gorm:"type:varchar(36);not null"`
	Reason     string    `json:"reason" gorm:"type:varchar(512);not null;default:''"`
	ReportedBy string    `json:"reported_by" gorm:"type:varchar(36);not null"`
	Status     string    `json:"status" gorm:"type:varchar(16);not null;default:'open'"`
	HandledBy  string    `json:"handled_by" gorm:"type:varchar(36);not null;default:''"`
	CreatedAt  time.Time `json:"created_at"`
}

// TableName pins the GORM table name.
func (CommunityReport) TableName() string { return "community_reports" }

// BeforeCreate assigns an ID before persistence.
func (r *CommunityReport) BeforeCreate(_ *gorm.DB) error {
	if r.ID == "" {
		r.ID = uuid.NewString()
	}
	return nil
}

// NormalizeCommunityTitle trims and bounds a question title.
func NormalizeCommunityTitle(title string) string {
	trimmed := strings.TrimSpace(title)
	if runes := []rune(trimmed); len(runes) > 255 {
		return string(runes[:255])
	}
	return trimmed
}

// ValidateCommunityBounty bounds the offered points.
func ValidateCommunityBounty(bounty int) bool {
	return bounty >= 0 && bounty <= 10000
}
