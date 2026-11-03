package types

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ============================================================================
// 变更信号（KnowAxis V3.0 §7.3）。
//
// 更新不能只靠"到期提醒"。§7.3 要求持续采集七类信号，每类定义来源、置信度
// 与默认动作。平台此前完全没有信号层：变更单只能由人手工发起，源系统的
// 作废、法规更新、反馈聚集都不会自己变成一张变更单。
//
// 采集端（连接器、反馈服务、冲突检测、调度器）只负责落信号；"要不要建单"
// 由置信度与默认动作决定，人保留最终决定权。
// ============================================================================

// ChangeSignalType is one of the seven §7.3 signal classes.
type ChangeSignalType string

const (
	// ChangeSignalInternalEvent 内部事件：工单关闭、ECO 发布、组织调整、产品停产。
	ChangeSignalInternalEvent ChangeSignalType = "internal_event"
	// ChangeSignalSourceSystem 源系统状态：PLM 图纸作废、制度修订、QMS 异常关闭。
	ChangeSignalSourceSystem ChangeSignalType = "source_system"
	// ChangeSignalExternalSubscription 外部订阅：法规、国标、行业标准更新。
	ChangeSignalExternalSubscription ChangeSignalType = "external_subscription"
	// ChangeSignalDependency 依赖链：被引用/依据/前置的知识发生变更。
	ChangeSignalDependency ChangeSignalType = "dependency"
	// ChangeSignalUserFeedback 用户反馈："这条已过时"、低满意度聚集。
	ChangeSignalUserFeedback ChangeSignalType = "user_feedback"
	// ChangeSignalConflict 冲突检测：两份权威资料结论不一致。
	ChangeSignalConflict ChangeSignalType = "conflict"
	// ChangeSignalConnectorHealth 连接器健康：连续无变化、权限同步失败。
	ChangeSignalConnectorHealth ChangeSignalType = "connector_health"
)

// allChangeSignalTypes is the whitelist in declaration order.
var allChangeSignalTypes = []ChangeSignalType{
	ChangeSignalInternalEvent, ChangeSignalSourceSystem,
	ChangeSignalExternalSubscription, ChangeSignalDependency,
	ChangeSignalUserFeedback, ChangeSignalConflict, ChangeSignalConnectorHealth,
}

// Valid reports whether the signal type is known.
func (t ChangeSignalType) Valid() bool {
	for _, candidate := range allChangeSignalTypes {
		if candidate == t {
			return true
		}
	}
	return false
}

// ChangeSignalAction is what the collector suggests doing with a signal.
type ChangeSignalAction string

const (
	// ChangeSignalActionCreateCR drafts a change request directly (high confidence).
	ChangeSignalActionCreateCR ChangeSignalAction = "create_cr"
	// ChangeSignalActionReviewTask opens a review task (mid confidence).
	ChangeSignalActionReviewTask ChangeSignalAction = "review_task"
	// ChangeSignalActionNotify only informs an owner (low confidence).
	ChangeSignalActionNotify ChangeSignalAction = "notify"
	// ChangeSignalActionInvestigate asks a human to look into it (anomalies).
	ChangeSignalActionInvestigate ChangeSignalAction = "investigate"
)

// Valid reports whether the action is known.
func (a ChangeSignalAction) Valid() bool {
	switch a {
	case ChangeSignalActionCreateCR, ChangeSignalActionReviewTask,
		ChangeSignalActionNotify, ChangeSignalActionInvestigate:
		return true
	default:
		return false
	}
}

// ChangeSignalStatus is the triage state.
type ChangeSignalStatus string

const (
	// ChangeSignalPending needs a decision.
	ChangeSignalPending ChangeSignalStatus = "pending"
	// ChangeSignalConverted became a change request.
	ChangeSignalConverted ChangeSignalStatus = "converted"
	// ChangeSignalDismissed was judged a false positive.
	ChangeSignalDismissed ChangeSignalStatus = "dismissed"
)

// Valid reports whether the status is known.
func (s ChangeSignalStatus) Valid() bool {
	switch s {
	case ChangeSignalPending, ChangeSignalConverted, ChangeSignalDismissed:
		return true
	default:
		return false
	}
}

// ChangeSignal is one collected update signal.
type ChangeSignal struct {
	ID              string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID        uint64 `json:"tenant_id" gorm:"not null;index:idx_change_signals_tenant_status,priority:1"`
	SignalType      string `json:"signal_type" gorm:"type:varchar(32);not null"`
	SourceID        string `json:"source_id" gorm:"type:varchar(128);not null;default:''"`
	KnowledgeID     string `json:"knowledge_id" gorm:"type:varchar(36);not null;default:''"`
	KnowledgeBaseID string `json:"knowledge_base_id" gorm:"type:varchar(36);not null;default:''"`
	Summary         string `json:"summary" gorm:"type:varchar(512);not null;default:''"`
	Confidence      string `json:"confidence" gorm:"type:varchar(16);not null;default:'mid'"`
	DefaultAction   string `json:"default_action" gorm:"type:varchar(24);not null;default:'review_task'"`
	Payload         JSON   `json:"payload" gorm:"type:json;not null;default:'{}'"`
	Status          string `json:"status" gorm:"type:varchar(16);not null;default:'pending';index:idx_change_signals_tenant_status,priority:2"`
	ChangeRequestID string `json:"change_request_id" gorm:"type:varchar(36);not null;default:''"`
	HandledBy       string `json:"handled_by" gorm:"type:varchar(36);not null;default:''"`
	// DedupeKey keeps one pending signal per source+kind (§7.3: repeated
	// connector noise must not flood the queue).
	DedupeKey string    `json:"dedupe_key" gorm:"type:varchar(191);not null;default:''"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName pins the GORM table name.
func (ChangeSignal) TableName() string { return "change_signals" }

// BeforeCreate assigns an ID and a dedupe key before persistence.
func (s *ChangeSignal) BeforeCreate(_ *gorm.DB) error {
	if s.ID == "" {
		s.ID = uuid.NewString()
	}
	if s.DedupeKey == "" {
		s.DedupeKey = ChangeSignalDedupeKey(s.TenantID, s.SignalType, s.SourceID, s.KnowledgeID)
	}
	return nil
}

// ChangeSignalDedupeKey builds the dedupe identity for one pending signal.
//
// 同一租户、同一类信号、同一来源（或同一知识）在待处理期间只保留一条：
// 连接器每轮同步都会报同一次失败，不去重会让队列变成噪声日志。
func ChangeSignalDedupeKey(tenantID uint64, signalType, sourceID, knowledgeID string) string {
	canonical := fmt.Sprintf("%d\n%s\n%s\n%s", tenantID, signalType, sourceID, knowledgeID)
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:24])
}

// ChangeSignalRisk maps a signal type onto the change risk that its draft should
// carry, so a high-stakes signal cannot be triaged as a low-risk edit.
var changeSignalRisk = map[ChangeSignalType]ChangeRiskLevel{
	ChangeSignalInternalEvent:        ChangeRiskMid,
	ChangeSignalSourceSystem:         ChangeRiskMid,
	ChangeSignalExternalSubscription: ChangeRiskHigh,
	ChangeSignalDependency:           ChangeRiskMid,
	ChangeSignalUserFeedback:         ChangeRiskLow,
	ChangeSignalConflict:             ChangeRiskHigh,
	ChangeSignalConnectorHealth:      ChangeRiskLow,
}

// DefaultRiskLevel returns the risk a change request drafted from this signal
// should carry.
func (t ChangeSignalType) DefaultRiskLevel() ChangeRiskLevel {
	if risk, ok := changeSignalRisk[t]; ok {
		return risk
	}
	return ChangeRiskMid
}

// DefaultChangeType returns the change type a drafted request should carry.
//
// 源系统作废与外部法规更新本质是"旧内容失效"，因此默认 delist/替代；其余
// 信号默认按内容修订处理，由人改成更准确的类型。
func (t ChangeSignalType) DefaultChangeType() ChangeType {
	switch t {
	case ChangeSignalSourceSystem:
		return ChangeTypeDelist
	case ChangeSignalExternalSubscription:
		return ChangeTypeContentRevision
	case ChangeSignalDependency:
		return ChangeTypeContentRevision
	default:
		return ChangeTypeTextFix
	}
}

// changeSignalDefaultAction is the §7.3 "默认动作" column.
var changeSignalDefaultAction = map[ChangeSignalType]ChangeSignalAction{
	ChangeSignalInternalEvent:        ChangeSignalActionCreateCR,
	ChangeSignalSourceSystem:         ChangeSignalActionCreateCR,
	ChangeSignalExternalSubscription: ChangeSignalActionCreateCR,
	ChangeSignalDependency:           ChangeSignalActionReviewTask,
	ChangeSignalUserFeedback:         ChangeSignalActionReviewTask,
	ChangeSignalConflict:             ChangeSignalActionCreateCR,
	ChangeSignalConnectorHealth:      ChangeSignalActionInvestigate,
}

// DefaultAction returns the default triage action for a signal type.
func (t ChangeSignalType) DefaultAction() ChangeSignalAction {
	if action, ok := changeSignalDefaultAction[t]; ok {
		return action
	}
	return ChangeSignalActionReviewTask
}

// NormalizeChangeSignal fills the derived fields and validates the shape.
func NormalizeChangeSignal(signal *ChangeSignal) error {
	if signal == nil {
		return fmt.Errorf("change signal is required")
	}
	signalType := ChangeSignalType(signal.SignalType)
	if !signalType.Valid() {
		return fmt.Errorf("unknown signal type %q", signal.SignalType)
	}
	if signal.DefaultAction == "" {
		signal.DefaultAction = string(signalType.DefaultAction())
	}
	if signal.Confidence == "" {
		signal.Confidence = "mid"
	}
	switch signal.Confidence {
	case "high", "mid", "low":
	default:
		return fmt.Errorf("confidence must be high, mid or low")
	}
	if signal.Status == "" {
		signal.Status = string(ChangeSignalPending)
	}
	if len(signal.Payload) == 0 {
		signal.Payload = JSON("{}")
	}
	if runes := []rune(signal.Summary); len(runes) > 512 {
		signal.Summary = string(runes[:512])
	}
	return nil
}
