package handler

import (
	"github.com/gin-gonic/gin"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// NetworkAccessHandler exposes the §16.3 IP allow-list admin surface.
type NetworkAccessHandler struct {
	policy interfaces.NetworkAccessService
}

// NewNetworkAccessHandler constructs the handler.
func NewNetworkAccessHandler(policy interfaces.NetworkAccessService) *NetworkAccessHandler {
	return &NetworkAccessHandler{policy: policy}
}

// networkAccessRequest is the update body.
type networkAccessRequest struct {
	AllowEntries []string `json:"allow_entries"`
	Enabled      bool     `json:"enabled"`
}

// GetNetworkAccessPolicy godoc
// @Summary      来源地址白名单
// @Description  读取本工作区的 IP 白名单（KnowAxis V3.0 §16.3）。空列表表示不限制。
// @Tags         空间管理
// @Produce      json
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /network-access [get]
func (h *NetworkAccessHandler) GetNetworkAccessPolicy(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	policy, err := h.policy.Get(ctx, tenantID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": policy})
}

// UpdateNetworkAccessPolicy godoc
// @Summary      设置来源地址白名单
// @Description  设置本工作区允许的来源地址（CIDR 或精确 IP，IPv4/IPv6）。启用后不在列表内的请求会被 403 拒绝。写错的条目会直接报错而不是被静默丢弃——否则"我明明加了公司网段"会变成"所有人都进不来"。
// @Tags         空间管理
// @Accept       json
// @Produce      json
// @Param        request  body  networkAccessRequest  true  "白名单"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /network-access [put]
func (h *NetworkAccessHandler) UpdateNetworkAccessPolicy(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req networkAccessRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(apperrors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	policy, err := h.policy.Set(ctx, tenantID, caller.UserID, types.NetworkAccessPolicyInput{
		AllowEntries: req.AllowEntries,
		Enabled:      req.Enabled,
	})
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": policy})
}
