package handler

import (
	"github.com/gin-gonic/gin"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// TokenBudgetHandler exposes the §16.5 budget and fallback-chain surface.
type TokenBudgetHandler struct {
	budgets interfaces.TokenBudgetService
}

// NewTokenBudgetHandler constructs the handler.
func NewTokenBudgetHandler(budgets interfaces.TokenBudgetService) *TokenBudgetHandler {
	return &TokenBudgetHandler{budgets: budgets}
}

// GetTokenBudget godoc
// @Summary      Token 预算与用量
// @Description  返回当前租户的 token 预算并评估本期用量（KnowAxis V3.0 §16.5）。未配置预算时返回一份"没有上限"的默认值而不是错误。达到阈值只告警、不阻断调用；告警按 80%/100%/120% 分档，且只在跨档时触发。
// @Tags         成本控制
// @Produce      json
// @Success      200  {object}  map[string]interface{}  "{success, data: TokenBudgetAssessment}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /token-budget [get]
func (h *TokenBudgetHandler) GetTokenBudget(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	assessment, err := h.budgets.Check(ctx, tenantID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": assessment})
}

// saveTokenBudgetRequest is the PUT body.
type saveTokenBudgetRequest struct {
	Period      string `json:"period"`
	LimitTokens int64  `json:"limit_tokens"`
	WarnPercent int    `json:"warn_percent"`
}

// SaveTokenBudget godoc
// @Summary      设置 Token 预算
// @Description  设置当前租户的 token 预算（KnowAxis V3.0 §16.5）。周期支持 daily/monthly，窗口按 UTC 计算，与用量记账同口径。
// @Tags         成本控制
// @Accept       json
// @Produce      json
// @Param        request  body  saveTokenBudgetRequest  true  "{period, limit_tokens, warn_percent}"
// @Success      200  {object}  map[string]interface{}  "{success, data: TokenBudget}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /token-budget [put]
func (h *TokenBudgetHandler) SaveTokenBudget(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req saveTokenBudgetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(apperrors.NewBadRequestError(err.Error()))
		return
	}
	saved, err := h.budgets.SaveBudget(ctx, tenantID, types.TokenBudget{
		Period:      types.TokenBudgetPeriod(req.Period),
		LimitTokens: req.LimitTokens,
		WarnPercent: req.WarnPercent,
	})
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": saved})
}

// ListFallbackChains godoc
// @Summary      模型降级链列表
// @Description  列出各场景配置的模型降级链（KnowAxis V3.0 §16.5）。顺序即优先级：第一个是主力，其后依次是降级候选。
// @Tags         成本控制
// @Produce      json
// @Success      200  {object}  map[string]interface{}  "{success, data: [ModelFallbackChain]}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /model-fallback-chains [get]
func (h *TokenBudgetHandler) ListFallbackChains(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	chains, err := h.budgets.ListFallbackChains(ctx, tenantID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": chains})
}

// saveFallbackChainRequest is the PUT body.
type saveFallbackChainRequest struct {
	Scene       string   `json:"scene"`
	ModelIDs    []string `json:"model_ids"`
	Description string   `json:"description"`
}

// SaveFallbackChain godoc
// @Summary      设置模型降级链
// @Description  按场景设置模型降级链（KnowAxis V3.0 §16.5）。链内重复与空 ID 会被去掉，超过 4 级会被截断——链越长，最坏情况延迟越长，那就不是降级而是重试风暴。
// @Tags         成本控制
// @Accept       json
// @Produce      json
// @Param        request  body  saveFallbackChainRequest  true  "{scene, model_ids, description}"
// @Success      200  {object}  map[string]interface{}  "{success, data: ModelFallbackChain}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /model-fallback-chains [put]
func (h *TokenBudgetHandler) SaveFallbackChain(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req saveFallbackChainRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(apperrors.NewBadRequestError(err.Error()))
		return
	}
	saved, err := h.budgets.SaveFallbackChain(ctx, tenantID, types.ModelFallbackChain{
		Scene:       req.Scene,
		ModelIDs:    req.ModelIDs,
		Description: req.Description,
	})
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": saved})
}

// DeleteFallbackChain godoc
// @Summary      删除模型降级链
// @Description  删除某个场景的降级链，该场景回到"只用主力模型，失败即失败"。
// @Tags         成本控制
// @Produce      json
// @Param        scene  path  string  true  "场景"
// @Success      200  {object}  map[string]interface{}  "{success}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /model-fallback-chains/{scene} [delete]
func (h *TokenBudgetHandler) DeleteFallbackChain(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	if err := h.budgets.DeleteFallbackChain(ctx, tenantID, c.Param("scene")); err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true})
}
