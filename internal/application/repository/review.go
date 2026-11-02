package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 审批流三张表的仓储哨兵错误，service 层据此映射业务错误（404 等）。
var (
	ErrApprovalFlowNotFound    = errors.New("approval flow not found")
	ErrApprovalTaskNotFound    = errors.New("approval task not found")
	ErrApprovalReceiptNotFound = errors.New("approval receipt not found")
)

// approvalListMaxLimit 是任务/回执列表的硬上限：审核待办与审计回放都是
// 低频人读接口，200 条足够一屏，防止无界扫描。
const approvalListMaxLimit = 200

// approvalFlowRepository 读取 KB 级审批流配置（v1 只读）。
type approvalFlowRepository struct {
	db *gorm.DB
}

// NewApprovalFlowRepository creates a new approval flow repository.
func NewApprovalFlowRepository(db *gorm.DB) interfaces.ApprovalFlowRepository {
	return &approvalFlowRepository{db: db}
}

// GetByKB 返回 (tenant, kb) 的审批流配置；无配置返回 ErrApprovalFlowNotFound。
func (r *approvalFlowRepository) GetByKB(ctx context.Context, tenantID uint64, kbID string) (*types.ApprovalFlow, error) {
	var flow types.ApprovalFlow
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND kb_id = ?", tenantID, kbID).
		First(&flow).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrApprovalFlowNotFound
		}
		return nil, err
	}
	return &flow, nil
}

// Upsert creates or updates a KB's approval flow and advances its version.
//
// The version bump is the point of §6.3's "在途流程按旧版本走完": tasks freeze
// the version at submission, so editing the flow only affects new submissions.
func (r *approvalFlowRepository) Upsert(
	ctx context.Context, flow *types.ApprovalFlow,
) (*types.ApprovalFlow, error) {
	if flow == nil || flow.TenantID == 0 || flow.KBID == "" {
		return nil, fmt.Errorf("approval flow: tenant_id and kb_id are required")
	}
	if flow.ID == "" {
		flow.ID = uuid.NewString()
	}
	if flow.Rules == nil {
		flow.Rules = types.JSON("{}")
	}
	if flow.RiskRules == nil {
		flow.RiskRules = types.JSON("{}")
	}
	err := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "tenant_id"}, {Name: "kb_id"}},
			DoUpdates: clause.Assignments(map[string]interface{}{
				"mode":           flow.Mode,
				"rules":          flow.Rules,
				"sla_hours":      flow.SLAHours,
				"reminder_hours": flow.ReminderHours,
				"escalate_role":  flow.EscalateRole,
				"enabled":        flow.Enabled,
				// 配置变更必须让版本前进；在途任务靠冻结的版本号隔离。
				"version":    gorm.Expr("approval_flows.version + 1"),
				"updated_at": gorm.Expr("NOW()"),
			}),
		}).
		Create(flow).Error
	if err != nil {
		return nil, err
	}
	return r.GetByKB(ctx, flow.TenantID, flow.KBID)
}

// ListByTenant lists a tenant's approval flow configuration.
func (r *approvalFlowRepository) ListByTenant(
	ctx context.Context, tenantID uint64, limit int,
) ([]*types.ApprovalFlow, error) {
	if limit <= 0 || limit > approvalListMaxLimit {
		limit = approvalListMaxLimit
	}
	var flows []*types.ApprovalFlow
	err := r.db.WithContext(ctx).
		Where("tenant_id = ?", tenantID).
		Order("updated_at DESC").
		Limit(limit).
		Find(&flows).Error
	return flows, err
}

// Delete removes a KB's approval flow so it falls back to single approval.
func (r *approvalFlowRepository) Delete(ctx context.Context, tenantID uint64, kbID string) error {
	return r.db.WithContext(ctx).
		Where("tenant_id = ? AND kb_id = ?", tenantID, kbID).
		Delete(&types.ApprovalFlow{}).Error
}

// approvalTaskRepository implements the approval task repository interface.
type approvalTaskRepository struct {
	db *gorm.DB
}

// NewApprovalTaskRepository creates a new approval task repository.
func NewApprovalTaskRepository(db *gorm.DB) interfaces.ApprovalTaskRepository {
	return &approvalTaskRepository{db: db}
}

// Create creates an approval task.
func (r *approvalTaskRepository) Create(ctx context.Context, task *types.ApprovalTask) error {
	return r.db.WithContext(ctx).Create(task).Error
}

// GetByID gets an approval task by tenant and id.
func (r *approvalTaskRepository) GetByID(ctx context.Context, tenantID uint64, taskID string) (*types.ApprovalTask, error) {
	var task types.ApprovalTask
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, taskID).
		First(&task).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrApprovalTaskNotFound
		}
		return nil, err
	}
	return &task, nil
}

// HasActiveByKnowledgeID reports whether the knowledge still has a pending
// task, so a second submit for the same content is rejected as a duplicate.
func (r *approvalTaskRepository) HasActiveByKnowledgeID(
	ctx context.Context, tenantID uint64, knowledgeID string,
) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&types.ApprovalTask{}).
		Where("tenant_id = ? AND knowledge_id = ? AND status = ?",
			tenantID, knowledgeID, string(types.ApprovalTaskStatusPending)).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// ListPending lists this tenant's pending tasks oldest-first ( fairest queue
// order for the review inbox).
func (r *approvalTaskRepository) ListPending(
	ctx context.Context, tenantID uint64, limit int,
) ([]*types.ApprovalTask, error) {
	if limit <= 0 || limit > approvalListMaxLimit {
		limit = approvalListMaxLimit
	}
	var tasks []*types.ApprovalTask
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND status = ?", tenantID, string(types.ApprovalTaskStatusPending)).
		Order("created_at ASC").
		Limit(limit).
		Find(&tasks).Error
	if err != nil {
		return nil, err
	}
	return tasks, nil
}

// ListPendingBySLA lists pending tasks whose SLA due time has entered the
// reminder window (or passed), oldest deadline first so the most overdue task
// is escalated first (§6.3).
func (r *approvalTaskRepository) ListPendingBySLA(
	ctx context.Context, tenantID uint64, before time.Time, limit int,
) ([]*types.ApprovalTask, error) {
	if limit <= 0 || limit > approvalListMaxLimit {
		limit = approvalListMaxLimit
	}
	var tasks []*types.ApprovalTask
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND status = ? AND sla_due IS NOT NULL AND sla_due <= ?",
			tenantID, string(types.ApprovalTaskStatusPending), before).
		Order("sla_due ASC").
		Limit(limit).
		Find(&tasks).Error
	return tasks, err
}

// ListPendingTenantIDs returns the tenants that still have pending tasks, so the
// sweep can run per tenant without scanning every tenant first.
func (r *approvalTaskRepository) ListPendingTenantIDs(ctx context.Context) ([]uint64, error) {
	var ids []uint64
	err := r.db.WithContext(ctx).
		Model(&types.ApprovalTask{}).
		Where("status = ?", string(types.ApprovalTaskStatusPending)).
		Distinct().
		Pluck("tenant_id", &ids).Error
	return ids, err
}

// Update saves the whole task row (caller loads, mutates, saves back).
func (r *approvalTaskRepository) Update(ctx context.Context, task *types.ApprovalTask) error {
	return r.db.WithContext(ctx).Save(task).Error
}

// approvalReceiptRepository implements the append-only approval receipt store.
//
// 硬规则：本仓储只提供 Append/Get/List。链完整性依赖 Append 内
// "读链尾 → 算 row_hash → 插入"的事务原子性，任何 UPDATE/DELETE 路径
// 都不得加进这个接口。
type approvalReceiptRepository struct {
	db *gorm.DB
}

// NewApprovalReceiptRepository creates a new approval receipt repository.
func NewApprovalReceiptRepository(db *gorm.DB) interfaces.ApprovalReceiptRepository {
	return &approvalReceiptRepository{db: db}
}

// approvalReceiptAdvisoryLock 串行化同一租户的链尾读取：两个并发 publish
// 同时读到同一 prev_hash 会在链上制造分叉（同一 prev 挂两个子节点）。
// Postgres 用事务级 advisory lock 兜住；其他方言（单测 sqlite）退化为
// 普通事务，靠单写者假设。
const approvalReceiptAdvisoryLock = "SELECT pg_advisory_xact_lock(hashtext('approval_receipts:' || ?))"

// Append 在单个事务内读取当前租户链尾 row_hash 作为 prev_hash、计算
// row_hash 并插入。receipt.CreatedAt 必须由调用方填好（哈希载荷的一部分）。
func (r *approvalReceiptRepository) Append(ctx context.Context, receipt *types.ApprovalReceipt) error {
	if receipt == nil {
		return errors.New("approval receipt: nil receipt")
	}
	if receipt.TenantID == 0 {
		return errors.New("approval receipt: tenant_id is required")
	}
	if receipt.CreatedAt.IsZero() {
		return errors.New("approval receipt: created_at is required for hash chaining")
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "postgres" {
			if err := tx.Exec(approvalReceiptAdvisoryLock, fmt.Sprintf("%d", receipt.TenantID)).Error; err != nil {
				return err
			}
		}
		var tail types.ApprovalReceipt
		err := tx.Where("tenant_id = ?", receipt.TenantID).
			Order("created_at DESC").
			First(&tail).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			// 本租户第一笔回执：锚在链首。
			receipt.PrevHash = types.ApprovalReceiptGenesisHash
		case err != nil:
			return err
		default:
			receipt.PrevHash = tail.RowHash
		}
		receipt.RowHash = types.ComputeApprovalReceiptRowHash(receipt.PrevHash, receipt)
		return tx.Create(receipt).Error
	})
}

// Get gets one receipt by tenant and receipt id.
func (r *approvalReceiptRepository) Get(
	ctx context.Context, tenantID uint64, receiptID string,
) (*types.ApprovalReceipt, error) {
	var receipt types.ApprovalReceipt
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND receipt_id = ?", tenantID, receiptID).
		First(&receipt).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrApprovalReceiptNotFound
		}
		return nil, err
	}
	return &receipt, nil
}

// List lists receipts newest-first for chain replay (verify prev_hash from
// the tail backwards).
func (r *approvalReceiptRepository) List(
	ctx context.Context, tenantID uint64, limit int,
) ([]*types.ApprovalReceipt, error) {
	if limit <= 0 || limit > approvalListMaxLimit {
		limit = approvalListMaxLimit
	}
	var receipts []*types.ApprovalReceipt
	err := r.db.WithContext(ctx).
		Where("tenant_id = ?", tenantID).
		Order("created_at DESC").
		Limit(limit).
		Find(&receipts).Error
	if err != nil {
		return nil, err
	}
	return receipts, nil
}

// approvalSLAEventRepository records reminders and escalations (§6.3).
type approvalSLAEventRepository struct {
	db *gorm.DB
}

// NewApprovalSLAEventRepository constructs the SLA event repository.
func NewApprovalSLAEventRepository(db *gorm.DB) interfaces.ApprovalSLAEventRepository {
	return &approvalSLAEventRepository{db: db}
}

// Create appends one SLA event.
func (r *approvalSLAEventRepository) Create(
	ctx context.Context, event *types.ApprovalSLAEvent,
) error {
	return r.db.WithContext(ctx).Create(event).Error
}

// HasEvent reports whether a task already got this kind of event. The sweep
// relies on it so a re-run cannot spam the same reminder every interval.
func (r *approvalSLAEventRepository) HasEvent(
	ctx context.Context, tenantID uint64, taskID, kind string,
) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&types.ApprovalSLAEvent{}).
		Where("tenant_id = ? AND task_id = ? AND kind = ?", tenantID, taskID, kind).
		Count(&count).Error
	return count > 0, err
}

// ListByTask returns a task's SLA ledger, newest first.
func (r *approvalSLAEventRepository) ListByTask(
	ctx context.Context, tenantID uint64, taskID string, limit int,
) ([]*types.ApprovalSLAEvent, error) {
	if limit <= 0 || limit > approvalListMaxLimit {
		limit = approvalListMaxLimit
	}
	var events []*types.ApprovalSLAEvent
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND task_id = ?", tenantID, taskID).
		Order("created_at DESC").
		Limit(limit).
		Find(&events).Error
	return events, err
}
