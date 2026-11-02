package types

import (
	"fmt"
	"time"
)

// ============================================================================
// 十四类治理审批对象（KnowAxis V3.0 §6.2）。
//
// 发布一篇知识只是十四个需要审批的动作之一。权限提权、批量导出、删除下架、
// 合并拆分、模型上线、Agent 工具授权等动作的风险不低于发布错误文档，必须
// 进入同一套审批与留痕体系——否则"审批"只在内容这一条路上成立，其它动作
// 依然是某个人点一下就生效。
//
// 平台此前让这些动作各走各的路径：外部分享直通 approved、导出只有状态位、
// 删除没有审批。这里用一个泛化的主体类型把它们接到既有的 approval_tasks +
// approval_receipts + approval_task_nodes 上。
// ============================================================================

// ApprovalSubjectType names the governance object under review.
type ApprovalSubjectType string

const (
	// ApprovalSubjectKnowledge 新知识入库 / 知识更新。
	ApprovalSubjectKnowledge ApprovalSubjectType = "knowledge"
	// ApprovalSubjectOfficialAnswer 官方答案新建/变更（§13.7）。
	ApprovalSubjectOfficialAnswer ApprovalSubjectType = "official_answer"
	// ApprovalSubjectRule 规则/政策新建或变更。
	ApprovalSubjectRule ApprovalSubjectType = "rule"
	// ApprovalSubjectOntology SchemaProposal（§8.4）。
	ApprovalSubjectOntology ApprovalSubjectType = "ontology_schema"
	// ApprovalSubjectPermission 权限授予/提权（§2.3 任何提权必须审批）。
	ApprovalSubjectPermission ApprovalSubjectType = "permission_grant"
	// ApprovalSubjectShare 外部分享 / 访客邀请（§2.5）。
	ApprovalSubjectShare ApprovalSubjectType = "external_share"
	// ApprovalSubjectExport 批量导出（§2.2 export 阈值审批）。
	ApprovalSubjectExport ApprovalSubjectType = "bulk_export"
	// ApprovalSubjectDeletion 删除 / 下架（§7.8）。
	ApprovalSubjectDeletion ApprovalSubjectType = "deletion"
	// ApprovalSubjectMerge 知识合并 / 拆分。
	ApprovalSubjectMerge ApprovalSubjectType = "knowledge_merge"
	// ApprovalSubjectTaxonomy 术语 / 分类 / 模板变更（§4.7）。
	ApprovalSubjectTaxonomy ApprovalSubjectType = "taxonomy_change"
	// ApprovalSubjectModelRelease 模型 / Prompt 上线（§16.5）。
	ApprovalSubjectModelRelease ApprovalSubjectType = "model_release"
	// ApprovalSubjectAgentTool Agent 工具授权（§13.9）。
	ApprovalSubjectAgentTool ApprovalSubjectType = "agent_tool_grant"
	// ApprovalSubjectCandidate AI 沉淀候选（§10.4）。
	ApprovalSubjectCandidate ApprovalSubjectType = "knowledge_candidate"
	// ApprovalSubjectChangeRequest 变更单（§7.2）。
	ApprovalSubjectChangeRequest ApprovalSubjectType = "change_request"
)

// Valid reports whether the subject type is known.
func (s ApprovalSubjectType) Valid() bool {
	switch s {
	case ApprovalSubjectKnowledge, ApprovalSubjectOfficialAnswer, ApprovalSubjectRule,
		ApprovalSubjectOntology, ApprovalSubjectPermission, ApprovalSubjectShare,
		ApprovalSubjectExport, ApprovalSubjectDeletion, ApprovalSubjectMerge,
		ApprovalSubjectTaxonomy, ApprovalSubjectModelRelease, ApprovalSubjectAgentTool,
		ApprovalSubjectCandidate, ApprovalSubjectChangeRequest:
		return true
	default:
		return false
	}
}

// IsKnowledgeSubject reports whether the subject is a knowledge document, which
// is the only type the legacy lifecycle publish path applies to.
func (s ApprovalSubjectType) IsKnowledgeSubject() bool {
	return s == ApprovalSubjectKnowledge
}

// governanceSubjectRisk is the default risk for each subject type (§6.2's 风险
// column). Permission escalation, external sharing, bulk export, deletion and
// model/agent changes are high: they either widen exposure or remove content.
var governanceSubjectRisk = map[ApprovalSubjectType]ApprovalRiskLevel{
	ApprovalSubjectKnowledge:      ApprovalRiskMid,
	ApprovalSubjectOfficialAnswer: ApprovalRiskHigh,
	ApprovalSubjectRule:           ApprovalRiskHigh,
	ApprovalSubjectOntology:       ApprovalRiskMid,
	ApprovalSubjectPermission:     ApprovalRiskHigh,
	ApprovalSubjectShare:          ApprovalRiskHigh,
	ApprovalSubjectExport:         ApprovalRiskHigh,
	ApprovalSubjectDeletion:       ApprovalRiskHigh,
	ApprovalSubjectMerge:          ApprovalRiskMid,
	ApprovalSubjectTaxonomy:       ApprovalRiskMid,
	ApprovalSubjectModelRelease:   ApprovalRiskHigh,
	ApprovalSubjectAgentTool:      ApprovalRiskHigh,
	ApprovalSubjectCandidate:      ApprovalRiskMid,
	ApprovalSubjectChangeRequest:  ApprovalRiskMid,
}

// DefaultRisk returns the subject type's baseline risk.
func (s ApprovalSubjectType) DefaultRisk() ApprovalRiskLevel {
	if risk, ok := governanceSubjectRisk[s]; ok {
		return risk
	}
	return ApprovalRiskMid
}

// RequiresReceiptBeforeEffect reports whether the action may only take effect
// after an approved receipt exists.
//
// Every high-risk subject does: that is the whole point of routing them through
// the approval flow. Low/mid subjects may proceed and be reviewed afterwards
// (§6.8 emergency channels rely on that), but the high ones must be blocked.
func (s ApprovalSubjectType) RequiresReceiptBeforeEffect() bool {
	return s.DefaultRisk() == ApprovalRiskHigh
}

// GovernanceApprovalRequest is the body used to submit any governance subject
// for approval.
type GovernanceApprovalRequest struct {
	// SubjectType / SubjectID identify the object.
	SubjectType ApprovalSubjectType `json:"subject_type"`
	SubjectID   string              `json:"subject_id"`
	// KBID scopes the approval-flow lookup (which chain applies).
	KBID string `json:"kb_id"`
	// Title is the workbench label.
	Title string `json:"title"`
	// Reason explains why the action is needed (required for escalation-type
	// subjects; recorded on the audit trail either way).
	Reason string `json:"reason"`
	// RiskLevel overrides the subject default when the caller can justify it.
	// The routing rules may only raise it, never lower it.
	RiskLevel ApprovalRiskLevel `json:"risk_level"`
	// Snapshot is the frozen evidence/context shown to the approver: the
	// difference for an edit, the recipient list for a share, the file list for
	// an export. Stored as the task baseline so "what was approved" stays
	// answerable.
	Snapshot JSON `json:"snapshot"`
}

// Normalize fills defaults from the subject type and enforces the risk floor.
//
// The floor is the point: §6.4 says 作者不能把自己写的内容标成低风险来降低审批
// 级别. So a caller may raise the risk of a subject but never lower it below the
// type's baseline — otherwise "I labeled this export as low risk" would be a
// one-line bypass of the entire approval gate.
func (r *GovernanceApprovalRequest) Normalize() {
	if r == nil {
		return
	}
	if floor := r.SubjectType.DefaultRisk(); riskRank(r.RiskLevel) < riskRank(floor) {
		r.RiskLevel = floor
	}
	if len(r.Snapshot) == 0 {
		r.Snapshot = JSON("{}")
	}
}

// riskRank orders risk levels so floors can be compared. It is duplicated from
// the service package deliberately: severity ordering is a property of the type,
// and a caller that bypasses the service must not be able to lower it either.
func riskRank(level ApprovalRiskLevel) int {
	switch level {
	case ApprovalRiskLow:
		return 1
	case ApprovalRiskMid:
		return 2
	case ApprovalRiskHigh:
		return 3
	default:
		return 0
	}
}

// Validate checks the request shape.
func (r *GovernanceApprovalRequest) Validate() error {
	if r == nil {
		return fmt.Errorf("approval request is required")
	}
	if !r.SubjectType.Valid() {
		return fmt.Errorf("unknown subject type %q", r.SubjectType)
	}
	if r.SubjectID == "" {
		return fmt.Errorf("subject_id is required")
	}
	return nil
}

// GovernanceApprovalDecision is the outcome of a governance approval lookup:
// whether the action may proceed right now.
type GovernanceApprovalDecision struct {
	// SubjectType / SubjectID the decision is about.
	SubjectType ApprovalSubjectType `json:"subject_type"`
	SubjectID   string              `json:"subject_id"`
	// RequiresApproval reports whether this action is gated at all.
	RequiresApproval bool `json:"requires_approval"`
	// Approved reports whether an approved receipt exists.
	Approved bool `json:"approved"`
	// ReceiptID is the authorising receipt, when approved.
	ReceiptID string `json:"receipt_id,omitempty"`
	// TaskID is the approval task that produced the receipt.
	TaskID string `json:"task_id,omitempty"`
	// Reason explains a denial or a waiver.
	Reason string `json:"reason,omitempty"`
}

// Allows reports whether the action may take effect.
func (d *GovernanceApprovalDecision) Allows() bool {
	return d != nil && (!d.RequiresApproval || d.Approved)
}

// GovernanceApprovalStatus is the lifecycle summary surfaced to the UI.
type GovernanceApprovalStatus struct {
	SubjectType ApprovalSubjectType `json:"subject_type"`
	SubjectID   string              `json:"subject_id"`
	TaskID      string              `json:"task_id,omitempty"`
	Status      string              `json:"status,omitempty"`
	RiskLevel   string              `json:"risk_level,omitempty"`
	ReceiptID   string              `json:"receipt_id,omitempty"`
	RequestedBy string              `json:"requested_by,omitempty"`
	CreatedAt   *time.Time          `json:"created_at,omitempty"`
	DecidedAt   *time.Time          `json:"decided_at,omitempty"`
}
