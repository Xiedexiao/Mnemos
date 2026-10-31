package types

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ============================================================================
// 导出管控（KnowAxis V3.0 §2.2 export 动作 / §2.4 标识 的 v1 子集）。
//
// export_requests 是一次导出申请（knowledge / evidence_pack / audit 三种
// scope）的申请-审批-执行状态机：
//
//	pending ──Decide(approve)──▶ approved ──Fulfill──▶ fulfilled
//	pending ──Decide(reject)───▶ rejected                （终态）
//
// 审批策略 v1（服务层实现）：secret/confidential 密级或 audit scope 必须
// 人工审批（Admin 且 ≠ 申请人，四眼）；public/internal 且非审计导出在
// Create 内直接置 approved（自动放行）。watermark_text 由服务端生成
// （"用户名-时间-随机4位"），Fulfill 返回给前端渲染导出物水印；文档级
// 水印渲染与离线 DRM 不在本期范围。
// ============================================================================

// ExportRequestStatus 导出申请状态。rejected 与 fulfilled 是终态；v1 不做
// "驳回后重新提交"，重新申请即新建一行（保留完整审批痕迹）。
type ExportRequestStatus string

const (
	// ExportRequestStatusPending 待审批（需人工审批的申请落在此状态）。
	ExportRequestStatusPending ExportRequestStatus = "pending"
	// ExportRequestStatusApproved 已批准（可 Fulfill）。
	ExportRequestStatusApproved ExportRequestStatus = "approved"
	// ExportRequestStatusRejected 已驳回（终态）。
	ExportRequestStatusRejected ExportRequestStatus = "rejected"
	// ExportRequestStatusFulfilled 已执行导出（终态）。
	ExportRequestStatusFulfilled ExportRequestStatus = "fulfilled"
)

// Valid 报告状态是否已知。
func (s ExportRequestStatus) Valid() bool {
	switch s {
	case ExportRequestStatusPending, ExportRequestStatusApproved,
		ExportRequestStatusRejected, ExportRequestStatusFulfilled:
		return true
	default:
		return false
	}
}

// String 渲染状态用于日志。
func (s ExportRequestStatus) String() string { return string(s) }

// ExportScope 导出对象类别（V3.0 §2.2 export 动作的 v1 子集）。
type ExportScope string

const (
	// ExportScopeKnowledge 导出知识库内容（文档/FAQ/知识条目）。
	ExportScopeKnowledge ExportScope = "knowledge"
	// ExportScopeEvidencePack 导出证据包（问答链路存档）。
	ExportScopeEvidencePack ExportScope = "evidence_pack"
	// ExportScopeAudit 导出审计日志（敏感：任何租户的审计导出必须人工审批）。
	ExportScopeAudit ExportScope = "audit"
)

// Valid 报告 scope 是否已知。
func (s ExportScope) Valid() bool {
	switch s {
	case ExportScopeKnowledge, ExportScopeEvidencePack, ExportScopeAudit:
		return true
	default:
		return false
	}
}

// String 渲染 scope 用于日志。
func (s ExportScope) String() string { return string(s) }

// ExportDecision 审批裁决动作（Decide 的入参）。
type ExportDecision string

const (
	// ExportDecisionApprove 批准。
	ExportDecisionApprove ExportDecision = "approve"
	// ExportDecisionReject 驳回。
	ExportDecisionReject ExportDecision = "reject"
)

// Valid 报告裁决是否已知。
func (d ExportDecision) Valid() bool {
	switch d {
	case ExportDecisionApprove, ExportDecisionReject:
		return true
	default:
		return false
	}
}

// String 渲染裁决用于日志。
func (d ExportDecision) String() string { return string(d) }

// ExportRequest 是一次导出申请的行。scope 与 max_classification 决定审批
// 策略；watermark_text 在 Create 时生成（或由调用方显式指定），Fulfill 时
// 返回给前端渲染。
type ExportRequest struct {
	// ID of the request.
	ID string `json:"id" gorm:"type:varchar(36);primaryKey"`
	// TenantID owning the request.
	TenantID uint64 `json:"tenant_id" gorm:"not null"`
	// RequestedBy is the applicant (四眼原则的"第一只眼"：审批人不得是申请人)。
	RequestedBy string `json:"requested_by" gorm:"type:varchar(36);not null"`
	// Scope: knowledge | evidence_pack | audit.
	Scope string `json:"scope" gorm:"type:varchar(16);not null"`
	// ScopeFilter is the export range filter (kb_ids / 条数 / 时间窗), a
	// JSON object. Marshalled with the local service helper, never inline
	// structs.
	ScopeFilter JSON `json:"scope_filter" gorm:"type:jsonb;default:'{}'"`
	// MaxClassification 本次导出允许的最高密级（public|internal|secret|
	// confidential）；secret/confidential 触发人工审批。
	MaxClassification string `json:"max_classification" gorm:"type:varchar(16);not null;default:'internal'"`
	// Status: pending | approved | rejected | fulfilled.
	Status string `json:"status" gorm:"type:varchar(16);not null;default:'pending'"`
	// ApproverID 审批人（四眼原则的"第二只眼"；自动 approve 时为空串）。
	ApproverID string `json:"approver_id" gorm:"type:varchar(36);default:''"`
	// DecisionNote 审批备注（自动 approve 时标注 auto-approved）。
	DecisionNote string `json:"decision_note" gorm:"type:text;default:''"`
	// WatermarkText 服务端生成的水印文本，Fulfill 时返回前端渲染。
	WatermarkText string `json:"watermark_text" gorm:"type:varchar(255);default:''"`
	// CreatedAt of the request.
	CreatedAt time.Time `json:"created_at"`
	// DecidedAt 审批时刻（approve/reject 共用）。
	DecidedAt *time.Time `json:"decided_at"`
	// FulfilledAt 导出执行时刻。
	FulfilledAt *time.Time `json:"fulfilled_at"`
}

// TableName pins the GORM table name.
func (ExportRequest) TableName() string { return "export_requests" }

// BeforeCreate assigns an ID before persistence.
func (r *ExportRequest) BeforeCreate(_ *gorm.DB) error {
	if r.ID == "" {
		r.ID = uuid.NewString()
	}
	return nil
}

// IsStatus reports whether the request is in the given status.
func (r *ExportRequest) IsStatus(s ExportRequestStatus) bool {
	return ExportRequestStatus(r.Status) == s
}

// RequiresApproval 报告该申请按 v1 审批策略是否必须人工审批：
// scope=audit 或 max_classification ∈ {secret, confidential} 必须审批；
// public/internal 且非审计导出可自动放行。密级判定复用
// KnowledgeClassification.Rank()，空值按 internal 处理（与既有口径一致）。
func (r *ExportRequest) RequiresApproval() bool {
	if ExportScope(r.Scope) == ExportScopeAudit {
		return true
	}
	classification := ParseKnowledgeClassification(r.MaxClassification)
	return classification.Rank() >= KnowledgeClassificationSecret.Rank()
}

// ExportDecisionInput carries one Decide call's parameters. ApproverRole 由
// handler 从 Caller 带入，服务层复核 Admin（路由层 Admin 守卫之外的纵深
// 防御），四眼校验（≠申请人）在服务层强制。
type ExportDecisionInput struct {
	// Decision: approve | reject.
	Decision ExportDecision
	// ApproverID is the authenticated decision maker.
	ApproverID string
	// ApproverRole is the decision maker's tenant role (service-side Admin
	// re-check).
	ApproverRole TenantRole
	// Note 审批备注（reject 时建议说明理由）。
	Note string
}
