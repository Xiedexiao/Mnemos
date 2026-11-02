package handler

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// SearchQualityHandler exposes search telemetry and the knowledge-gap list
// (KnowAxis V3.0 §12.5 搜索质量运营 + §14.4 知识缺口).
type SearchQualityHandler struct {
	quality interfaces.SearchQualityService
	gaps    interfaces.KnowledgeGapService
}

// NewSearchQualityHandler constructs the handler.
func NewSearchQualityHandler(
	quality interfaces.SearchQualityService,
	gaps interfaces.KnowledgeGapService,
) *SearchQualityHandler {
	return &SearchQualityHandler{quality: quality, gaps: gaps}
}

// windowDays parses the shared ?window_days= parameter.
func windowDays(c *gin.Context, fallback int) int {
	raw := c.Query("window_days")
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 || value > 365 {
		return fallback
	}
	return value
}

// GetSearchQuality godoc
// @Summary      搜索质量概览
// @Description  返回窗口内的查询量、零结果率、权限过滤量、点击率与平均耗时（KnowAxis V3.0 §12.5）。权限过滤量是聚合数字，用于运营判断"内容存在但看不到"，不会回传给检索用户。
// @Tags         搜索质量
// @Produce      json
// @Param        window_days  query  int  false  "统计窗口（天，默认 30，上限 365）"
// @Success      200  {object}  map[string]interface{}  "{success, data: SearchQualityOverview}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /search-quality [get]
func (h *SearchQualityHandler) GetSearchQuality(c *gin.Context) {
	tenantID, ok := types.TenantIDFromContext(c.Request.Context())
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	overview, err := h.quality.Overview(c.Request.Context(), tenantID, windowDays(c, 30))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": overview})
}

// GetFrequentQueries godoc
// @Summary      高频查询 / 高频零结果
// @Description  返回窗口内出现次数最多的查询词；zero_only=true 时只看返回空结果的查询，即"高频零结果"清单（§12.5）。
// @Tags         搜索质量
// @Produce      json
// @Param        window_days  query  int   false  "统计窗口（天，默认 30）"
// @Param        zero_only    query  bool  false  "只看零结果查询"
// @Param        limit        query  int   false  "返回条数（默认 20，上限 200）"
// @Success      200  {object}  map[string]interface{}  "{success, data: [FrequentQuery]}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /search-quality/frequent-queries [get]
func (h *SearchQualityHandler) GetFrequentQueries(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if err != nil || limit <= 0 || limit > 200 {
		limit = 20
	}
	zeroOnly, _ := strconv.ParseBool(c.DefaultQuery("zero_only", "false"))
	rows, listErr := h.quality.FrequentQueries(ctx, tenantID, windowDays(c, 30), zeroOnly, limit)
	if listErr != nil {
		c.Error(listErr)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": rows})
}

// MarkQueryClicked godoc
// @Summary      回填查询点击
// @Description  记录某次查询产生了点击（点击率指标的数据源，§12.5）。
// @Tags         搜索质量
// @Produce      json
// @Param        log_id  path  string  true  "查询日志ID"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /search-quality/logs/{log_id}/click [post]
func (h *SearchQualityHandler) MarkQueryClicked(c *gin.Context) {
	tenantID, ok := types.TenantIDFromContext(c.Request.Context())
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	if err := h.quality.MarkClicked(c.Request.Context(), tenantID, c.Param("log_id")); err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true})
}

// ListKnowledgeGaps godoc
// @Summary      知识缺口清单
// @Description  返回按出现次数聚合的缺口清单（§14.4）：高频搜索无结果、问答拒答等信号按归一化问题聚成一行的待办。
// @Tags         搜索质量
// @Produce      json
// @Param        status  query  string  false  "状态过滤（open|acknowledged|closed）"
// @Param        limit   query  int     false  "返回条数（默认 50，上限 200）"
// @Success      200  {object}  map[string]interface{}  "{success, data: [KnowledgeGap]}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge-gaps [get]
func (h *SearchQualityHandler) ListKnowledgeGaps(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	status := types.KnowledgeGapStatus(c.Query("status"))
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if err != nil || limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, listErr := h.gaps.List(ctx, tenantID, status, limit)
	if listErr != nil {
		c.Error(listErr)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": rows})
}

// updateGapRequest is the body of PATCH /knowledge-gaps/:gap_id.
type updateGapRequest struct {
	AssigneeID          string `json:"assignee_id"`
	Status              string `json:"status"`
	ResolvedKnowledgeID string `json:"resolved_knowledge_id"`
}

// UpdateKnowledgeGap godoc
// @Summary      处理知识缺口
// @Description  分派 Owner 或推进缺口状态（open→acknowledged→closed），关单时可关联补齐的知识（§14.4）。
// @Tags         搜索质量
// @Accept       json
// @Produce      json
// @Param        gap_id   path  string           true  "缺口ID"
// @Param        request  body  updateGapRequest true  "处置"
// @Success      200  {object}  map[string]interface{}  "{success, data: KnowledgeGap}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge-gaps/{gap_id} [patch]
func (h *SearchQualityHandler) UpdateKnowledgeGap(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req updateGapRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	gapID := c.Param("gap_id")
	if req.Status != "" {
		gap, err := h.gaps.UpdateStatus(ctx, tenantID, gapID,
			types.KnowledgeGapStatus(req.Status), req.ResolvedKnowledgeID)
		if err != nil {
			c.Error(err)
			return
		}
		c.JSON(200, gin.H{"success": true, "data": gap})
		return
	}
	gap, err := h.gaps.Assign(ctx, tenantID, gapID, req.AssigneeID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": gap})
}
