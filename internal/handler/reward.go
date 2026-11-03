package handler

import (
	"context"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// RewardHandler exposes §15.4 points redemption and knowledge-star awards.
type RewardHandler struct {
	rewards interfaces.RewardService
	stars   interfaces.KnowledgeStarService
}

// NewRewardHandler constructs the handler.
func NewRewardHandler(
	rewards interfaces.RewardService, stars interfaces.KnowledgeStarService,
) *RewardHandler {
	return &RewardHandler{rewards: rewards, stars: stars}
}

// rewardItemRequest is the catalog create/update body.
type rewardItemRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	CostPoints  int    `json:"cost_points"`
	Stock       *int   `json:"stock"`
	Enabled     *bool  `json:"enabled"`
}

// redeemRequest is the redemption body.
type redeemRequest struct {
	Note string `json:"note"`
}

// resolveRedemptionRequest is the operator decision body.
type resolveRedemptionRequest struct {
	Status string `json:"status"`
}

// selectStarsRequest is the knowledge-star selection body.
type selectStarsRequest struct {
	PeriodType string   `json:"period_type"`
	Period     string   `json:"period"`
	UserIDs    []string `json:"user_ids"`
	Reason     string   `json:"reason"`
}

// ListRewardCatalog godoc
// @Summary      兑换目录
// @Description  列出启用的积分礼品/福利（KnowAxis V3.0 §15.4 积分可兑换礼品/福利）。
// @Tags         积分兑换
// @Produce      json
// @Param        all  query  bool  false  "管理员查看全部（含已下架）"
// @Success      200  {object}  map[string]interface{}  "{success, data: [RewardCatalogItem]}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /rewards/catalog [get]
func (h *RewardHandler) ListRewardCatalog(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	includeAll := c.Query("all") == "true" || c.Query("all") == "1"
	if includeAll && !callerIsAdmin(ctx) {
		includeAll = false
	}
	items, err := h.rewards.ListItems(ctx, tenantID, !includeAll)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": items})
}

// CreateRewardItem godoc
// @Summary      新增兑换项
// @Description  管理员新增积分礼品/福利。stock=-1 表示不限量。
// @Tags         积分兑换
// @Accept       json
// @Produce      json
// @Param        request  body  rewardItemRequest  true  "兑换项"
// @Success      200  {object}  map[string]interface{}  "{success, data: RewardCatalogItem}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /rewards/catalog [post]
func (h *RewardHandler) CreateRewardItem(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req rewardItemRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	item, err := h.rewards.CreateItem(ctx, tenantID, caller.UserID, req.toInput())
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": item})
}

// UpdateRewardItem godoc
// @Summary      修改兑换项
// @Description  管理员修改积分礼品/福利；空字段表示不修改。
// @Tags         积分兑换
// @Accept       json
// @Produce      json
// @Param        id       path  string             true  "兑换项 ID"
// @Param        request  body  rewardItemRequest  true  "兑换项"
// @Success      200  {object}  map[string]interface{}  "{success, data: RewardCatalogItem}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /rewards/catalog/{id} [put]
func (h *RewardHandler) UpdateRewardItem(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req rewardItemRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	item, err := h.rewards.UpdateItem(ctx, tenantID, c.Param("id"), caller.UserID, req.toInput())
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": item})
}

// RedeemReward godoc
// @Summary      兑换积分礼品
// @Description  用本人积分兑换一件礼品（KnowAxis V3.0 §15.4）。扣分、库存与兑换单在同一事务内完成；余额不足或库存不足时不会产生任何变更。
// @Tags         积分兑换
// @Accept       json
// @Produce      json
// @Param        id       path  string         true  "兑换项 ID"
// @Param        request  body  redeemRequest  false "备注"
// @Success      200  {object}  map[string]interface{}  "{success, data: RewardRedemption}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /rewards/catalog/{id}/redeem [post]
func (h *RewardHandler) RedeemReward(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	caller := types.CallerFromContext(ctx)
	var req redeemRequest
	// 备注可选：空 body 也应允许。
	_ = c.ShouldBindJSON(&req)
	redemption, err := h.rewards.Redeem(ctx, tenantID, caller.UserID, c.Param("id"), req.Note)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": redemption})
}

// ListMyRedemptions godoc
// @Summary      我的兑换记录
// @Description  列出本人的兑换单。
// @Tags         积分兑换
// @Produce      json
// @Param        status  query  string  false  "pending/fulfilled/rejected/canceled"
// @Param        limit   query  int     false  "返回条数（默认 50）"
// @Success      200  {object}  map[string]interface{}  "{success, data: [RewardRedemption]}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /rewards/redemptions [get]
func (h *RewardHandler) ListMyRedemptions(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	caller := types.CallerFromContext(ctx)
	rows, err := h.rewards.ListMyRedemptions(ctx, tenantID, caller.UserID,
		types.RewardRedemptionStatus(c.Query("status")), parseRewardLimit(c))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": rows})
}

// ListAllRedemptions godoc
// @Summary      全部兑换记录
// @Description  管理员查看租户内所有兑换单。
// @Tags         积分兑换
// @Produce      json
// @Param        status  query  string  false  "状态过滤"
// @Param        limit   query  int     false  "返回条数（默认 50）"
// @Success      200  {object}  map[string]interface{}  "{success, data: [RewardRedemption]}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /rewards/admin/redemptions [get]
func (h *RewardHandler) ListAllRedemptions(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	rows, err := h.rewards.ListRedemptions(ctx, tenantID,
		types.RewardRedemptionStatus(c.Query("status")), parseRewardLimit(c))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": rows})
}

// CancelRedemption godoc
// @Summary      取消兑换
// @Description  本人取消待处理的兑换单，积分退回、库存回补。
// @Tags         积分兑换
// @Produce      json
// @Param        id  path  string  true  "兑换单 ID"
// @Success      200  {object}  map[string]interface{}  "{success, data: RewardRedemption}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /rewards/redemptions/{id}/cancel [post]
func (h *RewardHandler) CancelRedemption(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	caller := types.CallerFromContext(ctx)
	redemption, err := h.rewards.Cancel(ctx, tenantID, caller.UserID, c.Param("id"))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": redemption})
}

// ResolveRedemption godoc
// @Summary      处理兑换单
// @Description  管理员标记已发放（fulfilled）或驳回（rejected）；驳回时积分退回、库存回补。
// @Tags         积分兑换
// @Accept       json
// @Produce      json
// @Param        id       path  string                   true  "兑换单 ID"
// @Param        request  body  resolveRedemptionRequest  true  "处理结果"
// @Success      200  {object}  map[string]interface{}  "{success, data: RewardRedemption}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /rewards/admin/redemptions/{id}/resolve [post]
func (h *RewardHandler) ResolveRedemption(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req resolveRedemptionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	redemption, err := h.rewards.Resolve(ctx, tenantID, caller.UserID, c.Param("id"),
		types.RewardRedemptionStatus(strings.TrimSpace(req.Status)))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": redemption})
}

// ListKnowledgeStars godoc
// @Summary      知识之星（公开）
// @Description  查看已公开表彰的知识之星（KnowAxis V3.0 §15.4）。不传周期时返回最近几期。
// @Tags         知识之星
// @Produce      json
// @Param        period_type  query  string  false  "month/quarter"
// @Param        period       query  string  false  "YYYY-MM 或 YYYY-Qn"
// @Param        limit        query  int     false  "返回条数（默认 100）"
// @Success      200  {object}  map[string]interface{}  "{success, data: [KnowledgeStarAward]}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge-stars [get]
func (h *RewardHandler) ListKnowledgeStars(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	rows, err := h.stars.List(ctx, tenantID, c.Query("period_type"), c.Query("period"),
		true, parseRewardLimit(c))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": rows})
}

// ListKnowledgeStarsAdmin godoc
// @Summary      知识之星（管理视图）
// @Description  管理员查看含未公开的评选结果。
// @Tags         知识之星
// @Produce      json
// @Param        period_type  query  string  false  "month/quarter"
// @Param        period       query  string  false  "YYYY-MM 或 YYYY-Qn"
// @Param        limit        query  int     false  "返回条数（默认 100）"
// @Success      200  {object}  map[string]interface{}  "{success, data: [KnowledgeStarAward]}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge-stars/admin [get]
func (h *RewardHandler) ListKnowledgeStarsAdmin(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	rows, err := h.stars.List(ctx, tenantID, c.Query("period_type"), c.Query("period"),
		false, parseRewardLimit(c))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": rows})
}

// KnowledgeStarCandidates godoc
// @Summary      知识之星候选
// @Description  按周期内积分贡献排出候选人（管理员）。
// @Tags         知识之星
// @Produce      json
// @Param        period_type  query  string  false  "month/quarter（默认 month）"
// @Param        period       query  string  false  "周期（默认当前）"
// @Param        limit        query  int     false  "返回条数（默认 50）"
// @Success      200  {object}  map[string]interface{}  "{success, data: [KnowledgeStarCandidate]}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge-stars/candidates [get]
func (h *RewardHandler) KnowledgeStarCandidates(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	candidates, err := h.stars.Candidates(ctx, tenantID,
		types.KnowledgeStarPeriodType(c.Query("period_type")), c.Query("period"),
		parseRewardLimit(c))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": candidates})
}

// SelectKnowledgeStars godoc
// @Summary      选定知识之星
// @Description  管理员选定某一期的知识之星；排名按周期内贡献分自动计算，结果在公开前仅管理员可见。
// @Tags         知识之星
// @Accept       json
// @Produce      json
// @Param        request  body  selectStarsRequest  true  "评选结果"
// @Success      200  {object}  map[string]interface{}  "{success, data: [KnowledgeStarAward]}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge-stars [post]
func (h *RewardHandler) SelectKnowledgeStars(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req selectStarsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	awards, err := h.stars.Select(ctx, tenantID, caller.UserID, types.KnowledgeStarSelection{
		PeriodType: types.KnowledgeStarPeriodType(req.PeriodType),
		Period:     req.Period,
		UserIDs:    req.UserIDs,
		Reason:     req.Reason,
	})
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": awards})
}

// PublishKnowledgeStars godoc
// @Summary      公开知识之星
// @Description  把某一期的评选结果公开表彰（管理员）。
// @Tags         知识之星
// @Accept       json
// @Produce      json
// @Param        request  body  selectStarsRequest  true  "周期（忽略 user_ids）"
// @Success      200  {object}  map[string]interface{}  "{success, data: {published: N}}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge-stars/publish [post]
func (h *RewardHandler) PublishKnowledgeStars(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req selectStarsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	affected, err := h.stars.Publish(ctx, tenantID, caller.UserID,
		types.KnowledgeStarPeriodType(req.PeriodType), req.Period)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": gin.H{"published": affected}})
}

// toInput converts the request body into the service input.
func (r rewardItemRequest) toInput() types.RewardCatalogItemInput {
	return types.RewardCatalogItemInput{
		Name:        r.Name,
		Description: r.Description,
		CostPoints:  r.CostPoints,
		Stock:       r.Stock,
		Enabled:     r.Enabled,
	}
}

// parseRewardLimit parses the limit query with a sane default.
func parseRewardLimit(c *gin.Context) int {
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if err != nil || limit <= 0 || limit > 200 {
		return 50
	}
	return limit
}

// callerIsAdmin reports whether the caller may see retired catalog items.
func callerIsAdmin(ctx context.Context) bool {
	if types.IsSystemAdminFromContext(ctx) {
		return true
	}
	return types.CallerFromContext(ctx).Role.HasPermission(types.TenantRoleAdmin)
}
