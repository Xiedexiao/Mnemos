package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// ApprovalFlowRepository 读取 KB 级审批流配置（v1 只读；配置由运营侧/后续
// 管理接口写入）。
type ApprovalFlowRepository interface {
	// GetByKB 返回 (tenant, kb) 的审批流配置；无配置返回
	// repository.ErrApprovalFlowNotFound（服务层按 single 模式兜底）。
	GetByKB(ctx context.Context, tenantID uint64, kbID string) (*types.ApprovalFlow, error)
}

// ApprovalTaskRepository 审批任务的存储原语。
type ApprovalTaskRepository interface {
	Create(ctx context.Context, task *types.ApprovalTask) error
	// GetByID 按租户 + ID 取任务；不存在返回 repository.ErrApprovalTaskNotFound。
	GetByID(ctx context.Context, tenantID uint64, taskID string) (*types.ApprovalTask, error)
	// HasActiveByKnowledgeID 报告该知识是否仍有 pending 任务（提交幂等防重）。
	HasActiveByKnowledgeID(ctx context.Context, tenantID uint64, knowledgeID string) (bool, error)
	// ListPending 按提交时间升序列出本租户全部 pending 任务（v1：Admin+ 待办池）。
	ListPending(ctx context.Context, tenantID uint64, limit int) ([]*types.ApprovalTask, error)
	// Update 全量保存任务行（调用方加载后修改再回写）。
	Update(ctx context.Context, task *types.ApprovalTask) error
}

// ApprovalReceiptRepository 审批回执的存储原语。append-only：接口刻意不提供
// Update/Delete，链完整性由仓储 Append 在单事务内"读链尾 → 算哈希 → 插入"保证。
type ApprovalReceiptRepository interface {
	// Append 在单个事务内读取当前租户链尾 row_hash 作为 prev_hash、计算
	// row_hash 并插入；调用方需先填好 CreatedAt（哈希载荷的一部分）。
	Append(ctx context.Context, receipt *types.ApprovalReceipt) error
	// Get 按租户 + receipt_id 取单条回执；不存在返回
	// repository.ErrApprovalReceiptNotFound。
	Get(ctx context.Context, tenantID uint64, receiptID string) (*types.ApprovalReceipt, error)
	// List 按创建时间倒序返回回执（链回放时从尾向头逐行验证 prev_hash）。
	List(ctx context.Context, tenantID uint64, limit int) ([]*types.ApprovalReceipt, error)
}

// ReviewService 知识审核流服务（V3.0 §6 v1 子集）。除 GetReceiptChain 显式
// 收租户 ID 外，租户一律取自 ctx（types.TenantIDFromContext），缺失返回
// service.ErrReviewTenantMissing。
type ReviewService interface {
	// SubmitReview 提交知识进入审核：冻结内容基线（content hash + chunk 数 +
	// 标题 + custom_metadata 摘要）、按密级路由风险级（confidential→high、
	// secret→mid、其余 low）、建任务、写审计并推进 lifecycle submit_review。
	// 同知识已有 pending 任务时报 service.ErrReviewTaskAlreadyPending。
	SubmitReview(ctx context.Context, knowledgeID string, requesterID string) (*types.ApprovalTask, error)
	// Decide 审批人投一票。四眼校验（审批人不得等于提交人）；驳回立即终态并
	// 推进 lifecycle reject_review；通过票数达到法定人数（dual 模式或 high
	// 风险需 2 个不同审批人）才生成回执（哈希链追加）并推进 lifecycle publish。
	Decide(
		ctx context.Context,
		taskID string,
		approverID string,
		decision types.ApprovalDecision,
		reasonCode types.ApprovalReasonCode,
		comment string,
	) (*types.ApprovalTask, error)
	// ListPendingTasks 列出待办。v1：路由层已要求 Admin+，返回本租户全部
	// pending；assigneeID 预留给按 assignee_ids 过滤的指派语义。
	ListPendingTasks(ctx context.Context, assigneeID string) ([]*types.ApprovalTask, error)
	// GetTask 按任务 ID 取单条（租户隔离）。
	GetTask(ctx context.Context, taskID string) (*types.ApprovalTask, error)
	// GetReceipt 按回执 ID 取单条（租户隔离）。
	GetReceipt(ctx context.Context, receiptID string) (*types.ApprovalReceipt, error)
	// GetReceiptChain 审计回放：按创建时间倒序返回回执链，limit 由服务层钳制。
	GetReceiptChain(ctx context.Context, tenantID uint64, limit int) ([]*types.ApprovalReceipt, error)
}
