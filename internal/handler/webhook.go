package handler

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// WebhookHandler exposes the §16.4 outbound webhook management surface.
type WebhookHandler struct {
	webhooks interfaces.WebhookService
}

// NewWebhookHandler constructs the handler.
func NewWebhookHandler(webhooks interfaces.WebhookService) *WebhookHandler {
	return &WebhookHandler{webhooks: webhooks}
}

// webhookEndpointRequest is the create/update body.
type webhookEndpointRequest struct {
	URL         string   `json:"url"`
	Description string   `json:"description"`
	Events      []string `json:"events"`
	Enabled     *bool    `json:"enabled"`
	Secret      string   `json:"secret"`
}

// toInput converts the request.
func (r webhookEndpointRequest) toInput() types.WebhookEndpointInput {
	return types.WebhookEndpointInput{
		URL:         r.URL,
		Description: r.Description,
		Events:      r.Events,
		Enabled:     r.Enabled,
		Secret:      r.Secret,
	}
}

// ListWebhookEventTypes godoc
// @Summary      Webhook 事件类型
// @Description  返回可订阅的出站事件目录（知识发布/更新/下架、审批完成、权限变更）。
// @Tags         Webhook
// @Produce      json
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /webhooks/events [get]
func (h *WebhookHandler) ListWebhookEventTypes(c *gin.Context) {
	c.JSON(200, gin.H{"success": true, "data": types.AllWebhookEventTypes()})
}

// ListWebhookEndpoints godoc
// @Summary      Webhook 订阅列表
// @Description  列出本工作区的出站 Webhook 订阅。签名密钥永不返回，只给出 has_secret。
// @Tags         Webhook
// @Produce      json
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /webhooks/endpoints [get]
func (h *WebhookHandler) ListWebhookEndpoints(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	rows, err := h.webhooks.ListEndpoints(ctx, tenantID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": rows})
}

// CreateWebhookEndpoint godoc
// @Summary      新增 Webhook 订阅
// @Description  订阅一个出站事件接收方（KnowAxis V3.0 §16.4）。签名密钥至少 16 位，AES-GCM 加密存储；没有 SYSTEM_AES_KEY 时拒绝而不是明文入库。
// @Tags         Webhook
// @Accept       json
// @Produce      json
// @Param        request  body  webhookEndpointRequest  true  "订阅"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /webhooks/endpoints [post]
func (h *WebhookHandler) CreateWebhookEndpoint(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req webhookEndpointRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(apperrors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	endpoint, err := h.webhooks.CreateEndpoint(ctx, tenantID, caller.UserID, req.toInput())
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": endpoint})
}

// UpdateWebhookEndpoint godoc
// @Summary      修改 Webhook 订阅
// @Description  修改订阅。空字段表示不修改；secret 传 "-" 清除已存的签名密钥。
// @Tags         Webhook
// @Accept       json
// @Produce      json
// @Param        id       path  string                 true  "订阅 ID"
// @Param        request  body  webhookEndpointRequest  true  "订阅"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /webhooks/endpoints/{id} [put]
func (h *WebhookHandler) UpdateWebhookEndpoint(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req webhookEndpointRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(apperrors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	endpoint, err := h.webhooks.UpdateEndpoint(ctx, tenantID, c.Param("id"),
		caller.UserID, req.toInput())
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": endpoint})
}

// DeleteWebhookEndpoint godoc
// @Summary      删除 Webhook 订阅
// @Description  删除订阅并清理未投递的事件；已投递与已死信的行保留供排查。
// @Tags         Webhook
// @Produce      json
// @Param        id  path  string  true  "订阅 ID"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /webhooks/endpoints/{id} [delete]
func (h *WebhookHandler) DeleteWebhookEndpoint(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	caller := types.CallerFromContext(ctx)
	if err := h.webhooks.DeleteEndpoint(ctx, tenantID, c.Param("id"), caller.UserID); err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true})
}

// ListWebhookDeliveries godoc
// @Summary      投递记录
// @Description  查看出站事件投递记录与状态（pending/delivered/dead）。
// @Tags         Webhook
// @Produce      json
// @Param        endpoint_id  query  string  false  "订阅 ID"
// @Param        state        query  string  false  "状态"
// @Param        limit        query  int     false  "条数（默认 50）"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /webhooks/deliveries [get]
func (h *WebhookHandler) ListWebhookDeliveries(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if err != nil || limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, listErr := h.webhooks.ListDeliveries(ctx, tenantID,
		strings.TrimSpace(c.Query("endpoint_id")),
		types.WebhookDeliveryState(strings.TrimSpace(c.Query("state"))), limit)
	if listErr != nil {
		c.Error(listErr)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": rows})
}

// ReplayWebhookDelivery godoc
// @Summary      重投死信事件
// @Description  把一条已放弃的投递重新排入队列（订阅方修复后使用）。
// @Tags         Webhook
// @Produce      json
// @Param        id  path  string  true  "投递 ID"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /webhooks/deliveries/{id}/replay [post]
func (h *WebhookHandler) ReplayWebhookDelivery(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	caller := types.CallerFromContext(ctx)
	if err := h.webhooks.Replay(ctx, tenantID, c.Param("id"), caller.UserID); err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true})
}
