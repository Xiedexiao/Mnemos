package types

import (
	"fmt"
	"strings"
	"time"
)

// ============================================================================
// 变更单 + 影响任务（KnowAxis V3.0 §7.2/§7.6 的 v1 子集）。
//
// 三张表各管一件事：
//   - change_requests 一次批量知识变更 = 一张单（编号 CR-YYYYMMDD-序号，
//     自带 draft→…→closed/rolled_back 状态机）。
//   - change_items    变更作用到的知识条目清单，Publish 时逐项回写结果。
//   - impact_tasks    影响分析产出的波及面待办，关闭门禁要求全部闭环。
//
// 变更单不改内容本身：Publish 通过 KnowledgeLifecycleService.Transition 推进
// 每个条目的生命周期（v1 一律 mark_stale），保证每次状态变化都可审计。
// ============================================================================

// ChangeRequestStatus 变更单状态机。单向推进，closed/rolled_back 为终态。
type ChangeRequestStatus string

const (
	// ChangeRequestStatusDraft 草稿：可增删条目，未做影响分析。
	ChangeRequestStatusDraft ChangeRequestStatus = "draft"
	// ChangeRequestStatusAnalyzing 已做影响分析：仍可增删条目（增删后可重新分析）。
	ChangeRequestStatusAnalyzing ChangeRequestStatus = "analyzing"
	// ChangeRequestStatusInApproval 审批中（v1 为占位态，真实审批对接 A2 审核流）。
	ChangeRequestStatusInApproval ChangeRequestStatus = "in_approval"
	// ChangeRequestStatusScheduled 已排期：等待 effective_at 到点后发布。
	ChangeRequestStatusScheduled ChangeRequestStatus = "scheduled"
	// ChangeRequestStatusPublishing 发布中：逐项调 lifecycle，失败项可重试。
	ChangeRequestStatusPublishing ChangeRequestStatus = "publishing"
	// ChangeRequestStatusVerifying 验证中：所有条目已应用，等待影响任务闭环。
	ChangeRequestStatusVerifying ChangeRequestStatus = "verifying"
	// ChangeRequestStatusClosed 已关闭：门禁（全部任务 done/waived 且条目非 pending）通过。
	ChangeRequestStatusClosed ChangeRequestStatus = "closed"
	// ChangeRequestStatusRolledBack 已回滚：发布后主动撤销（终态，可审计）。
	ChangeRequestStatusRolledBack ChangeRequestStatus = "rolled_back"
)

// changeRequestTransitions 是变更单状态机的合法迁移表。发布环节的"留在
// publishing 重试"不是迁移（状态不变），由 Publish 方法自行放行。
var changeRequestTransitions = map[ChangeRequestStatus][]ChangeRequestStatus{
	ChangeRequestStatusDraft:      {ChangeRequestStatusAnalyzing},
	ChangeRequestStatusAnalyzing:  {ChangeRequestStatusInApproval},
	ChangeRequestStatusInApproval: {ChangeRequestStatusScheduled},
	ChangeRequestStatusScheduled:  {ChangeRequestStatusPublishing},
	ChangeRequestStatusPublishing: {ChangeRequestStatusVerifying},
	ChangeRequestStatusVerifying:  {ChangeRequestStatusClosed, ChangeRequestStatusRolledBack},
	ChangeRequestStatusClosed:     {},
	ChangeRequestStatusRolledBack: {},
}

// CanTransitionTo 报告从当前状态迁入 next 是否合法。
func (s ChangeRequestStatus) CanTransitionTo(next ChangeRequestStatus) bool {
	if s == next {
		return false
	}
	for _, allowed := range changeRequestTransitions[s] {
		if allowed == next {
			return true
		}
	}
	return false
}

// Valid 报告该状态是否已知。
func (s ChangeRequestStatus) Valid() bool {
	switch s {
	case ChangeRequestStatusDraft, ChangeRequestStatusAnalyzing, ChangeRequestStatusInApproval,
		ChangeRequestStatusScheduled, ChangeRequestStatusPublishing, ChangeRequestStatusVerifying,
		ChangeRequestStatusClosed, ChangeRequestStatusRolledBack:
		return true
	default:
		return false
	}
}

// String 渲染状态用于日志。
func (s ChangeRequestStatus) String() string { return string(s) }

// ChangeRequestReason 变更原因（治理口径的业务分类）。
type ChangeRequestReason string

const (
	// ChangeReasonRegulation 法规/合规驱动。
	ChangeReasonRegulation ChangeRequestReason = "regulation"
	// ChangeReasonProcess 流程调整驱动。
	ChangeReasonProcess ChangeRequestReason = "process"
	// ChangeReasonProduct 产品变更驱动。
	ChangeReasonProduct ChangeRequestReason = "product"
	// ChangeReasonCorrection 纠错驱动。
	ChangeReasonCorrection ChangeRequestReason = "correction"
	// ChangeReasonCustomer 客户/外部诉求驱动。
	ChangeReasonCustomer ChangeRequestReason = "customer"
	// ChangeReasonOther 其他。
	ChangeReasonOther ChangeRequestReason = "other"
)

// Valid 报告原因是否在白名单内。
func (r ChangeRequestReason) Valid() bool {
	switch r {
	case ChangeReasonRegulation, ChangeReasonProcess, ChangeReasonProduct,
		ChangeReasonCorrection, ChangeReasonCustomer, ChangeReasonOther:
		return true
	default:
		return false
	}
}

// ChangeType 变更类型。它决定 Publish 时每个条目走哪种 lifecycle 动作：
// v1 replacement/delist 语义上就是"旧内容先失效"，走 mark_stale；其余类型
// 在 v1 没有真实的内容改写管线，同样先标 stale 提示"受变更单波及"。
type ChangeType string

const (
	// ChangeTypeTextFix 文字勘误。
	ChangeTypeTextFix ChangeType = "text_fix"
	// ChangeTypeContentRevision 内容改版。
	ChangeTypeContentRevision ChangeType = "content_revision"
	// ChangeTypeParamChange 参数/口径变更。
	ChangeTypeParamChange ChangeType = "param_change"
	// ChangeTypeReplacement 新版本替代旧版本。
	ChangeTypeReplacement ChangeType = "replacement"
	// ChangeTypeDelist 下架。
	ChangeTypeDelist ChangeType = "delist"
	// ChangeTypeRestructure 结构重组（拆分/合并）。
	ChangeTypeRestructure ChangeType = "restructure"
)

// Valid 报告变更类型是否在白名单内。
func (t ChangeType) Valid() bool {
	switch t {
	case ChangeTypeTextFix, ChangeTypeContentRevision, ChangeTypeParamChange,
		ChangeTypeReplacement, ChangeTypeDelist, ChangeTypeRestructure:
		return true
	default:
		return false
	}
}

// ChangeRiskLevel 变单风险级（决定影响任务的 priority 路由）。取值口径与
// 审批流的 ApprovalRiskLevel 一致（low/mid/high），独立定义避免跨模块耦合。
type ChangeRiskLevel string

const (
	ChangeRiskLow  ChangeRiskLevel = "low"
	ChangeRiskMid  ChangeRiskLevel = "mid"
	ChangeRiskHigh ChangeRiskLevel = "high"
)

// Valid 报告风险级是否在白名单内。
func (r ChangeRiskLevel) Valid() bool {
	switch r {
	case ChangeRiskLow, ChangeRiskMid, ChangeRiskHigh:
		return true
	default:
		return false
	}
}

// ChangeItemStatus 变更条目的应用状态。Publish 成功→applied、失败→failed
// （CR 留在 publishing 可重试）；skipped 预留给人工跳过路径。
type ChangeItemStatus string

const (
	// ChangeItemStatusPending 待应用。
	ChangeItemStatusPending ChangeItemStatus = "pending"
	// ChangeItemStatusApplied 已应用（lifecycle 推进成功）。
	ChangeItemStatusApplied ChangeItemStatus = "applied"
	// ChangeItemStatusFailed 应用失败（可随下次 Publish 重试）。
	ChangeItemStatusFailed ChangeItemStatus = "failed"
	// ChangeItemStatusSkipped 已跳过（人工决定，不阻塞关闭门禁）。
	ChangeItemStatusSkipped ChangeItemStatus = "skipped"
)

// ImpactTaskTargetType 影响任务的目标类型。v1 只产出 knowledge 类；
// 其余为后续版本预留（培训材料、外部文档、物料、系统配置的波及登记）。
type ImpactTaskTargetType string

const (
	// ImpactTargetKnowledge 知识条目（v1 唯一产出的目标类型）。
	ImpactTargetKnowledge ImpactTaskTargetType = "knowledge"
	// ImpactTargetTraining 培训课件。
	ImpactTargetTraining ImpactTaskTargetType = "training"
	// ImpactTargetExternalDoc 对外文档。
	ImpactTargetExternalDoc ImpactTaskTargetType = "external_doc"
	// ImpactTargetMaterial 物料。
	ImpactTargetMaterial ImpactTaskTargetType = "material"
	// ImpactTargetSystemConfig 系统配置。
	ImpactTargetSystemConfig ImpactTaskTargetType = "system_config"
)

// Valid 报告目标类型是否在白名单内。
func (t ImpactTaskTargetType) Valid() bool {
	switch t {
	case ImpactTargetKnowledge, ImpactTargetTraining, ImpactTargetExternalDoc,
		ImpactTargetMaterial, ImpactTargetSystemConfig:
		return true
	default:
		return false
	}
}

// ImpactTaskPriority 影响任务优先级。由变更单 risk_level 路由：
// high→must_change、mid→suggested、low→fyi。
type ImpactTaskPriority string

const (
	// ImpactPriorityMustChange 必须处理（不闭环无法关闭变更单）。
	ImpactPriorityMustChange ImpactTaskPriority = "must_change"
	// ImpactPrioritySuggested 建议处理。
	ImpactPrioritySuggested ImpactTaskPriority = "suggested"
	// ImpactPriorityFYI 仅周知。
	ImpactPriorityFYI ImpactTaskPriority = "fyi"
)

// Valid 报告优先级是否在白名单内。
func (p ImpactTaskPriority) Valid() bool {
	switch p {
	case ImpactPriorityMustChange, ImpactPrioritySuggested, ImpactPriorityFYI:
		return true
	default:
		return false
	}
}

// ImpactTaskStatus 影响任务状态。open → done | waived；关闭门禁要求所有
// 任务都不是 open。
type ImpactTaskStatus string

const (
	// ImpactTaskStatusOpen 待处理。
	ImpactTaskStatusOpen ImpactTaskStatus = "open"
	// ImpactTaskStatusDone 已完成。
	ImpactTaskStatusDone ImpactTaskStatus = "done"
	// ImpactTaskStatusWaived 已豁免（明确决定不做，留痕）。
	ImpactTaskStatusWaived ImpactTaskStatus = "waived"
)

// Valid 报告任务状态是否在白名单内。
func (s ImpactTaskStatus) Valid() bool {
	switch s {
	case ImpactTaskStatusOpen, ImpactTaskStatusDone, ImpactTaskStatusWaived:
		return true
	default:
		return false
	}
}

// ============================================================================
// 审计动作（action 命名沿用 `<area>.<event>` 点分风格，定义在本文件让变更
// 单垂直切片自包含，与 review.go 的做法一致）。
// ============================================================================

const (
	// AuditActionCRCreated 变更单创建。
	AuditActionCRCreated AuditAction = "cr.created"
	// AuditActionCRItemAdded 追加变更条目。
	AuditActionCRItemAdded AuditAction = "cr.item_added"
	// AuditActionCRItemRemoved 移除变更条目。
	AuditActionCRItemRemoved AuditAction = "cr.item_removed"
	// AuditActionCRAnalyzed 影响分析完成（含生成的任务数）。
	AuditActionCRAnalyzed AuditAction = "cr.analyzed"
	// AuditActionCRSubmitted 提交审批。
	AuditActionCRSubmitted AuditAction = "cr.submitted"
	// AuditActionCRScheduled 排期（设定生效时间）。
	AuditActionCRScheduled AuditAction = "cr.scheduled"
	// AuditActionCRPublishStarted 进入发布（开始逐项应用）。
	AuditActionCRPublishStarted AuditAction = "cr.publish_started"
	// AuditActionCRPublishFinished 发布轮次结束（全部 applied 或存在 failed）。
	AuditActionCRPublishFinished AuditAction = "cr.publish_finished"
	// AuditActionCRClosed 关闭（门禁通过）。
	AuditActionCRClosed AuditAction = "cr.closed"
	// AuditActionCRRolledBack 回滚。
	AuditActionCRRolledBack AuditAction = "cr.rolled_back"
	// AuditActionCRImpactTaskUpdated 影响任务状态/备注更新。
	AuditActionCRImpactTaskUpdated AuditAction = "cr.impact_task_updated"
	// AuditActionCRVerified 验证结论已填写（关闭门禁条件②）。
	AuditActionCRVerified AuditAction = "cr.verified"
	// AuditActionCRNotified 影响通知已送达（关闭门禁条件④）。
	AuditActionCRNotified AuditAction = "cr.notified"
)

// ============================================================================
// 结构体
// ============================================================================

// ChangeRequestBaseline 是影响分析时刻冻结的条目清单快照（change_requests
// .baseline JSONB）。关闭与回溯时用它对照"这张单到底覆盖了哪些条目"。
type ChangeRequestBaseline struct {
	// ItemCount 分析时刻的条目数。
	ItemCount int `json:"item_count"`
	// KnowledgeIDs 分析时刻的条目知识 ID 清单。
	KnowledgeIDs []string `json:"knowledge_ids"`
	// Note 创建人补充的变更说明。
	Note string `json:"note"`
}

// ChangeCloseCondition 是 close_conditions JSONB 里的一项结构化关闭前置
// 条件。v1 只做展示与留痕：真正的硬门禁由服务层 Close 的"任务全闭环 +
// 条目非 pending"校验兜底。
type ChangeCloseCondition struct {
	// Key 机器可读的条件键（如 "verify_sample_qa"）。
	Key string `json:"key"`
	// Description 人类可读的描述。
	Description string `json:"description"`
}

// ChangeRequest 变更单主表。Items / ImpactTasks 是聚合字段（gorm:"-"），
// 仅由服务层 List/Get 填充，方便一次返回完整单据。
type ChangeRequest struct {
	ID string `json:"id" gorm:"type:varchar(36);primaryKey"`
	// TenantID 租户隔离。
	TenantID uint64 `json:"tenant_id" gorm:"not null;uniqueIndex:uq_change_requests_tenant_number,priority:1"`
	// CRNumber 单号，格式 CR-YYYYMMDD-序号（服务层生成，租户内唯一）。
	CRNumber string `json:"cr_number" gorm:"type:varchar(32);not null;uniqueIndex:uq_change_requests_tenant_number,priority:2"`
	// Title 变更标题。
	Title string `json:"title" gorm:"type:varchar(255);not null"`
	// Reason 变更原因（regulation/process/product/correction/customer/other）。
	Reason string `json:"reason" gorm:"type:varchar(32);not null"`
	// ChangeType 变更类型（text_fix/content_revision/param_change/replacement/delist/restructure）。
	ChangeType string `json:"change_type" gorm:"type:varchar(24);not null"`
	// Status 变更单状态机（draft…rolled_back）。
	Status string `json:"status" gorm:"type:varchar(16);not null;default:'draft'"`
	// RiskLevel 风险级（low/mid/high），路由影响任务 priority。
	RiskLevel string `json:"risk_level" gorm:"type:varchar(16);not null;default:'low'"`
	// Baseline 分析时刻的条目清单快照。
	Baseline JSON `json:"baseline" gorm:"type:json;not null;default:'{}'"`
	// EffectiveAt 生效时间（Schedule 设定；发布后条目按它对外生效）。
	EffectiveAt *time.Time `json:"effective_at"`
	// AnnounceAt 公告时间（v1 记录性字段）。
	AnnounceAt *time.Time `json:"announce_at"`
	// ExpiresAt 失效时间（v1 记录性字段）。
	ExpiresAt *time.Time `json:"expires_at"`
	// RollbackPlan 回滚方案文本。
	RollbackPlan string `json:"rollback_plan" gorm:"type:text;not null;default:''"`
	// CloseConditions 结构化关闭前置条件（v1 只读展示）。
	CloseConditions JSON `json:"close_conditions" gorm:"type:json;not null;default:'[]'"`
	// ApprovalTaskID 提交审批时创建的治理审批任务（§6.2 change_request）。
	ApprovalTaskID string `json:"approval_task_id" gorm:"type:varchar(36);not null;default:''"`
	// ApprovalReceiptID 排期时绑定的审批回执：发布与审计都以它为凭据
	// （§7.2：审批通过才能进入排期）。
	ApprovalReceiptID string `json:"approval_receipt_id" gorm:"type:varchar(64);not null;default:''"`
	// VerificationResult 验证结论（发布后由验证人填写，关闭门禁条件②）。
	VerificationResult string `json:"verification_result" gorm:"type:text;not null;default:''"`
	// NotifiedAt 影响通知送达时刻（关闭门禁条件④）。
	NotifiedAt *time.Time `json:"notified_at"`
	// CreatedBy 创建人（审批四眼校验等其它治理动作依赖它）。
	CreatedBy string `json:"created_by" gorm:"type:varchar(36);not null"`
	// ClosedAt 关闭时刻。
	ClosedAt *time.Time `json:"closed_at"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// Items 变更条目（聚合，服务层填充）。
	Items []*ChangeItem `json:"items,omitempty" gorm:"-"`
	// ImpactTasks 影响任务（聚合，服务层填充）。
	ImpactTasks []*ImpactTask `json:"impact_tasks,omitempty" gorm:"-"`
}

// TableName 固定 GORM 表名。
func (ChangeRequest) TableName() string { return "change_requests" }

// ChangeItem 变更单作用的单条知识。item_status 由 Publish 逐项回写。
type ChangeItem struct {
	ID string `json:"id" gorm:"type:varchar(36);primaryKey"`
	// CRID 所属变更单。
	CRID string `json:"cr_id" gorm:"type:varchar(36);not null;uniqueIndex:uq_change_items_cr_knowledge,priority:1"`
	// KnowledgeID 目标知识。
	KnowledgeID string `json:"knowledge_id" gorm:"type:varchar(36);not null;uniqueIndex:uq_change_items_cr_knowledge,priority:2"`
	// ItemStatus 应用状态（pending/applied/failed/skipped）。
	ItemStatus string `json:"item_status" gorm:"type:varchar(16);not null;default:'pending'"`
	// ReplacedByID 替代类变更里接替本条目的新知识（§7.2 replacement）。
	// 为空时发布降级为 mark_stale，而不是让整批条目 failed。
	ReplacedByID string `json:"replaced_by_id" gorm:"type:varchar(36);not null;default:''"`

	CreatedAt time.Time `json:"created_at"`
}

// TableName 固定 GORM 表名。
func (ChangeItem) TableName() string { return "change_items" }

// ImpactTask 影响分析产出的波及面待办。列默认值 'normal' 是建表兜底；
// 服务层总是显式写入 must_change/suggested/fyi 之一。
type ImpactTask struct {
	ID string `json:"id" gorm:"type:varchar(36);primaryKey"`
	// TenantID 租户隔离。
	TenantID uint64 `json:"tenant_id" gorm:"not null;index:idx_impact_tasks_tenant_status,priority:1"`
	// CRID 所属变更单。
	CRID string `json:"cr_id" gorm:"type:varchar(36);not null;index:idx_impact_tasks_cr"`
	// TargetType 波及对象类型（v1 只产 knowledge）。
	TargetType string `json:"target_type" gorm:"type:varchar(16);not null"`
	// TargetID 波及对象 ID（knowledge 类即知识 ID；其余类型为后续版本预留）。
	TargetID string `json:"target_id" gorm:"type:varchar(36);not null;default:'';index:idx_impact_tasks_target"`
	// Priority 处理优先级（must_change/suggested/fyi）。
	Priority string `json:"priority" gorm:"type:varchar(16);not null;default:'normal'"`
	// Status 任务状态（open/done/waived）；关闭门禁要求全非 open。
	Status string `json:"status" gorm:"type:varchar(16);not null;default:'open';index:idx_impact_tasks_tenant_status,priority:2"`
	// AssigneeID 指派人（v1 空串 = 未指派，走治理面认领）。
	AssigneeID string `json:"assignee_id" gorm:"type:varchar(36);not null;default:''"`
	// Note 说明/完成备注。
	Note string `json:"note" gorm:"type:text;not null;default:''"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName 固定 GORM 表名。
func (ImpactTask) TableName() string { return "impact_tasks" }

// ChangeRequestCreateInput 是 CreateCR 的入参（身份字段由服务层补齐）。
type ChangeRequestCreateInput struct {
	Title           string
	Reason          ChangeRequestReason
	ChangeType      ChangeType
	RiskLevel       ChangeRiskLevel
	RollbackPlan    string
	Note            string
	EffectiveAt     *time.Time
	AnnounceAt      *time.Time
	ExpiresAt       *time.Time
	CloseConditions []ChangeCloseCondition
	CreatedBy       string
}

// ChangeRequestListFilter 是 List 的过滤参数；零值 = 不过滤不分页硬上限。
type ChangeRequestListFilter struct {
	// Status 可选，按变更单状态过滤。
	Status string
	// Limit / Offset 分页参数；Limit<=0 时仓储侧套默认值。
	Limit  int
	Offset int
}

// ChangeCloseBlockedError 是 Close 门禁未满足时返回的业务错误（handler 映射
// 400 并把未闭环项透出）。用类型而非哨兵字符串，让调用方 errors.As 后拿到
// 具体清单。
type ChangeCloseBlockedError struct {
	// OpenTasks 仍未 done/waived 的影响任务 ID。
	OpenTasks []string
	// PendingItems 仍是 pending 的变更条目知识 ID。
	PendingItems []string
	// Verified 验证结论是否已填写（§7.2 关闭条件②）。
	Verified bool
	// Notified 影响通知是否已送达（§7.2 关闭条件④）。
	Notified bool
	// OldVersionGone 旧版本是否已失效（§7.2 关闭条件③）。
	OldVersionGone bool
}

// Error 实现 error 接口；消息里带清单，便于审计与前端提示。
func (e *ChangeCloseBlockedError) Error() string {
	var b strings.Builder
	b.WriteString("change request close gate not met")
	if len(e.PendingItems) > 0 {
		fmt.Fprintf(&b, "; pending item(s): %s", strings.Join(e.PendingItems, ","))
	}
	if !e.Verified {
		b.WriteString("; verification result is missing")
	}
	if !e.OldVersionGone {
		b.WriteString("; the previous version has not been retired")
	}
	if !e.Notified {
		b.WriteString("; affected owners have not been notified")
	}
	if len(e.OpenTasks) > 0 {
		fmt.Fprintf(&b, "; open impact task(s): %s", strings.Join(e.OpenTasks, ","))
	}
	return b.String()
}
