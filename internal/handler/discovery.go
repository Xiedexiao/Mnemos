package handler

import (
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// DiscoveryHandler exposes §15.3 discovery (recommendations, history) and
// follow subscriptions.
type DiscoveryHandler struct {
	recommendations interfaces.RecommendationService
	subscriptions   interfaces.SubscriptionService
}

// NewDiscoveryHandler constructs the handler.
func NewDiscoveryHandler(
	recommendations interfaces.RecommendationService,
	subscriptions interfaces.SubscriptionService,
) *DiscoveryHandler {
	return &DiscoveryHandler{recommendations: recommendations, subscriptions: subscriptions}
}

// recordViewRequest is the body of POST /discovery/views.
type recordViewRequest struct {
	KnowledgeID string `json:"knowledge_id"`
}

// subscribeRequest is the body of POST /discovery/subscriptions.
type subscribeRequest struct {
	SubjectType string `json:"subject_type"`
	SubjectID   string `json:"subject_id"`
	SpaceID     string `json:"space_id"`
	Label       string `json:"label"`
}

// setSubscriptionRequest is the body of PATCH /discovery/subscriptions/:id.
type setSubscriptionRequest struct {
	Enabled *bool `json:"enabled"`
}

// GetKnowledgeRecommendations godoc
// @Summary      相关知识 / 猜你想学 / 最新 / 热门
// @Description  返回一篇知识的推荐看板（KnowAxis V3.0 §15.3）：相关知识、猜你想学、最新动态与热门知识。每一条都已经过生命周期、密级与文档 ACL 过滤——推荐位不会泄露无权内容的存在。
// @Tags         发现
// @Produce      json
// @Param        id     path   string  true   "知识ID"
// @Param        limit  query  int     false  "每类条数（默认 5，上限 20）"
// @Success      200  {object}  map[string]interface{}  "{success, data: RecommendationBoard}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge/{id}/recommendations [get]
func (h *DiscoveryHandler) GetKnowledgeRecommendations(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	board, err := h.recommendations.Board(ctx, tenantID, c.Param("id"), parseDiscoveryLimit(c))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": board})
}

// GetHomeRecommendations godoc
// @Summary      首页推荐
// @Description  个人首页的猜你想学、最新动态与热门知识（KnowAxis V3.0 §15.3）。
// @Tags         发现
// @Produce      json
// @Param        limit  query  int  false  "每类条数（默认 5，上限 20）"
// @Success      200  {object}  map[string]interface{}  "{success, data: RecommendationBoard}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /discovery/recommendations [get]
func (h *DiscoveryHandler) GetHomeRecommendations(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	caller := types.CallerFromContext(ctx)
	board, err := h.recommendations.Home(ctx, tenantID, caller.UserID, parseDiscoveryLimit(c))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": board})
}

// RecordKnowledgeView godoc
// @Summary      记录一次阅读
// @Description  记录阅读事件（用于浏览历史与热门统计，KnowAxis V3.0 §15.3/§16.1）。同一篇 30 分钟内重复提交只记一次。
// @Tags         发现
// @Accept       json
// @Produce      json
// @Param        request  body  recordViewRequest  true  "阅读"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /discovery/views [post]
func (h *DiscoveryHandler) RecordKnowledgeView(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req recordViewRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	if err := h.recommendations.RecordView(ctx, tenantID, caller.UserID, req.KnowledgeID); err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true})
}

// GetViewHistory godoc
// @Summary      浏览历史
// @Description  本人的浏览历史（后端版，跨设备，KnowAxis V3.0 §15.3）。无权访问的条目只保留占位而不显示标题。
// @Tags         发现
// @Produce      json
// @Param        limit  query  int  false  "条数（默认 50）"
// @Success      200  {object}  map[string]interface{}  "{success, data: [ViewHistoryItem]}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /discovery/history [get]
func (h *DiscoveryHandler) GetViewHistory(c *gin.Context) {
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
	caller := types.CallerFromContext(ctx)
	items, listErr := h.recommendations.History(ctx, tenantID, caller.UserID, limit)
	if listErr != nil {
		c.Error(listErr)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": items})
}

// Subscribe godoc
// @Summary      订阅
// @Description  订阅空间、目录、单篇知识、标签或作者（KnowAxis V3.0 §15.3 我的关注）。订阅不授予访问权，摘要会逐条过权限。
// @Tags         发现
// @Accept       json
// @Produce      json
// @Param        request  body  subscribeRequest  true  "订阅"
// @Success      200  {object}  map[string]interface{}  "{success, data: Subscription}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /discovery/subscriptions [post]
func (h *DiscoveryHandler) Subscribe(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req subscribeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	subscription, err := h.subscriptions.Subscribe(ctx, tenantID, caller.UserID,
		types.SubscriptionInput{
			SubjectType: req.SubjectType,
			SubjectID:   req.SubjectID,
			SpaceID:     req.SpaceID,
			Label:       req.Label,
		})
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": subscription})
}

// ListSubscriptions godoc
// @Summary      我的订阅
// @Description  列出本人的订阅。
// @Tags         发现
// @Produce      json
// @Param        enabled_only  query  bool  false  "只看启用的"
// @Success      200  {object}  map[string]interface{}  "{success, data: [Subscription]}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /discovery/subscriptions [get]
func (h *DiscoveryHandler) ListSubscriptions(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	enabledOnly := c.Query("enabled_only") == "true" || c.Query("enabled_only") == "1"
	caller := types.CallerFromContext(ctx)
	subs, err := h.subscriptions.List(ctx, tenantID, caller.UserID, enabledOnly)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": subs})
}

// SetSubscription godoc
// @Summary      暂停/恢复订阅
// @Description  暂停后不再进入摘要，订阅本身保留。
// @Tags         发现
// @Accept       json
// @Produce      json
// @Param        id       path  string                 true  "订阅 ID"
// @Param        request  body  setSubscriptionRequest true  "开关"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /discovery/subscriptions/{id} [patch]
func (h *DiscoveryHandler) SetSubscription(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req setSubscriptionRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Enabled == nil {
		c.Error(errors.NewBadRequestError("enabled is required"))
		return
	}
	caller := types.CallerFromContext(ctx)
	if err := h.subscriptions.SetEnabled(ctx, tenantID, caller.UserID, c.Param("id"), *req.Enabled); err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true})
}

// Unsubscribe godoc
// @Summary      取消订阅
// @Description  删除一条订阅。
// @Tags         发现
// @Produce      json
// @Param        id  path  string  true  "订阅 ID"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /discovery/subscriptions/{id} [delete]
func (h *DiscoveryHandler) Unsubscribe(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	caller := types.CallerFromContext(ctx)
	if err := h.subscriptions.Unsubscribe(ctx, tenantID, caller.UserID, c.Param("id")); err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true})
}

// GetDigest godoc
// @Summary      订阅摘要
// @Description  我的关注摘要（KnowAxis V3.0 §15.3）：窗口内被订阅对象下发生变更的知识。每条都经过权限过滤，无权条目只计入隐藏数量。
// @Tags         发现
// @Produce      json
// @Param        since  query  string  false  "起始时间（RFC3339，缺省最近 7 天）"
// @Param        limit  query  int     false  "条数（默认 100）"
// @Success      200  {object}  map[string]interface{}  "{success, data: Digest}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /discovery/digest [get]
func (h *DiscoveryHandler) GetDigest(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var since time.Time
	if raw := strings.TrimSpace(c.Query("since")); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			c.Error(errors.NewBadRequestError("since must be RFC3339"))
			return
		}
		since = parsed
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "100"))
	if err != nil || limit <= 0 || limit > types.DigestMaxItems {
		limit = types.DigestMaxItems
	}
	caller := types.CallerFromContext(ctx)
	digest, digestErr := h.subscriptions.Digest(ctx, tenantID, caller.UserID, since, limit)
	if digestErr != nil {
		c.Error(digestErr)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": digest})
}

// parseDiscoveryLimit parses the shared per-list limit.
func parseDiscoveryLimit(c *gin.Context) int {
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "5"))
	if err != nil {
		return types.DefaultRecommendationLimit
	}
	return types.NormalizeRecommendationLimit(limit)
}
