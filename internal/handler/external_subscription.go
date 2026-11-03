package handler

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// ExternalSubscriptionHandler exposes the §7.3 external-subscription collector.
type ExternalSubscriptionHandler struct {
	feeds interfaces.ExternalSubscriptionService
}

// NewExternalSubscriptionHandler constructs the handler.
func NewExternalSubscriptionHandler(
	feeds interfaces.ExternalSubscriptionService,
) *ExternalSubscriptionHandler {
	return &ExternalSubscriptionHandler{feeds: feeds}
}

// externalFeedRequest is the create/update body.
type externalFeedRequest struct {
	Name               string   `json:"name"`
	FeedURL            string   `json:"feed_url"`
	Format             string   `json:"format"`
	DomainTags         []string `json:"domain_tags"`
	KnowledgeBaseIDs   []string `json:"knowledge_base_ids"`
	Enabled            *bool    `json:"enabled"`
	CheckIntervalHours int      `json:"check_interval_hours"`
	AuthHeader         string   `json:"auth_header"`
}

// CreateExternalFeed godoc
// @Summary      新增外部订阅源
// @Description  订阅法规/国标/行业标准/监管口径的 RSS/Atom/JSON Feed（KnowAxis V3.0 §7.3）。采集到的新条目会按领域标签匹配知识，并为每篇匹配到的知识生成一条 external_subscription 变更信号。
// @Tags         变更信号
// @Accept       json
// @Produce      json
// @Param        request  body  externalFeedRequest  true  "订阅源"
// @Success      200  {object}  map[string]interface{}  "{success, data: ExternalSubscriptionFeed}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /external-subscriptions/feeds [post]
func (h *ExternalSubscriptionHandler) CreateExternalFeed(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req externalFeedRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	feed, err := h.feeds.CreateFeed(ctx, tenantID, caller.UserID, req.toInput())
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": feed})
}

// ListExternalFeeds godoc
// @Summary      订阅源列表
// @Description  列出本租户的法规/标准订阅源，含上次检查时间与上次错误。
// @Tags         变更信号
// @Produce      json
// @Param        enabled_only  query  bool  false  "只看启用的"
// @Success      200  {object}  map[string]interface{}  "{success, data: [ExternalSubscriptionFeed]}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /external-subscriptions/feeds [get]
func (h *ExternalSubscriptionHandler) ListExternalFeeds(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	enabledOnly := c.Query("enabled_only") == "true" || c.Query("enabled_only") == "1"
	feeds, err := h.feeds.ListFeeds(ctx, tenantID, enabledOnly)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": feeds})
}

// UpdateExternalFeed godoc
// @Summary      修改订阅源
// @Description  修改订阅配置。空字段表示不修改；domain_tags/knowledge_base_ids 传 [] 清空；auth_header 传 "-" 清除已存的认证头。
// @Tags         变更信号
// @Accept       json
// @Produce      json
// @Param        id       path  string               true  "订阅源 ID"
// @Param        request  body  externalFeedRequest  true  "订阅源"
// @Success      200  {object}  map[string]interface{}  "{success, data: ExternalSubscriptionFeed}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /external-subscriptions/feeds/{id} [put]
func (h *ExternalSubscriptionHandler) UpdateExternalFeed(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req externalFeedRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	feed, err := h.feeds.UpdateFeed(ctx, tenantID, c.Param("id"), req.toInput())
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": feed})
}

// DeleteExternalFeed godoc
// @Summary      删除订阅源
// @Description  退订并清除已采集的条目标记（已生成的变更信号保留）。
// @Tags         变更信号
// @Produce      json
// @Param        id  path  string  true  "订阅源 ID"
// @Success      200  {object}  map[string]interface{}  "{success: true}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /external-subscriptions/feeds/{id} [delete]
func (h *ExternalSubscriptionHandler) DeleteExternalFeed(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	if err := h.feeds.DeleteFeed(ctx, tenantID, c.Param("id")); err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true})
}

// SyncExternalFeed godoc
// @Summary      立即检查订阅源
// @Description  立即抓取并解析一个订阅源，为新条目匹配知识并生成变更信号，返回本次结果（抓取数/新条目数/匹配知识数/信号数）。
// @Tags         变更信号
// @Produce      json
// @Param        id  path  string  true  "订阅源 ID"
// @Success      200  {object}  map[string]interface{}  "{success, data: ExternalSubscriptionSyncResult}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /external-subscriptions/feeds/{id}/sync [post]
func (h *ExternalSubscriptionHandler) SyncExternalFeed(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	result, err := h.feeds.SyncFeed(ctx, tenantID, c.Param("id"))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": result})
}

// SweepExternalFeeds godoc
// @Summary      立即执行一轮到期检查
// @Description  检查所有到期的订阅源（调度器每小时做一次，这里是运维手动触发）。
// @Tags         变更信号
// @Produce      json
// @Success      200  {object}  map[string]interface{}  "{success, data: ExternalSubscriptionSweepResult}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /external-subscriptions/sweep [post]
func (h *ExternalSubscriptionHandler) SweepExternalFeeds(c *gin.Context) {
	ctx := c.Request.Context()
	if _, ok := types.TenantIDFromContext(ctx); !ok {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	// SyncDue 跨租户扫描（调度器语义）；手动触发只允许系统管理员，由路由
	// 层保证，这里再确认一次。
	if !types.IsSystemAdminFromContext(ctx) {
		c.Error(errors.NewForbiddenError("only a system admin may run a cross-tenant sweep"))
		return
	}
	result, err := h.feeds.SyncDue(ctx)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": result})
}

// ListExternalEntries godoc
// @Summary      已采集条目
// @Description  列出订阅源已见过的条目，含每条匹配到的知识数与生成的信号数。
// @Tags         变更信号
// @Produce      json
// @Param        feed_id  query  string  false  "订阅源 ID"
// @Param        limit    query  int     false  "返回条数（默认 50）"
// @Success      200  {object}  map[string]interface{}  "{success, data: [ExternalSubscriptionEntry]}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /external-subscriptions/entries [get]
func (h *ExternalSubscriptionHandler) ListExternalEntries(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if err != nil || limit <= 0 || limit > 200 {
		limit = 50
	}
	entries, listErr := h.feeds.ListEntries(
		ctx, tenantID, strings.TrimSpace(c.Query("feed_id")), limit)
	if listErr != nil {
		c.Error(listErr)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": entries})
}

// toInput converts the request body into the service input.
func (r externalFeedRequest) toInput() types.ExternalSubscriptionFeedInput {
	return types.ExternalSubscriptionFeedInput{
		Name:               r.Name,
		FeedURL:            r.FeedURL,
		Format:             r.Format,
		DomainTags:         r.DomainTags,
		KnowledgeBaseIDs:   r.KnowledgeBaseIDs,
		Enabled:            r.Enabled,
		CheckIntervalHours: r.CheckIntervalHours,
		AuthHeader:         r.AuthHeader,
	}
}
