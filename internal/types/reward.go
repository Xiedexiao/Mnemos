package types

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ============================================================================
// 积分兑换（KnowAxis V3.0 §15.4 "积分可兑换礼品/福利"）。
//
// 兑换是平台里第一个会**扣减**积分的动作。扣减必须和余额检查、库存扣减、
// 兑换单创建在同一个事务里完成，否则并发兑换能把余额或库存刷成负数。
// 因此这一块的写入路径在仓储层用事务实现，服务层只做参数校验与状态机。
// ============================================================================

// RewardRedemptionStatus is the redemption lifecycle.
type RewardRedemptionStatus string

const (
	// RewardRedemptionPending is created and points are debited; awaiting
	// fulfilment by an operator.
	RewardRedemptionPending RewardRedemptionStatus = "pending"
	// RewardRedemptionFulfilled is delivered; points stay debited.
	RewardRedemptionFulfilled RewardRedemptionStatus = "fulfilled"
	// RewardRedemptionRejected is refused; points are refunded and stock is
	// restored.
	RewardRedemptionRejected RewardRedemptionStatus = "rejected"
	// RewardRedemptionCanceled is canceled by the user before fulfilment;
	// points are refunded and stock is restored.
	RewardRedemptionCanceled RewardRedemptionStatus = "canceled"
)

// Valid reports whether the status is known.
func (s RewardRedemptionStatus) Valid() bool {
	switch s {
	case RewardRedemptionPending, RewardRedemptionFulfilled,
		RewardRedemptionRejected, RewardRedemptionCanceled:
		return true
	default:
		return false
	}
}

// IsRefund reports whether entering this status returns the points.
func (s RewardRedemptionStatus) IsRefund() bool {
	return s == RewardRedemptionRejected || s == RewardRedemptionCanceled
}

// RewardCatalogItem is one redeemable gift or benefit.
type RewardCatalogItem struct {
	ID       string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID uint64 `json:"tenant_id" gorm:"not null;index:idx_reward_items_tenant,priority:1"`
	// Name is the display name.
	Name string `json:"name" gorm:"type:varchar(128);not null"`
	// Description explains what the user gets.
	Description string `json:"description" gorm:"type:varchar(1024);not null;default:''"`
	// CostPoints is the price in points (must be > 0).
	CostPoints int `json:"cost_points" gorm:"not null"`
	// Stock is the remaining quantity; -1 means unlimited.
	//
	// 没有 gorm default tag：GORM 会把带 default 的零值字段从 INSERT 里省掉，
	// 显式设置的 stock=0（售罄）会被数据库默认值 -1 覆盖成"不限量"。默认值
	// 由服务层与迁移负责。
	Stock int `json:"stock" gorm:"not null"`
	// Enabled hides the item from the catalog when false (it stays for the
	// redemption history). Same zero-value caveat as Stock: no gorm default.
	Enabled   bool      `json:"enabled" gorm:"not null;index:idx_reward_items_tenant,priority:2"`
	CreatedBy string    `json:"created_by" gorm:"type:varchar(36);not null;default:''"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName pins the GORM table name.
func (RewardCatalogItem) TableName() string { return "reward_catalog_items" }

// BeforeCreate assigns an ID before persistence.
func (i *RewardCatalogItem) BeforeCreate(_ *gorm.DB) error {
	if i.ID == "" {
		i.ID = uuid.NewString()
	}
	return nil
}

// RewardRedemption is one user's claim on a catalog item.
type RewardRedemption struct {
	ID       string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID uint64 `json:"tenant_id" gorm:"not null;index:idx_reward_redemptions_user,priority:1"`
	UserID   string `json:"user_id" gorm:"type:varchar(36);not null;index:idx_reward_redemptions_user,priority:2"`
	ItemID   string `json:"item_id" gorm:"type:varchar(36);not null"`
	// ItemName is a snapshot: the item may later be renamed or deleted, the
	// redemption must still read correctly.
	ItemName string `json:"item_name" gorm:"type:varchar(128);not null"`
	// PointsCost is the price actually paid (a snapshot for the same reason).
	PointsCost int `json:"points_cost" gorm:"not null"`
	// Status is the lifecycle state.
	Status string `json:"status" gorm:"type:varchar(16);not null;default:'pending';index:idx_reward_redemptions_user,priority:3"`
	// Note is the user's message (size, address, preference).
	Note string `json:"note" gorm:"type:varchar(512);not null;default:''"`
	// HandledBy / HandledAt record the operator decision.
	HandledBy string     `json:"handled_by" gorm:"type:varchar(36);not null;default:''"`
	HandledAt *time.Time `json:"handled_at"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// TableName pins the GORM table name.
func (RewardRedemption) TableName() string { return "reward_redemptions" }

// BeforeCreate assigns an ID before persistence.
func (r *RewardRedemption) BeforeCreate(_ *gorm.DB) error {
	if r.ID == "" {
		r.ID = uuid.NewString()
	}
	return nil
}

// RewardCatalogItemInput is the Admin create/update payload.
type RewardCatalogItemInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	CostPoints  int    `json:"cost_points"`
	// Stock < 0 means unlimited; nil on update means unchanged.
	Stock   *int  `json:"stock"`
	Enabled *bool `json:"enabled"`
}

// ValidateRewardCatalogItemInput checks the shape shared by create and update.
func ValidateRewardCatalogItemInput(input RewardCatalogItemInput, create bool) error {
	if create && strings.TrimSpace(input.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if input.CostPoints < 0 {
		return fmt.Errorf("cost_points cannot be negative")
	}
	if create && input.CostPoints == 0 {
		return fmt.Errorf("cost_points must be greater than zero")
	}
	if input.Stock != nil && *input.Stock < -1 {
		return fmt.Errorf("stock must be -1 (unlimited) or a non-negative number")
	}
	return nil
}
