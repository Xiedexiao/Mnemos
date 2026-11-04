package types

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ============================================================================
// 知识审核流（KnowAxis V3.0 §6 的 v1 子集）。
//
// 三张表各管一件事：
//   - approval_flows   KB 级审批流配置（single|dual + risk_rules）。
//   - approval_tasks   一次审核提交 = 一个任务（基线快照 + 表决记录）。
//   - approval_receipts append-only 审批回执，prev_hash/row_hash 锚成哈希链。
//
// 状态机极简：pending → approved | rejected | canceled。多人表决（dual /
// high 风险）通过 decided_history 攒票，达到法定人数才落终态并出回执。
// ============================================================================

// ApprovalFlowMode 审批流模式：single 单人终审；dual 双人（四眼）终审。
type ApprovalFlowMode string

const (
	// ApprovalModeSingle 单人审批，1 票通过即发布。
	ApprovalModeSingle ApprovalFlowMode = "single"
	// ApprovalModeDual 双人审批，2 个不同审批人各投一票通过才发布。
	ApprovalModeDual ApprovalFlowMode = "dual"
)

// ApprovalTaskStatus 任务状态。pending → approved/rejected/canceled 单向流转，
// 终态不可再变（重新提交会创建新任务）。
type ApprovalTaskStatus string

const (
	// ApprovalTaskStatusPending 待审批。
	ApprovalTaskStatusPending ApprovalTaskStatus = "pending"
	// ApprovalTaskStatusApproved 已通过（已出回执并发布）。
	ApprovalTaskStatusApproved ApprovalTaskStatus = "approved"
	// ApprovalTaskStatusRejected 已驳回（lifecycle 退回草稿）。
	ApprovalTaskStatusRejected ApprovalTaskStatus = "rejected"
	// ApprovalTaskStatusCanceled 已撤销（提交后状态机推进失败等系统路径）。
	ApprovalTaskStatusCanceled ApprovalTaskStatus = "canceled"
)

// ApprovalDecision 是审批人单票的表决取向（记录在 decided_history）。
type ApprovalDecision string

const (
	ApprovalDecisionApproved ApprovalDecision = "approved"
	ApprovalDecisionRejected ApprovalDecision = "rejected"
)

// Valid 报告该表决取向是否合法（请求体白名单）。
func (d ApprovalDecision) Valid() bool {
	switch d {
	case ApprovalDecisionApproved, ApprovalDecisionRejected:
		return true
	default:
		return false
	}
}

// ApprovalRiskLevel 风险级（v1 由密级路由：confidential→high、secret→mid、
// 其余 low）。high 风险在 v1 一律按 dual 双人表决处理。
type ApprovalRiskLevel string

const (
	ApprovalRiskLow  ApprovalRiskLevel = "low"
	ApprovalRiskMid  ApprovalRiskLevel = "mid"
	ApprovalRiskHigh ApprovalRiskLevel = "high"
)

// ApprovalReasonCode 驳回原因码（approve 时允许为空）。
type ApprovalReasonCode string

const (
	// ApprovalReasonFactError 事实错误。
	ApprovalReasonFactError ApprovalReasonCode = "fact_error"
	// ApprovalReasonInsufficientEvidence 依据不足。
	ApprovalReasonInsufficientEvidence ApprovalReasonCode = "insufficient_evidence"
	// ApprovalReasonFormat 格式问题。
	ApprovalReasonFormat ApprovalReasonCode = "format"
	// ApprovalReasonConflict 与权威知识冲突。
	ApprovalReasonConflict ApprovalReasonCode = "conflict"
	// ApprovalReasonPermission 越权/密级不符。
	ApprovalReasonPermission ApprovalReasonCode = "permission"
	// ApprovalReasonOther 其他。
	ApprovalReasonOther ApprovalReasonCode = "other"
)

// Valid 报告原因码是否在白名单内；空串合法（通过时无需原因）。
func (c ApprovalReasonCode) Valid() bool {
	switch c {
	case "", ApprovalReasonFactError, ApprovalReasonInsufficientEvidence, ApprovalReasonFormat,
		ApprovalReasonConflict, ApprovalReasonPermission, ApprovalReasonOther:
		return true
	default:
		return false
	}
}

// 审核流审计动作（action 命名沿用 `<area>.<event>` 点分风格）。定义在本文件
// 而非 audit_log.go，让审核流垂直切片自包含。
const (
	// AuditActionReviewSubmitted 提交审核（任务创建 + lifecycle 进入 pending_review）。
	AuditActionReviewSubmitted AuditAction = "review.submitted"
	// AuditActionReviewApproved 一票通过（含未达法定人数的中间票）。
	AuditActionReviewApproved AuditAction = "review.approved"
	// AuditActionReviewRejected 驳回。
	AuditActionReviewRejected AuditAction = "review.rejected"
	// AuditActionReviewReceiptIssued 回执入链（哈希链追加成功）。
	AuditActionReviewReceiptIssued AuditAction = "review.receipt_issued"
)

// ApprovalFlow 是 KB 级审批流配置。每个 (tenant, kb) 至多一行；无配置时
// 服务层按 single 兜底。enabled=false 视同无配置。
type ApprovalFlow struct {
	ID       string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID uint64 `json:"tenant_id" gorm:"not null;uniqueIndex:uq_approval_flows_tenant_kb,priority:1"`
	KBID     string `json:"kb_id" gorm:"type:varchar(36);not null;uniqueIndex:uq_approval_flows_tenant_kb,priority:2"`
	// Mode 审批模式：single | dual。
	Mode string `json:"mode" gorm:"type:varchar(16);not null;default:'single'"`
	// Version 流程定义版本。任务提交时冻结，改配置不影响在途任务（§6.3
	// "在途流程按旧版本走完"）。
	Version int `json:"version" gorm:"not null;default:1"`
	// Rules 条件路由规则（ApprovalRoutingRules）。
	Rules JSON `json:"rules" gorm:"type:json;not null;default:'{}'"`
	// SLAHours 审批时限（小时）；0 用服务层默认值。
	SLAHours int `json:"sla_hours" gorm:"not null;default:72"`
	// ReminderHours 首次催办提前量（小时，相对 SLA 到期）。
	ReminderHours int `json:"reminder_hours" gorm:"not null;default:24"`
	// EscalateRole 超时升级到的角色（缺省 admin）。
	EscalateRole string `json:"escalate_role" gorm:"type:varchar(32);not null;default:'admin'"`
	// RiskRules 兼容列：v1 只读 {"sla_hours": n}；新写入走 SLAHours/Rules。
	RiskRules JSON `json:"risk_rules,omitempty" gorm:"type:json;not null;default:'{}'"`
	Enabled   bool `json:"enabled" gorm:"not null;default:true"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName 固定 GORM 表名。
func (ApprovalFlow) TableName() string { return "approval_flows" }

// ApprovalRoutingRules 是条件路由配置（§6.3 条件路由 + §6.4 风险分级）。
//
// 规则按"取最严"求值：命中任意一条高风险约束就走高风险链。作者不能把内容
// 标成低风险来降低审批级别，因为风险分由服务层按内容与影响算出后与这里的
// 约束取并集。
type ApprovalRoutingRules struct {
	// MinRiskLevel 强制的最低风险级（low|medium|high）。
	MinRiskLevel string `json:"min_risk_level,omitempty"`
	// ForceDualAbove 达到该风险级时强制双人（即使 mode=single）。
	ForceDualAbove string `json:"force_dual_above,omitempty"`
	// ClassificationRouting 密级 → 审批模式覆盖（single|dual）。
	ClassificationRouting map[string]string `json:"classification_routing,omitempty"`
	// RequireDistinctReviewerRoles 双审必须来自不同角色（§6.3 四眼）。
	RequireDistinctReviewerRoles bool `json:"require_distinct_reviewer_roles,omitempty"`
	// EscalationHours 未响应多久后升级；0 用 SLAHours。
	EscalationHours int `json:"escalation_hours,omitempty"`
}

// ParseApprovalRoutingRules decodes the rules column, treating bad JSON as an
// empty rule set: a configuration typo must not block every submission.
func ParseApprovalRoutingRules(raw JSON) ApprovalRoutingRules {
	var rules ApprovalRoutingRules
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &rules)
	}
	return rules
}

// ApprovalSLAEventKind names a recorded SLA action (§6.3).
type ApprovalSLAEventKind string

const (
	// ApprovalSLAEventReminded 已催办。
	ApprovalSLAEventReminded ApprovalSLAEventKind = "reminded"
	// ApprovalSLAEventEscalated 已升级。
	ApprovalSLAEventEscalated ApprovalSLAEventKind = "escalated"
)

// ApprovalSLAEvent records one reminder or escalation so auditing can answer
// "why was this task escalated".
type ApprovalSLAEvent struct {
	ID        string    `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID  uint64    `json:"tenant_id" gorm:"not null"`
	TaskID    string    `json:"task_id" gorm:"type:varchar(36);not null"`
	Kind      string    `json:"kind" gorm:"type:varchar(16);not null"`
	Detail    JSON      `json:"detail" gorm:"type:json;not null;default:'{}'"`
	CreatedAt time.Time `json:"created_at"`
}

// TableName 固定 GORM 表名。
func (ApprovalSLAEvent) TableName() string { return "approval_sla_events" }

// BeforeCreate assigns an ID before persistence.
func (e *ApprovalSLAEvent) BeforeCreate(_ *gorm.DB) error {
	if e.ID == "" {
		e.ID = uuid.NewString()
	}
	return nil
}

// ApprovalBaseline 是提交审核时刻的内容指纹快照，冻结进任务与回执。字段
// 顺序即 JSON 序列化顺序（回执哈希按固定顺序复算），新增字段只能追加。
type ApprovalBaseline struct {
	// ContentHash 与任务/回执列上的 content_hash 同值。
	ContentHash string `json:"content_hash"`
	// ChunkCount 提交时的 text/faq chunk 数。
	ChunkCount int `json:"chunk_count"`
	// Title 标题快照。
	Title string `json:"title"`
	// CustomMetadataSummary 用户元数据的稳定摘要（Knowledge.CustomMetadataText）。
	CustomMetadataSummary string `json:"custom_metadata_summary"`
	// Classification 提交时密级。
	Classification string `json:"classification"`
	// ParseStatus 提交时解析状态（记录性字段，不作为门槛）。
	ParseStatus string `json:"parse_status"`
}

// ApprovalDecisionRecord 是 decided_history 里的一票：谁、投了什么、何时。
type ApprovalDecisionRecord struct {
	By       string    `json:"by"`
	Decision string    `json:"decision"`
	At       time.Time `json:"at"`
}

// ApprovalTask 一次审核提交。baseline/content_hash 冻结提交时刻的内容，
// 决策时可对照"内容是否中途变过"。
type ApprovalTask struct {
	ID          string `json:"id" gorm:"type:varchar(36);primaryKey" extensions:"x-agent=approval_task_id"`
	TenantID    uint64 `json:"tenant_id" gorm:"not null;index:idx_approval_tasks_tenant_status,priority:1"`
	KnowledgeID string `json:"knowledge_id" gorm:"type:varchar(36);not null;index:idx_approval_tasks_knowledge" extensions:"x-agent=knowledge_id"`
	KBID        string `json:"kb_id" gorm:"type:varchar(36);not null" extensions:"x-agent=knowledge_base_id"`
	// SubjectType/SubjectID identify what is under review (§6.2 十四类审核对象).
	// Knowledge publishing uses type "knowledge" and mirrors KnowledgeID, so
	// existing queries and historical rows keep working unchanged.
	SubjectType string `json:"subject_type" gorm:"type:varchar(32);not null;default:'knowledge'" extensions:"x-agent"`
	SubjectID   string `json:"subject_id" gorm:"type:varchar(64);not null;default:''" extensions:"x-agent"`
	// Title is a human label for the review workbench (the knowledge title, the
	// official answer's question, the export request's purpose, …).
	Title string `json:"title" gorm:"type:varchar(255);not null;default:''" extensions:"x-agent"`
	// Mode 冻结提交时刻的流配置（single|dual），避免 submit→decide 之间
	// flow 配置漂移导致表决口径不一致。
	Mode     string `json:"mode" gorm:"type:varchar(16);not null;default:'single'" extensions:"x-agent"`
	Baseline JSON   `json:"baseline" gorm:"type:json;not null;default:'{}'"`
	// ContentHash 提交时刻的内容指纹（sha256_hex）。
	ContentHash string `json:"content_hash" gorm:"type:varchar(128);not null"`
	RiskLevel   string `json:"risk_level" gorm:"type:varchar(16);not null" extensions:"x-agent"`
	Status      string `json:"status" gorm:"type:varchar(16);not null;default:'pending';index:idx_approval_tasks_tenant_status,priority:2" extensions:"x-agent"`
	AssigneeIDs JSON   `json:"assignee_ids" gorm:"type:json;not null;default:'[]'"`
	RequestedBy string `json:"requested_by" gorm:"type:varchar(36);not null"`
	DecidedBy   string `json:"decided_by" gorm:"type:varchar(36);not null;default:''"`
	// DecidedHistory 全部表决记录 [{by,decision,at}]；dual 模式据此攒票。
	DecidedHistory JSON       `json:"decided_history" gorm:"type:json;not null;default:'[]'"`
	DecisionAt     *time.Time `json:"decision_at"`
	ReasonCode     string     `json:"reason_code" gorm:"type:varchar(32);not null;default:''"`
	Comment        string     `json:"comment" gorm:"type:text;not null;default:''"`
	SLADue         *time.Time `json:"sla_due" extensions:"x-agent"`
	ReceiptID      string     `json:"receipt_id" gorm:"type:varchar(64);not null;default:''"`
	CreatedAt      time.Time  `json:"created_at" extensions:"x-agent"`
	UpdatedAt      time.Time  `json:"updated_at" extensions:"x-agent"`
}

// TableName 固定 GORM 表名。
func (ApprovalTask) TableName() string { return "approval_tasks" }

// ApprovalReceipt 是 append-only 的审批回执。行与行之间以 prev_hash →
// row_hash 首尾相接成租户内单链，链首为 GENESIS。硬规则：只允许 Append，
// 业务代码不得出现对这张表的 UPDATE/DELETE。
type ApprovalReceipt struct {
	ReceiptID   string `json:"receipt_id" gorm:"type:varchar(64);primaryKey"`
	TenantID    uint64 `json:"tenant_id" gorm:"not null"`
	TaskID      string `json:"task_id" gorm:"type:varchar(36);not null"`
	KnowledgeID string `json:"knowledge_id" gorm:"type:varchar(36);not null"`
	// ContentHash 与任务上的 content_hash 同值（发布内容的指纹）。
	ContentHash      string `json:"content_hash" gorm:"type:varchar(128);not null"`
	BaselineSnapshot JSON   `json:"baseline_snapshot" gorm:"type:json;not null;default:'{}'"`
	// ApproverID 完成法定人数的那一票的审批人；完整表决记录看任务的
	// decided_history（回执按单一责任人建模）。
	ApproverID string `json:"approver_id" gorm:"type:varchar(36);not null"`
	Decision   string `json:"decision" gorm:"type:varchar(16);not null"`
	ReasonCode string `json:"reason_code" gorm:"type:varchar(32);not null;default:''"`
	// RowHash = sha256_hex(prev_hash + "\n" + 规范化JSON字段串)。
	RowHash   string    `json:"row_hash" gorm:"type:varchar(64);not null"`
	PrevHash  string    `json:"prev_hash" gorm:"type:varchar(64);not null;default:'GENESIS'"`
	CreatedAt time.Time `json:"created_at"`
}

// TableName 固定 GORM 表名。
func (ApprovalReceipt) TableName() string { return "approval_receipts" }

// ApprovalReceiptGenesisHash 链首占位：租户第一笔回执的 prev_hash。
const ApprovalReceiptGenesisHash = "GENESIS"

// approvalReceiptHashPayload 是回执哈希链的规范化载荷。字段顺序即哈希输入
// 顺序 —— 修改字段、顺序或序列化格式都会让既有链无法复算，属于破坏性变更。
// BaselineSnapshot 通过 ApprovalBaseline 结构体重序列化，规避 Postgres
// jsonb 存储不保留键序/空白导致的复算漂移。
type approvalReceiptHashPayload struct {
	ReceiptID        string           `json:"receipt_id"`
	TenantID         uint64           `json:"tenant_id"`
	TaskID           string           `json:"task_id"`
	KnowledgeID      string           `json:"knowledge_id"`
	ContentHash      string           `json:"content_hash"`
	BaselineSnapshot ApprovalBaseline `json:"baseline_snapshot"`
	ApproverID       string           `json:"approver_id"`
	Decision         string           `json:"decision"`
	ReasonCode       string           `json:"reason_code"`
	CreatedAt        string           `json:"created_at"`
}

// HashPayload 返回该回执的规范化哈希载荷（供 Append 与链校验共用）。
func (r *ApprovalReceipt) HashPayload() approvalReceiptHashPayload {
	var baseline ApprovalBaseline
	if r != nil && len(r.BaselineSnapshot) > 0 {
		// 反序列化失败按零值处理：坏快照仍可入链，只是指纹取空基线。
		_ = json.Unmarshal(r.BaselineSnapshot, &baseline)
	}
	payload := approvalReceiptHashPayload{CreatedAt: time.RFC3339Nano}
	if r == nil {
		return payload
	}
	payload.ReceiptID = r.ReceiptID
	payload.TenantID = r.TenantID
	payload.TaskID = r.TaskID
	payload.KnowledgeID = r.KnowledgeID
	payload.ContentHash = r.ContentHash
	payload.BaselineSnapshot = baseline
	payload.ApproverID = r.ApproverID
	payload.Decision = r.Decision
	payload.ReasonCode = r.ReasonCode
	payload.CreatedAt = r.CreatedAt.UTC().Format(time.RFC3339Nano)
	return payload
}

// ComputeApprovalReceiptRowHash 计算 row_hash = sha256_hex(prevHash + "\n" +
// 规范化JSON字段串)。仓储 Append 在链尾读取事务内调用它。
func ComputeApprovalReceiptRowHash(prevHash string, receipt *ApprovalReceipt) string {
	payload, err := json.Marshal(receipt.HashPayload())
	if err != nil {
		// 结构体序列化不会失败；兜底保证哈希总可计算。
		payload = []byte("{}")
	}
	sum := sha256.Sum256([]byte(prevHash + "\n" + string(payload)))
	return hex.EncodeToString(sum[:])
}
