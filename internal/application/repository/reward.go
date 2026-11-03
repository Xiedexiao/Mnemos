package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// 兑换（§15.4）的业务哨兵错误。
var (
	// ErrRewardItemNotFound 目录项不存在。
	ErrRewardItemNotFound = errors.New("reward item not found")
	// ErrRewardItemDisabled 目录项已下架。
	ErrRewardItemDisabled = errors.New("reward item is disabled")
	// ErrRewardOutOfStock 库存不足。
	ErrRewardOutOfStock = errors.New("reward item is out of stock")
	// ErrRewardInsufficientPoints 余额不足。
	ErrRewardInsufficientPoints = errors.New("insufficient points")
	// ErrRewardRedemptionNotFound 兑换单不存在。
	ErrRewardRedemptionNotFound = errors.New("reward redemption not found")
	// ErrRewardRedemptionNotPending 兑换单已处理，不能再次流转。
	ErrRewardRedemptionNotPending = errors.New("reward redemption is not pending")
)

// rewardRepository implements interfaces.RewardRepository (KnowAxis V3.0
// §15.4 积分兑换).
type rewardRepository struct {
	db *gorm.DB
}

// NewRewardRepository constructs the repository.
func NewRewardRepository(db *gorm.DB) interfaces.RewardRepository {
	return &rewardRepository{db: db}
}

// CreateItem inserts one catalog item.
func (r *rewardRepository) CreateItem(ctx context.Context, item *types.RewardCatalogItem) error {
	return r.db.WithContext(ctx).Create(item).Error
}

// UpdateItem persists the mutable fields.
func (r *rewardRepository) UpdateItem(ctx context.Context, item *types.RewardCatalogItem) error {
	result := r.db.WithContext(ctx).Model(&types.RewardCatalogItem{}).
		Where("tenant_id = ? AND id = ?", item.TenantID, item.ID).
		Updates(map[string]interface{}{
			"name":        item.Name,
			"description": item.Description,
			"cost_points": item.CostPoints,
			"stock":       item.Stock,
			"enabled":     item.Enabled,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrRewardItemNotFound
	}
	return nil
}

// GetItem returns one item in the tenant.
func (r *rewardRepository) GetItem(
	ctx context.Context, tenantID uint64, itemID string,
) (*types.RewardCatalogItem, error) {
	var item types.RewardCatalogItem
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, itemID).
		First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrRewardItemNotFound
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}

// ListItems returns the catalog; enabledOnly hides retired items.
func (r *rewardRepository) ListItems(
	ctx context.Context, tenantID uint64, enabledOnly bool,
) ([]*types.RewardCatalogItem, error) {
	query := r.db.WithContext(ctx).Where("tenant_id = ?", tenantID)
	if enabledOnly {
		query = query.Where("enabled = ?", true)
	}
	var items []*types.RewardCatalogItem
	if err := query.Order("cost_points ASC, created_at ASC").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// Redeem atomically reserves stock, debits the balance, inserts the ledger row
// and writes the pending redemption.
//
// 关键点在原子性：余额判断不能是"先读后写"——两个并发兑换会同时读到
// 100 分、各自扣 60，最终余额出错。这里用 `balance >= cost` 的条件更新
// 让数据库做判定；库存同理用 `stock > 0` 条件更新。两者与兑换单插入在
// 同一事务里，失败全回滚。
func (r *rewardRepository) Redeem(
	ctx context.Context, tenantID uint64, userID, itemID string,
	redemption *types.RewardRedemption, ledger *types.UserPointsLedger,
) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var item types.RewardCatalogItem
		err := tx.Where("tenant_id = ? AND id = ?", tenantID, itemID).First(&item).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrRewardItemNotFound
		}
		if err != nil {
			return err
		}
		if !item.Enabled {
			return ErrRewardItemDisabled
		}

		if item.Stock >= 0 {
			stock := tx.Model(&types.RewardCatalogItem{}).
				Where("tenant_id = ? AND id = ? AND stock > 0", tenantID, itemID).
				Update("stock", gorm.Expr("stock - 1"))
			if stock.Error != nil {
				return stock.Error
			}
			if stock.RowsAffected == 0 {
				return ErrRewardOutOfStock
			}
		}

		now := time.Now().UTC()
		debit := tx.Model(&types.UserPointBalance{}).
			Where("tenant_id = ? AND user_id = ? AND balance >= ?", tenantID, userID, item.CostPoints).
			Updates(map[string]interface{}{
				"balance":    gorm.Expr("balance - ?", item.CostPoints),
				"updated_at": now,
			})
		if debit.Error != nil {
			return debit.Error
		}
		if debit.RowsAffected == 0 {
			return ErrRewardInsufficientPoints
		}
		var balance types.UserPointBalance
		if err := tx.Where("tenant_id = ? AND user_id = ?", tenantID, userID).
			First(&balance).Error; err != nil {
			return err
		}
		if err := tx.Model(&types.UserPointBalance{}).
			Where("id = ?", balance.ID).
			Updates(map[string]interface{}{
				"level":      string(types.PointLevelForBalance(balance.Balance)),
				"updated_at": now,
			}).Error; err != nil {
			return err
		}

		// 目录可能被改名或改价：兑换单记录的是成交那一刻的快照。
		redemption.TenantID = tenantID
		redemption.UserID = userID
		redemption.ItemID = item.ID
		redemption.ItemName = item.Name
		redemption.PointsCost = item.CostPoints
		redemption.Status = string(types.RewardRedemptionPending)
		redemption.CreatedAt = now
		redemption.UpdatedAt = now
		if err := tx.Create(redemption).Error; err != nil {
			return err
		}

		ledger.TenantID = tenantID
		ledger.UserID = userID
		ledger.Action = string(types.PointsActionRedeemed)
		ledger.Points = -item.CostPoints
		ledger.SourceType = "redemption"
		ledger.SourceID = redemption.ID
		ledger.CreatedAt = now
		return tx.Create(ledger).Error
	})
}

// GetRedemption returns one order in the tenant.
func (r *rewardRepository) GetRedemption(
	ctx context.Context, tenantID uint64, redemptionID string,
) (*types.RewardRedemption, error) {
	var redemption types.RewardRedemption
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, redemptionID).
		First(&redemption).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrRewardRedemptionNotFound
	}
	if err != nil {
		return nil, err
	}
	return &redemption, nil
}

// ListRedemptions returns orders, newest first.
func (r *rewardRepository) ListRedemptions(
	ctx context.Context, tenantID uint64, userID string,
	status types.RewardRedemptionStatus, limit int,
) ([]*types.RewardRedemption, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := r.db.WithContext(ctx).Where("tenant_id = ?", tenantID)
	if userID != "" {
		query = query.Where("user_id = ?", userID)
	}
	if status != "" {
		query = query.Where("status = ?", string(status))
	}
	var rows []*types.RewardRedemption
	if err := query.Order("created_at DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// ResolveRedemption transitions one pending order and, for refund statuses,
// credits the points back and restores stock in the same transaction.
func (r *rewardRepository) ResolveRedemption(
	ctx context.Context, tenantID uint64, redemptionID string,
	status types.RewardRedemptionStatus, actorID string,
	refundLedger *types.UserPointsLedger,
) (*types.RewardRedemption, error) {
	var updated types.RewardRedemption
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var redemption types.RewardRedemption
		err := tx.Where("tenant_id = ? AND id = ?", tenantID, redemptionID).
			First(&redemption).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrRewardRedemptionNotFound
		}
		if err != nil {
			return err
		}
		if types.RewardRedemptionStatus(redemption.Status) != types.RewardRedemptionPending {
			return ErrRewardRedemptionNotPending
		}

		now := time.Now().UTC()
		if err := tx.Model(&types.RewardRedemption{}).
			Where("id = ?", redemption.ID).
			Updates(map[string]interface{}{
				"status":     string(status),
				"handled_by": actorID,
				"handled_at": now,
				"updated_at": now,
			}).Error; err != nil {
			return err
		}
		redemption.Status = string(status)
		redemption.HandledBy = actorID
		redemption.HandledAt = &now
		redemption.UpdatedAt = now
		updated = redemption

		if !status.IsRefund() {
			return nil
		}

		credit := tx.Model(&types.UserPointBalance{}).
			Where("tenant_id = ? AND user_id = ?", tenantID, redemption.UserID).
			Updates(map[string]interface{}{
				"balance":    gorm.Expr("balance + ?", redemption.PointsCost),
				"updated_at": now,
			})
		if credit.Error != nil {
			return credit.Error
		}
		if credit.RowsAffected == 0 {
			// 余额行被清理掉了：重建一行，保证退款不丢。
			if err := tx.Create(&types.UserPointBalance{
				TenantID: tenantID,
				UserID:   redemption.UserID,
				Balance:  redemption.PointsCost,
				Level:    string(types.PointLevelForBalance(redemption.PointsCost)),
			}).Error; err != nil {
				return err
			}
		} else {
			var balance types.UserPointBalance
			if err := tx.Where("tenant_id = ? AND user_id = ?", tenantID, redemption.UserID).
				First(&balance).Error; err != nil {
				return err
			}
			if err := tx.Model(&types.UserPointBalance{}).
				Where("id = ?", balance.ID).
				Update("level", string(types.PointLevelForBalance(balance.Balance))).Error; err != nil {
				return err
			}
		}

		if refundLedger != nil {
			refundLedger.TenantID = tenantID
			refundLedger.UserID = redemption.UserID
			refundLedger.Action = string(types.PointsActionRefunded)
			refundLedger.Points = redemption.PointsCost
			refundLedger.SourceType = "redemption"
			refundLedger.SourceID = redemption.ID
			refundLedger.CreatedAt = now
			if err := tx.Create(refundLedger).Error; err != nil {
				return err
			}
		}

		// 库存只对有限库存项回补；-1 表示无限，不需要动。
		if err := tx.Model(&types.RewardCatalogItem{}).
			Where("tenant_id = ? AND id = ? AND stock >= 0", tenantID, redemption.ItemID).
			Update("stock", gorm.Expr("stock + 1")).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &updated, nil
}
