package types

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ============================================================================
// 官方答案（KnowAxis V3.0 §13.7）。
//
// 高频、高风险问题需要一份经过审批的标准答案：带条件、例外、适用范围、
// 有效期、Owner 与证据。文档片段现拼的答案做不到这一点——同一个问题每次
// 措辞都可能不同，也无法回答"这条口径是谁批的、什么时候到期"。
//
// 状态机刻意保持极简：
//
//	draft → in_review → approved → published
//	                         ↑           ↓
//	                      草稿修改    expired / superseded / retired
//
// 发布必须绑定审批回执（§6.7/§13.7），与知识发布同一门槛。
// ============================================================================

// OfficialAnswerStatus is the lifecycle state of an official answer.
type OfficialAnswerStatus string

const (
	// OfficialAnswerDraft still being written.
	OfficialAnswerDraft OfficialAnswerStatus = "draft"
	// OfficialAnswerInReview awaiting approval.
	OfficialAnswerInReview OfficialAnswerStatus = "in_review"
	// OfficialAnswerApproved approved but not yet published (may wait for its
	// effective date).
	OfficialAnswerApproved OfficialAnswerStatus = "approved"
	// OfficialAnswerPublished currently answering questions.
	OfficialAnswerPublished OfficialAnswerStatus = "published"
	// OfficialAnswerExpired past its validity window (or evidence went stale).
	OfficialAnswerExpired OfficialAnswerStatus = "expired"
	// OfficialAnswerSuperseded replaced by a newer answer.
	OfficialAnswerSuperseded OfficialAnswerStatus = "superseded"
	// OfficialAnswerRetired withdrawn without replacement.
	OfficialAnswerRetired OfficialAnswerStatus = "retired"
)

// Valid reports whether the status is known.
func (s OfficialAnswerStatus) Valid() bool {
	switch s {
	case OfficialAnswerDraft, OfficialAnswerInReview, OfficialAnswerApproved,
		OfficialAnswerPublished, OfficialAnswerExpired, OfficialAnswerSuperseded,
		OfficialAnswerRetired:
		return true
	default:
		return false
	}
}

// Answerable reports whether the answer may back a response right now.
func (s OfficialAnswerStatus) Answerable() bool {
	return s == OfficialAnswerApproved || s == OfficialAnswerPublished
}

// officialAnswerTransitions is the legal state machine. Anything not listed is
// rejected: an official answer that could jump from draft to published would
// defeat the point of having an approval.
var officialAnswerTransitions = map[OfficialAnswerStatus][]OfficialAnswerStatus{
	OfficialAnswerDraft:      {OfficialAnswerInReview, OfficialAnswerRetired},
	OfficialAnswerInReview:   {OfficialAnswerApproved, OfficialAnswerDraft, OfficialAnswerRetired},
	OfficialAnswerApproved:   {OfficialAnswerPublished, OfficialAnswerInReview, OfficialAnswerRetired},
	OfficialAnswerPublished:  {OfficialAnswerExpired, OfficialAnswerSuperseded, OfficialAnswerRetired, OfficialAnswerInReview},
	OfficialAnswerExpired:    {OfficialAnswerPublished, OfficialAnswerSuperseded, OfficialAnswerRetired},
	OfficialAnswerSuperseded: {OfficialAnswerRetired},
	OfficialAnswerRetired:    {},
}

// CanTransitionTo reports whether the state change is legal.
func (s OfficialAnswerStatus) CanTransitionTo(next OfficialAnswerStatus) bool {
	for _, allowed := range officialAnswerTransitions[s] {
		if allowed == next {
			return true
		}
	}
	return false
}

// OfficialAnswer is one approved standard answer (§13.7).
type OfficialAnswer struct {
	ID       string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID uint64 `json:"tenant_id" gorm:"not null;index:idx_official_answers_tenant_status,priority:1"`
	// QuestionNormalized is the lookup key: one active answer per question.
	QuestionNormalized string `json:"question_normalized" gorm:"type:varchar(512);not null"`
	Question           string `json:"question" gorm:"type:varchar(512);not null"`
	Answer             string `json:"answer" gorm:"type:text;not null;default:''"`
	// Conditions describe when the answer holds (§13.7 包含条件).
	Conditions string `json:"conditions" gorm:"type:text;not null;default:''"`
	// Exceptions describe when it does not apply.
	Exceptions string `json:"exceptions" gorm:"type:text;not null;default:''"`
	// Scope narrows applicability (model/region/customer/version).
	Scope string `json:"scope" gorm:"type:varchar(512);not null;default:''"`
	// RiskLevel drives whether a missing receipt blocks publishing.
	RiskLevel string `json:"risk_level" gorm:"type:varchar(16);not null;default:'mid'"`
	Status    string `json:"status" gorm:"type:varchar(16);not null;default:'draft';index:idx_official_answers_tenant_status,priority:2"`
	// EffectiveAt/ExpiresAt are the validity window.
	EffectiveAt *time.Time `json:"effective_at"`
	ExpiresAt   *time.Time `json:"expires_at"`
	// OwnerID is the person accountable for this answer.
	OwnerID string `json:"owner_id" gorm:"type:varchar(36);not null;default:''"`
	// Evidence lists the knowledge IDs backing the answer.
	Evidence JSON `json:"evidence" gorm:"type:json;not null;default:'[]'"`
	// ReceiptID is the approval receipt that authorised publishing.
	ReceiptID string `json:"receipt_id" gorm:"type:varchar(64);not null;default:''"`
	// SupersededByID points at the replacement.
	SupersededByID string `json:"superseded_by_id" gorm:"type:varchar(36);not null;default:''"`
	// KnowledgeBaseID scopes visibility so an answer is never wider than the
	// knowledge it summarises (§2.4 派生权限).
	KnowledgeBaseID string    `json:"knowledge_base_id" gorm:"type:varchar(36);not null;default:''"`
	Version         int       `json:"version" gorm:"not null;default:0"`
	CreatedBy       string    `json:"created_by" gorm:"type:varchar(36);not null;default:''"`
	ReviewedBy      string    `json:"reviewed_by" gorm:"type:varchar(36);not null;default:''"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// TableName pins the GORM table name.
func (OfficialAnswer) TableName() string { return "official_answers" }

// BeforeCreate assigns an ID before persistence.
func (a *OfficialAnswer) BeforeCreate(_ *gorm.DB) error {
	if a.ID == "" {
		a.ID = uuid.NewString()
	}
	return nil
}

// EvidenceIDs decodes the evidence list; a bad blob yields an empty list so a
// corrupt row cannot block the answer path.
func (a *OfficialAnswer) EvidenceIDs() []string {
	if a == nil || len(a.Evidence) == 0 {
		return nil
	}
	var ids []string
	if err := json.Unmarshal(a.Evidence, &ids); err != nil {
		return nil
	}
	return ids
}

// SetEvidenceIDs stores the evidence list.
func (a *OfficialAnswer) SetEvidenceIDs(ids []string) {
	raw, err := json.Marshal(ids)
	if err != nil {
		raw = []byte("[]")
	}
	a.Evidence = JSON(raw)
}

// NormalizeOfficialAnswerQuestion is the lookup key normalisation: lowercase,
// collapsed whitespace, trailing punctuation trimmed. It mirrors the knowledge
// gap clustering so a gap and the answer that closes it line up.
func NormalizeOfficialAnswerQuestion(question string) string {
	normalized := strings.Join(strings.Fields(strings.ToLower(question)), " ")
	normalized = strings.TrimRight(normalized, "?？!！。. ")
	if runes := []rune(normalized); len(runes) > 512 {
		normalized = string(runes[:512])
	}
	return normalized
}

// ResolveOfficialAnswerState folds the validity window into the stored status so
// a published answer past its expiry never answers as if current (§7.1 读取时
// 解析生效/失效，不做静默改写).
func ResolveOfficialAnswerState(answer *OfficialAnswer, now time.Time) OfficialAnswerStatus {
	if answer == nil {
		return OfficialAnswerRetired
	}
	status := OfficialAnswerStatus(answer.Status)
	if !status.Valid() {
		return OfficialAnswerDraft
	}
	if status != OfficialAnswerApproved && status != OfficialAnswerPublished {
		return status
	}
	if answer.ExpiresAt != nil && now.After(*answer.ExpiresAt) {
		return OfficialAnswerExpired
	}
	if answer.EffectiveAt != nil && now.Before(*answer.EffectiveAt) {
		// 已批准但未到生效日：可以展示，但必须标注"尚未生效"。
		return OfficialAnswerApproved
	}
	return OfficialAnswerPublished
}

// OfficialAnswerMatch is one hit from the answer lookup.
type OfficialAnswerMatch struct {
	// Answer is the record.
	Answer *OfficialAnswer `json:"answer"`
	// EffectiveStatus is the read-time resolved state.
	EffectiveStatus OfficialAnswerStatus `json:"effective_status"`
	// Exact reports whether the stored question matched exactly (after
	// normalisation) versus a prefix/containment match.
	Exact bool `json:"exact"`
}
