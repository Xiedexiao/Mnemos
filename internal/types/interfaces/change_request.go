package interfaces

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// ChangeRequestRepository 变更单三张表（change_requests / change_items /
// impact_tasks）的存储原语。租户隔离：主表与影响任务按 tenant_id 过滤；
// change_items 没有租户列，经由所属变更单（CRID）间接隔离。
type ChangeRequestRepository interface {
	// Create 插入变更单主行（含唯一号约束 uq_change_requests_tenant_number）。
	Create(ctx context.Context, cr *types.ChangeRequest) error
	// GetByID 按租户 + ID 取变更单；不存在返回 repository.ErrChangeRequestNotFound。
	GetByID(ctx context.Context, tenantID uint64, id string) (*types.ChangeRequest, error)
	// List 分页列出变更单（created_at 倒序），返回总数。Status 为空不过滤。
	List(ctx context.Context, tenantID uint64, filter types.ChangeRequestListFilter) ([]*types.ChangeRequest, int64, error)
	// Update 全量保存变更单主行（调用方加载后修改再回写）。
	Update(ctx context.Context, cr *types.ChangeRequest) error
	// CountByNumberPrefix 统计本租户当日已用序号（编号生成的基数）。
	CountByNumberPrefix(ctx context.Context, tenantID uint64, prefix string) (int64, error)

	// CreateItem 追加变更条目（(cr_id, knowledge_id) 唯一）。
	CreateItem(ctx context.Context, item *types.ChangeItem) error
	// DeleteItem 删除变更条目；不存在返回 repository.ErrChangeItemNotFound。
	DeleteItem(ctx context.Context, crID string, knowledgeID string) error
	// GetItemByKnowledge 取单条条目（幂等防重/存在性判断）。
	GetItemByKnowledge(ctx context.Context, crID string, knowledgeID string) (*types.ChangeItem, error)
	// ListItemsByCR 批量取条目（聚合展示与发布循环共用）。
	ListItemsByCR(ctx context.Context, crIDs []string) ([]*types.ChangeItem, error)
	// UpdateItem 回写条目应用状态。
	UpdateItem(ctx context.Context, item *types.ChangeItem) error

	// CreateTask 新建影响任务。
	CreateTask(ctx context.Context, task *types.ImpactTask) error
	// GetTask 按租户 + ID 取影响任务；不存在返回 repository.ErrImpactTaskNotFound。
	GetTask(ctx context.Context, tenantID uint64, taskID string) (*types.ImpactTask, error)
	// UpdateTask 全量保存影响任务。
	UpdateTask(ctx context.Context, task *types.ImpactTask) error
	// ListTasksByCR 批量取影响任务（聚合展示与关闭门禁共用）。
	ListTasksByCR(ctx context.Context, crIDs []string) ([]*types.ImpactTask, error)

	// ListTagNeighborKnowledgeIDs 返回同一 KB 内与目标知识共享任意标签的
	// 近邻知识 ID（v1"谁引用了它"影响分析的简化实现，按 knowledge_tag_
	// relations + knowledge_tags 联查；完整的引用图/引文级分析为后续版本）。
	ListTagNeighborKnowledgeIDs(ctx context.Context, tenantID uint64, kbID string, knowledgeID string, limit int) ([]string, error)
}

// ChangeRequestService 变更单 + 影响任务服务（V3.0 §7.2/§7.6 v1）。租户一律
// 取自 ctx（types.TenantIDFromContext），缺失返回 service.ErrCRTenantMissing。
// 发布通过 interfaces.KnowledgeLifecycleService.Transition 推进每个条目的
// 生命周期，保证每次状态变化可审计。
type ChangeRequestService interface {
	// CreateCR 创建变更单（draft）：校验 reason/change_type/risk_level 白名单，
	// 生成 CR-YYYYMMDD-序号 单号。
	CreateCR(ctx context.Context, input types.ChangeRequestCreateInput) (*types.ChangeRequest, error)
	// AddItem 追加变更条目（仅 draft/analyzing 可改；知识须存在且属于本租户）。
	AddItem(ctx context.Context, crID string, knowledgeID string) (*types.ChangeRequest, error)
	// RemoveItem 移除变更条目（仅 draft/analyzing 可改）。
	RemoveItem(ctx context.Context, crID string, knowledgeID string) (*types.ChangeRequest, error)
	// Analyze 影响分析（draft→analyzing）：为每个条目建 1 条 knowledge 类
	// 影响任务（priority 按 cr.risk_level），并对同 KB 共享标签的近邻知识
	// 生成波及任务；冻结条目清单进 baseline。
	Analyze(ctx context.Context, crID string) (*types.ChangeRequest, error)
	// SubmitForApproval 提交审批（analyzing→in_approval；v1 占位，真实审批
	// 对接 A2 审核流为后续版本）。
	SubmitForApproval(ctx context.Context, crID string) (*types.ChangeRequest, error)
	// Schedule 排期（in_approval→scheduled）：设定 effective_at（空则取当前时刻）。
	Schedule(ctx context.Context, crID string, effectiveAt *time.Time) (*types.ChangeRequest, error)
	// Publish 发布（scheduled→publishing→verifying）：对每个 pending/failed
	// 条目调 lifecycle Transition（v1 一律 mark_stale），成功写 applied、失败写
	// failed；存在失败时 CR 留在 publishing 可重试，全部成功才进 verifying。
	Publish(ctx context.Context, crID string) (*types.ChangeRequest, error)
	// Close 关闭（verifying→closed）。门禁：所有影响任务 done/waived 且所有
	// 条目非 pending；不满足返回 *types.ChangeCloseBlockedError（400）。
	Close(ctx context.Context, crID string) (*types.ChangeRequest, error)
	// Rollback 回滚（verifying→rolled_back；终态，留痕审计）。
	Rollback(ctx context.Context, crID string) (*types.ChangeRequest, error)
	// List 分页列出变更单（聚合 items + impact_tasks）。
	List(ctx context.Context, filter types.ChangeRequestListFilter) ([]*types.ChangeRequest, int64, error)
	// Get 取单张变更单（聚合 items + impact_tasks）。
	Get(ctx context.Context, crID string) (*types.ChangeRequest, error)
	// UpdateImpactTask 更新影响任务状态（open/done/waived）与备注；任务必须
	// 属于指定变更单。
	UpdateImpactTask(ctx context.Context, crID string, taskID string, status types.ImpactTaskStatus, note string) (*types.ImpactTask, error)
}
