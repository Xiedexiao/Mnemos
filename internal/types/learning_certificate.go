package types

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ============================================================================
// 学习路径认证（KnowAxis V3.0 §15.5）。
//
// 平台已有"学习路径 + 逐步完成"，但完成到 100% 只是记了一个时间戳：无法
// 回答"这个人是否已被认证可以独立上岗"。§15.5 要求学习路径与上岗资格关联，
// 所以需要一张可查、可撤销、可到期的证书。
//
// 阶段（基础 → 进阶 → 熟练）此前只是 Audience 自由文本；这里给出枚举，
// 认证按阶段发放。
// ============================================================================

// LearningStage is the proficiency stage a path targets (§15.5 基础→进阶→熟练).
type LearningStage string

const (
	// LearningStageBasic 基础：入职与常识。
	LearningStageBasic LearningStage = "basic"
	// LearningStageAdvanced 进阶：独立处理常规工作。
	LearningStageAdvanced LearningStage = "advanced"
	// LearningStageProficient 熟练：可处理异常并带新人。
	LearningStageProficient LearningStage = "proficient"
)

// Valid reports whether the stage is known.
func (s LearningStage) Valid() bool {
	switch s {
	case LearningStageBasic, LearningStageAdvanced, LearningStageProficient:
		return true
	default:
		return false
	}
}

// ParseLearningStage normalises a stored value; unknown values fall back to
// basic so legacy rows keep working.
func ParseLearningStage(raw string) LearningStage {
	stage := LearningStage(raw)
	if stage.Valid() {
		return stage
	}
	return LearningStageBasic
}

// Rank orders the stages so "at least advanced" comparisons are possible.
func (s LearningStage) Rank() int {
	switch s {
	case LearningStageBasic:
		return 1
	case LearningStageAdvanced:
		return 2
	case LearningStageProficient:
		return 3
	default:
		return 0
	}
}

// LearningCertificateStatus is the certificate lifecycle.
type LearningCertificateStatus string

const (
	// LearningCertificateActive is currently valid.
	LearningCertificateActive LearningCertificateStatus = "active"
	// LearningCertificateRevoked was withdrawn (e.g. policy change, misconduct).
	LearningCertificateRevoked LearningCertificateStatus = "revoked"
	// LearningCertificateExpired is past its validity window.
	LearningCertificateExpired LearningCertificateStatus = "expired"
)

// LearningCertificate is one earned credential (§15.5 完成后获得认证).
type LearningCertificate struct {
	ID       string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID uint64 `json:"tenant_id" gorm:"not null"`
	// UserID holding the certificate.
	UserID string `json:"user_id" gorm:"type:varchar(36);not null"`
	// PathID the certificate was earned on.
	PathID string `json:"path_id" gorm:"type:varchar(36);not null"`
	// Stage the certificate attests.
	Stage string `json:"stage" gorm:"type:varchar(16);not null;default:'basic'"`
	// Serial is the tenant-unique certificate number.
	Serial string `json:"serial" gorm:"type:varchar(48);not null"`
	// Status is the lifecycle state.
	Status    string     `json:"status" gorm:"type:varchar(16);not null;default:'active'"`
	IssuedAt  time.Time  `json:"issued_at"`
	ExpiresAt *time.Time `json:"expires_at"`
	RevokedAt *time.Time `json:"revoked_at"`
	// RevokedReason explains a withdrawal.
	RevokedReason string `json:"revoked_reason" gorm:"type:varchar(255);not null;default:''"`
}

// TableName pins the GORM table name.
func (LearningCertificate) TableName() string { return "learning_certificates" }

// BeforeCreate assigns an ID and serial before persistence.
func (c *LearningCertificate) BeforeCreate(_ *gorm.DB) error {
	if c.ID == "" {
		c.ID = uuid.NewString()
	}
	if c.Serial == "" {
		c.Serial = newLearningCertificateSerial()
	}
	return nil
}

// newLearningCertificateSerial mints a readable, tenant-unique serial.
func newLearningCertificateSerial() string {
	return "CERT-" + uuid.NewString()[:8]
}

// EffectiveStatus folds the expiry window into the stored status so an expired
// certificate never counts as valid at read time (§7.1 读取时解析，不静默改写).
func (c *LearningCertificate) EffectiveStatus(now time.Time) LearningCertificateStatus {
	if c == nil {
		return LearningCertificateExpired
	}
	status := LearningCertificateStatus(c.Status)
	if status == LearningCertificateRevoked {
		return status
	}
	if c.ExpiresAt != nil && now.After(*c.ExpiresAt) {
		return LearningCertificateExpired
	}
	if status != LearningCertificateActive {
		return status
	}
	return LearningCertificateActive
}

// Active reports whether the certificate is usable at the given instant.
func (c *LearningCertificate) Active(now time.Time) bool {
	return c.EffectiveStatus(now) == LearningCertificateActive
}

// LearningCertification is the result of a completion attempt.
type LearningCertification struct {
	// Certificate is the issued credential, nil when not yet eligible.
	Certificate *LearningCertificate `json:"certificate,omitempty"`
	// Eligible reports whether the learner completed every step.
	Eligible bool `json:"eligible"`
	// CompletedSteps / TotalSteps describe the progress that was evaluated.
	CompletedSteps int `json:"completed_steps"`
	TotalSteps     int `json:"total_steps"`
	// Reason explains why a certificate was not issued.
	Reason string `json:"reason,omitempty"`
}

// DescribeLearningStage renders a stage for audit details.
func DescribeLearningStage(stage LearningStage) string {
	return fmt.Sprintf("stage=%s", stage)
}
