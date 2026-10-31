package repository

import (
	"context"
	"errors"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
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
			if err := tx.Exec(approvalReceiptAdvisoryLock, receipt.TenantID).Error; err != nil {
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
