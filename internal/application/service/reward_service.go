package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// 兑换（§15.4）的审计动作。
const (
	auditActionRewardItemCreated = "reward.item_created"
	auditActionRewardItemUpdated = "reward.item_updated"
	auditActionRewardRedeemed    = "reward.redeemed"
	auditActionRewardResolved    = "reward.redemption_resolved"
)

// maxRewardNoteLen mirrors the migration's varchar(512).
const maxRewardNoteLen = 512

// rewardService implements interfaces.RewardService (KnowAxis V3.0 §15.4).
type rewardService struct {
	repo  interfaces.RewardRepository
	audit interfaces.AuditLogService
	now   func() time.Time
}

// NewRewardService constructs the service.
func NewRewardService(
	repo interfaces.RewardRepository, audit interfaces.AuditLogService,
) interfaces.RewardService {
	return &rewardService{repo: repo, audit: audit, now: time.Now}
}

// CreateItem adds a catalog item.
func (s *rewardService) CreateItem(
	ctx context.Context, tenantID uint64, actorID string,
	input types.RewardCatalogItemInput,
) (*types.RewardCatalogItem, error) {
	if tenantID == 0 {
		return nil, apperrors.NewUnauthorizedError("tenant context is required")
	}
	if err := types.ValidateRewardCatalogItemInput(input, true); err != nil {
		return nil, apperrors.NewBadRequestError(err.Error())
	}
	stock := -1
	if input.Stock != nil {
		stock = *input.Stock
	}
	item := &types.RewardCatalogItem{
		TenantID:    tenantID,
		Name:        strings.TrimSpace(input.Name),
		Description: strings.TrimSpace(input.Description),
		CostPoints:  input.CostPoints,
		Stock:       stock,
		Enabled:     true,
		CreatedBy:   actorID,
	}
	if input.Enabled != nil {
		item.Enabled = *input.Enabled
	}
	if err := s.repo.CreateItem(ctx, item); err != nil {
		return nil, err
	}
	s.logAudit(ctx, tenantID, actorID, auditActionRewardItemCreated, "catalog_item", item.ID,
		map[string]interface{}{"name": item.Name, "cost_points": item.CostPoints, "stock": item.Stock})
	return item, nil
}

// UpdateItem edits a catalog item.
//
// 空字段表示不修改；stock 用指针区分"未提供"与"设为 0"（售罄）。需要清空
// 描述时把字段留空的语义是"不变"，这是刻意的：目录是长表单，误清空比
// 保留一句旧描述更难恢复。
func (s *rewardService) UpdateItem(
	ctx context.Context, tenantID uint64, itemID, actorID string,
	input types.RewardCatalogItemInput,
) (*types.RewardCatalogItem, error) {
	if err := types.ValidateRewardCatalogItemInput(input, false); err != nil {
		return nil, apperrors.NewBadRequestError(err.Error())
	}
	item, err := s.repo.GetItem(ctx, tenantID, itemID)
	if err != nil {
		return nil, mapRewardError(err)
	}
	if strings.TrimSpace(input.Name) != "" {
		item.Name = strings.TrimSpace(input.Name)
	}
	if strings.TrimSpace(input.Description) != "" {
		item.Description = strings.TrimSpace(input.Description)
	}
	if input.CostPoints != 0 {
		item.CostPoints = input.CostPoints
	}
	if input.Stock != nil {
		item.Stock = *input.Stock
	}
	if input.Enabled != nil {
		item.Enabled = *input.Enabled
	}
	if err := s.repo.UpdateItem(ctx, item); err != nil {
		return nil, mapRewardError(err)
	}
	s.logAudit(ctx, tenantID, actorID, auditActionRewardItemUpdated, "catalog_item", item.ID,
		map[string]interface{}{"name": item.Name, "cost_points": item.CostPoints,
			"stock": item.Stock, "enabled": item.Enabled})
	return item, nil
}

// ListItems returns the catalog.
func (s *rewardService) ListItems(
	ctx context.Context, tenantID uint64, enabledOnly bool,
) ([]*types.RewardCatalogItem, error) {
	return s.repo.ListItems(ctx, tenantID, enabledOnly)
}

// Redeem spends the user's points on one item.
func (s *rewardService) Redeem(
	ctx context.Context, tenantID uint64, userID, itemID, note string,
) (*types.RewardRedemption, error) {
	if userID == "" {
		return nil, apperrors.NewUnauthorizedError("caller identity is required")
	}
	note = strings.TrimSpace(note)
	if len([]rune(note)) > maxRewardNoteLen {
		return nil, apperrors.NewBadRequestError("note is too long")
	}
	redemption := &types.RewardRedemption{Note: note}
	ledger := &types.UserPointsLedger{}
	if err := s.repo.Redeem(ctx, tenantID, userID, itemID, redemption, ledger); err != nil {
		return nil, mapRewardError(err)
	}
	s.logAudit(ctx, tenantID, userID, auditActionRewardRedeemed, "redemption", redemption.ID,
		map[string]interface{}{"item_id": redemption.ItemID, "item_name": redemption.ItemName,
			"points_cost": redemption.PointsCost})
	return redemption, nil
}

// Cancel lets the owner cancel a pending order.
func (s *rewardService) Cancel(
	ctx context.Context, tenantID uint64, userID, redemptionID string,
) (*types.RewardRedemption, error) {
	redemption, err := s.repo.GetRedemption(ctx, tenantID, redemptionID)
	if err != nil {
		return nil, mapRewardError(err)
	}
	if redemption.UserID != userID {
		// 不暴露"存在但不属于你"：与不存在同样返回 404。
		return nil, apperrors.NewNotFoundError("redemption not found")
	}
	resolved, err := s.repo.ResolveRedemption(ctx, tenantID, redemptionID,
		types.RewardRedemptionCanceled, userID, &types.UserPointsLedger{})
	if err != nil {
		return nil, mapRewardError(err)
	}
	s.logAudit(ctx, tenantID, userID, auditActionRewardResolved, "redemption", redemptionID,
		map[string]interface{}{"status": resolved.Status, "refunded": true})
	return resolved, nil
}

// ListMyRedemptions returns the caller's orders.
func (s *rewardService) ListMyRedemptions(
	ctx context.Context, tenantID uint64, userID string,
	status types.RewardRedemptionStatus, limit int,
) ([]*types.RewardRedemption, error) {
	if userID == "" {
		return nil, apperrors.NewUnauthorizedError("caller identity is required")
	}
	if status != "" && !status.Valid() {
		return nil, apperrors.NewBadRequestError("unknown redemption status")
	}
	return s.repo.ListRedemptions(ctx, tenantID, userID, status, limit)
}

// ListRedemptions returns all orders (Admin).
func (s *rewardService) ListRedemptions(
	ctx context.Context, tenantID uint64, status types.RewardRedemptionStatus, limit int,
) ([]*types.RewardRedemption, error) {
	if status != "" && !status.Valid() {
		return nil, apperrors.NewBadRequestError("unknown redemption status")
	}
	return s.repo.ListRedemptions(ctx, tenantID, "", status, limit)
}

// Resolve records the operator decision.
func (s *rewardService) Resolve(
	ctx context.Context, tenantID uint64, actorID, redemptionID string,
	status types.RewardRedemptionStatus,
) (*types.RewardRedemption, error) {
	if status != types.RewardRedemptionFulfilled && status != types.RewardRedemptionRejected {
		return nil, apperrors.NewBadRequestError("status must be fulfilled or rejected")
	}
	refund := &types.UserPointsLedger{}
	if !status.IsRefund() {
		refund = nil
	}
	resolved, err := s.repo.ResolveRedemption(ctx, tenantID, redemptionID, status, actorID, refund)
	if err != nil {
		return nil, mapRewardError(err)
	}
	s.logAudit(ctx, tenantID, actorID, auditActionRewardResolved, "redemption", redemptionID,
		map[string]interface{}{"status": resolved.Status, "refunded": status.IsRefund()})
	return resolved, nil
}

// mapRewardError maps repository sentinels onto API errors.
func mapRewardError(err error) error {
	switch {
	case errors.Is(err, repository.ErrRewardItemNotFound):
		return apperrors.NewNotFoundError("reward item not found")
	case errors.Is(err, repository.ErrRewardRedemptionNotFound):
		return apperrors.NewNotFoundError("redemption not found")
	case errors.Is(err, repository.ErrRewardItemDisabled):
		return apperrors.NewBadRequestError("this reward is no longer available")
	case errors.Is(err, repository.ErrRewardOutOfStock):
		return apperrors.NewBadRequestError("this reward is out of stock")
	case errors.Is(err, repository.ErrRewardInsufficientPoints):
		return apperrors.NewBadRequestError("insufficient points")
	case errors.Is(err, repository.ErrRewardRedemptionNotPending):
		return apperrors.NewBadRequestError("redemption is already handled")
	default:
		return err
	}
}

// logAudit writes one reward audit event; failures never block the write.
func (s *rewardService) logAudit(
	ctx context.Context, tenantID uint64, actorID, action, targetType, targetID string,
	details map[string]interface{},
) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Log(ctx, &types.AuditLog{
		TenantID:    tenantID,
		ActorUserID: actorID,
		Action:      types.AuditAction(action),
		ScopeType:   "reward",
		TargetType:  targetType,
		TargetID:    targetID,
		Outcome:     "success",
		Details:     gamificationAuditJSON(details),
	})
}
