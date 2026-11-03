package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Tencent/WeKnora/internal/application/service"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

// ParseQualityHandler 暴露解析质量评分（KnowAxis V3.0 §3.5 / §9.4）：
//
//	GET  /knowledge/:id/parse-quality           — 单条评分（KB read 边界内）
//	POST /knowledge/:id/parse-quality/recompute — 立即重算并落库
//	GET  /parse-quality/queue?limit=N           — 解析异常队列（租户治理面）
//
// 租户/用户身份一律从 types.CallerFromContext 取（路由层 RBAC + KBAccess
// 守卫保证其可信）；on the knowledge routes the KB-access guard has already
// rewritten the context tenant to the effective (content-owner) workspace,
// so shared-KB reads/writes land on the right rows. 服务层哨兵错误经
// writeParseQualityError 映射 HTTP 语义。
type ParseQualityHandler struct {
	parseQualityService interfaces.ParseQualityService
}

// NewParseQualityHandler constructs the handler.
func NewParseQualityHandler(
	parseQualityService interfaces.ParseQualityService,
) *ParseQualityHandler {
	return &ParseQualityHandler{parseQualityService: parseQualityService}
}

// parseQualityResponse 是单条评分的响应体：分数行 + 维度构成（前端不必自己
// 持有 §3.5 权重表）。
type parseQualityResponse struct {
	*types.ParseQualityScore
	// Dimensions 六维度按权重表顺序的得分与加权贡献。
	Dimensions []types.ParseQualityDimensionScore `json:"dimensions"`
	// Threshold 当前判定的阈值（below_threshold 的分界），一并返回，让前端
	// 文案不写死 60。
	Threshold int `json:"threshold"`
}

// newParseQualityResponse wraps a score row with its dimension breakdown.
func newParseQualityResponse(score *types.ParseQualityScore) parseQualityResponse {
	return parseQualityResponse{
		ParseQualityScore: score,
		Dimensions:        score.DimensionScores(),
		Threshold:         types.ParseQualityThreshold,
	}
}

// GetParseQuality godoc
// @Summary      查询单条知识的解析质量分
// @Description  返回该知识的解析质量评分（§3.5 六维度 + 加权总分 + 档位 good/fair/poor）；未评分时 404，可调 recompute 现算。
// @Tags         解析质量
// @Produce      json
// @Param        id  path  string  true  "知识ID"  extensions(x-agent=knowledge_id)
// @Success      200  {object}  map[string]interface{}  "{success, data: {六维度, total_score, grade, below_threshold, dimensions, threshold}}"
// @Failure      400  {object}  apperrors.AppError  "缺少知识ID"
// @Failure      404  {object}  apperrors.AppError  "知识或评分不存在"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge/{id}/parse-quality [get]
func (h *ParseQualityHandler) GetParseQuality(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := parseQualityExecutionTenant(c, ctx)
	if !ok {
		return
	}
	knowledgeID := strings.TrimSpace(c.Param("id"))
	if knowledgeID == "" {
		c.Error(apperrors.NewBadRequestError("knowledge id is required"))
		return
	}
	score, err := h.parseQualityService.Get(ctx, tenantID, knowledgeID)
	if err != nil {
		writeParseQualityError(c, ctx, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": newParseQualityResponse(score)})
}

// RecomputeParseQuality godoc
// @Summary      重算单条知识的解析质量分
// @Description  立即按当前 chunks 与元数据重算解析质量分并落库（覆盖），返回完整的六维度构成与审计后的总分。
// @Tags         解析质量
// @Produce      json
// @Param        id  path  string  true  "知识ID"  extensions(x-agent=knowledge_id)
// @Success      200  {object}  map[string]interface{}  "{success, data: 评分行 + dimensions}"
// @Failure      400  {object}  apperrors.AppError  "缺少知识ID"
// @Failure      404  {object}  apperrors.AppError  "知识不存在"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge/{id}/parse-quality/recompute [post]
func (h *ParseQualityHandler) RecomputeParseQuality(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := parseQualityExecutionTenant(c, ctx)
	if !ok {
		return
	}
	knowledgeID := strings.TrimSpace(c.Param("id"))
	if knowledgeID == "" {
		c.Error(apperrors.NewBadRequestError("knowledge id is required"))
		return
	}
	score, err := h.parseQualityService.ComputeForKnowledge(
		ctx, tenantID, knowledgeID, types.CallerFromContext(ctx).UserID,
	)
	if err != nil {
		writeParseQualityError(c, ctx, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": newParseQualityResponse(score)})
}

// ListParseQualityQueue godoc
// @Summary      获取解析异常队列
// @Description  返回本租户解析质量低于阈值（§3.5，<60）的文档，按总分升序（最差在前），行内附知识标题/文件名/解析状态/错误信息。这些文档可搜但不可作高置信来源。
// @Tags         解析质量
// @Produce      json
// @Param        limit  query  int  false  "返回条数，1-200，默认 50"
// @Success      200  {object}  map[string]interface{}  "{success, data: {items, total, threshold}}"
// @Failure      401  {object}  apperrors.AppError  "缺少租户上下文"
// @Security     Bearer
// @Router       /parse-quality/queue [get]
func (h *ParseQualityHandler) ListParseQualityQueue(c *gin.Context) {
	ctx := c.Request.Context()
	// The queue is a tenant-wide governance surface (no :id, no KB guard):
	// the caller's own workspace is the right scope here — unlike the
	// per-knowledge routes above, nothing rewrites the execution tenant.
	caller := types.CallerFromContext(ctx)
	if caller.TenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is missing"))
		return
	}
	limit := 0
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			limit = v
		}
	}
	items, err := h.parseQualityService.ListBelowThreshold(ctx, caller.TenantID, limit)
	if err != nil {
		writeParseQualityError(c, ctx, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"items":     items,
			"total":     len(items),
			"threshold": types.ParseQualityThreshold,
		},
	})
}

// parseQualityExecutionTenant resolves the tenant that owns the knowledge the
// request addresses. The KB-access guard rewrites the request's execution
// tenant to the content's owner workspace (so an org-shared or
// agent-visible KB resolves correctly) while leaving the authenticated
// caller's identity untouched — score rows are keyed on the owner, so the
// execution tenant is the right scope here. Falls back to the caller's
// tenant for routes where no KB guard ran.
func parseQualityExecutionTenant(c *gin.Context, ctx context.Context) (uint64, bool) {
	if tenantID, ok := types.TenantIDFromContext(ctx); ok && tenantID != 0 {
		return tenantID, true
	}
	if tenantID := types.CallerFromContext(ctx).TenantID; tenantID != 0 {
		return tenantID, true
	}
	c.Error(apperrors.NewUnauthorizedError("tenant context is missing"))
	return 0, false
}

// writeParseQualityError maps the service sentinels onto HTTP statuses.
// Anything unrecognised is logged and surfaced as 500 with the reason, so a
// real defect is not masked as a client error.
func writeParseQualityError(c *gin.Context, ctx context.Context, err error) {
	switch {
	case errors.Is(err, service.ErrParseQualityTenantMissing):
		c.Error(apperrors.NewUnauthorizedError(err.Error()))
	case errors.Is(err, service.ErrParseQualityKnowledgeMissing):
		c.Error(apperrors.NewBadRequestError(err.Error()))
	case errors.Is(err, service.ErrParseQualityKnowledgeNotFound),
		errors.Is(err, service.ErrParseQualityNotFound):
		c.Error(apperrors.NewNotFoundError(err.Error()))
	default:
		logger.ErrorWithFields(ctx, err, map[string]interface{}{"handler": "parse_quality"})
		c.Error(apperrors.NewInternalServerError("parse quality operation failed").WithDetails(err.Error()))
	}
}
