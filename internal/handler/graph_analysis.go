package handler

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// GraphAnalysisHandler exposes the §11.1 centrality / community / expert
// surfaces.
type GraphAnalysisHandler struct {
	analysis interfaces.GraphAnalysisService
}

// NewGraphAnalysisHandler constructs the handler.
func NewGraphAnalysisHandler(analysis interfaces.GraphAnalysisService) *GraphAnalysisHandler {
	return &GraphAnalysisHandler{analysis: analysis}
}

// GetCentrality godoc
// @Summary      知识中心度
// @Description  按加权 PageRank 排出被依赖最多的知识（重点维护清单，KnowAxis V3.0 §11.1）。权重来自生命周期：有效文档的依赖算整票，待复审/疑似过期算半票。只统计有权查看的文档，隐藏数量不对外暴露（§11.3 计数保护）。
// @Tags         图分析
// @Produce      json
// @Param        id     path   string  true   "知识库ID"
// @Param        limit  query  int     false  "节点上限（默认 500，上限 2000）"
// @Success      200  {object}  map[string]interface{}  "{success, data: CentralityReport}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge-bases/{id}/graph-analysis/centrality [get]
func (h *GraphAnalysisHandler) GetCentrality(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	limit, err := graphAnalysisLimitQuery(c)
	if err != nil {
		c.Error(err)
		return
	}
	report, reportErr := h.analysis.Centrality(ctx, tenantID, c.Param("id"), limit)
	if reportErr != nil {
		c.Error(reportErr)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": report})
}

// GetCommunities godoc
// @Summary      知识社区检测
// @Description  按关联图的连通分量划分知识领域，并列出没有任何关联的孤岛文档（KnowAxis V3.0 §11.1 社区检测）。用连通分量而非 Louvain：同一张图每次得到同样的划分，复盘时能解释"为什么这两块被划到一起"。
// @Tags         图分析
// @Produce      json
// @Param        id     path   string  true   "知识库ID"
// @Param        limit  query  int     false  "节点上限（默认 500，上限 2000）"
// @Success      200  {object}  map[string]interface{}  "{success, data: AnalysisCommunityReport}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge-bases/{id}/graph-analysis/communities [get]
func (h *GraphAnalysisHandler) GetCommunities(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	limit, err := graphAnalysisLimitQuery(c)
	if err != nil {
		c.Error(err)
		return
	}
	report, reportErr := h.analysis.Communities(ctx, tenantID, c.Param("id"), limit)
	if reportErr != nil {
		c.Error(reportErr)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": report})
}

// GetExperts godoc
// @Summary      领域专家发现
// @Description  按贡献、被采纳答案、有用评价与引用聚合出"这个领域谁最懂"（KnowAxis V3.0 §11.1 专家发现）。分数与权重随响应回显，可复核；未达最小证据量（3 项信号）的人不会出现在结果里——列出来本身就会泄露他参与过这个领域（§11.3）。
// @Tags         图分析
// @Produce      json
// @Param        id      path   string  true   "知识库ID"
// @Param        limit   query  int     false  "参与统计的文档上限（默认 500，上限 2000）"
// @Param        days    query  int     false  "信号窗口天数（默认 90，上限 365）"
// @Success      200  {object}  map[string]interface{}  "{success, data: ExpertReport}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge-bases/{id}/graph-analysis/experts [get]
func (h *GraphAnalysisHandler) GetExperts(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	limit, err := graphAnalysisLimitQuery(c)
	if err != nil {
		c.Error(err)
		return
	}
	days, err := graphAnalysisDaysQuery(c)
	if err != nil {
		c.Error(err)
		return
	}
	report, reportErr := h.analysis.Experts(ctx, tenantID, c.Param("id"), limit, days)
	if reportErr != nil {
		c.Error(reportErr)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": report})
}

// graphAnalysisLimitQuery parses the optional limit parameter.
func graphAnalysisLimitQuery(c *gin.Context) (int, error) {
	raw := strings.TrimSpace(c.Query("limit"))
	if raw == "" {
		return 0, nil
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return 0, apperrors.NewBadRequestError("limit must be a number")
	}
	if parsed < 0 {
		return 0, apperrors.NewBadRequestError("limit must not be negative")
	}
	return parsed, nil
}

// graphAnalysisDaysQuery parses the optional window parameter.
func graphAnalysisDaysQuery(c *gin.Context) (int, error) {
	raw := strings.TrimSpace(c.Query("days"))
	if raw == "" {
		return 0, nil
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return 0, apperrors.NewBadRequestError("days must be a number")
	}
	if parsed < 0 {
		return 0, apperrors.NewBadRequestError("days must not be negative")
	}
	return parsed, nil
}
