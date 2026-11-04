package handler

import (
	"context"
	"errors"
	"net/http"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// FreshnessHandler 暴露 KnowAxis V3.0 §7.9 新鲜度评分与 §7.7 待复审
// 清单：
//   - GET /knowledge/:id/freshness —— 单条评分（KB read 边界内）；
//   - GET /review/queue          —— 待复审清单（租户治理面，Admin+）。
//
// 租户/用户身份一律从 types.CallerFromContext 取（路由层 RBAC + KBAccess
// 守卫保证其可信），与 ReviewHandler 同策略。
type FreshnessHandler struct {
	freshnessService interfaces.FreshnessService
}

// NewFreshnessHandler constructs the handler.
func NewFreshnessHandler(
	freshnessService interfaces.FreshnessService,
) *FreshnessHandler {
	return &FreshnessHandler{freshnessService: freshnessService}
}

// GetKnowledgeFreshness godoc
// @Summary      查询知识新鲜度评分
// @Description  返回单条知识的新鲜度评分（KnowAxis V3.0 §7.9）：加权总分（V1 可达上限 70）、档位（good/aging/stale/critical）与可行动的丢分信号。
// @Tags         知识生命周期
// @Produce      json
// @Param        id  path  string  true  "知识ID"  extensions(x-agent=knowledge_id)
// @Success      200  {object}  handler.DataResponse{data=types.FreshnessScore}  "{success, knowledge_id, data: {total, grade, top_signals}}"
// @Failure      404  {object}  apperrors.AppError  "知识不存在"
// @Security     Bearer
// @Security     ApiKeyAuth
// @x-agent      {"id":"knowledge.freshness","risk":"read"}
// @x-agent-hint "Scores how current one document is: a 0-100 total, a grade (good/aging/stale/critical) and the top reasons it lost points. Use it before citing an old document or when asked whether content is still valid."
// @Router       /knowledge/{id}/freshness [get]
func (h *FreshnessHandler) GetKnowledgeFreshness(c *gin.Context) {
	ctx := c.Request.Context()
	knowledgeID := c.Param("id")
	if knowledgeID == "" {
		c.Error(apperrors.NewBadRequestError("knowledge id is required"))
		return
	}
	score, err := h.freshnessService.ScoreFor(ctx, knowledgeID)
	if err != nil {
		writeFreshnessError(c, ctx, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success":      true,
		"knowledge_id": knowledgeID,
		"data":         score,
	})
}

// ListReviewQueue godoc
// @Summary      获取待复审清单
// @Description  返回本租户待复审知识（KnowAxis V3.0 §7.7）：已处于 pending_review_cycle 的条目 + 尚为 valid 但复审到期或显式有效期已过的条目。按复审基线时间升序（最久未复审在前），分页；行内附新鲜度评分与入队原因。
// @Tags         知识治理
// @Produce      json
// @Param        page        query  int  false  "页码，默认 1"
// @Param        page_size   query  int  false  "每页条数，1-100，默认 20"
// @Success      200  {object}  map[string]interface{}  "{success, data: PageResult{total, page, page_size, data: [FreshnessReviewQueueItem]}}"
// @Failure      401  {object}  apperrors.AppError  "缺少租户上下文"
// @Security     Bearer
// @Router       /review/queue [get]
func (h *FreshnessHandler) ListReviewQueue(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	if caller.TenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is missing"))
		return
	}
	page, pageSize, ok := parseListPagination(c)
	if !ok {
		return // 校验错误已挂到 context
	}

	result, err := h.freshnessService.ReviewQueue(ctx, &types.Pagination{
		Page:     page,
		PageSize: pageSize,
	})
	if err != nil {
		writeFreshnessError(c, ctx, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": result})
}

// writeFreshnessError 把新鲜度/复审清单的业务错误映射为 HTTP 语义：
// 知识缺失 → 404；服务层已带语义的 AppError（如缺租户上下文 → 401）原样
// 透传；其余记日志后 500（不外泄内部细节）。
func writeFreshnessError(c *gin.Context, ctx context.Context, err error) {
	var appErr *apperrors.AppError
	switch {
	case errors.Is(err, apprepo.ErrKnowledgeNotFound), errors.Is(err, gorm.ErrRecordNotFound):
		c.Error(apperrors.NewNotFoundError("knowledge not found"))
	case errors.As(err, &appErr):
		c.Error(err)
	default:
		logger.ErrorWithFields(ctx, err, map[string]interface{}{"handler": "freshness"})
		c.Error(apperrors.NewInternalServerError("freshness operation failed").WithDetails(err.Error()))
	}
}
