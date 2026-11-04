package handler

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// SamplingHandler exposes §6.6 sampling inspections.
type SamplingHandler struct {
	sampling interfaces.SamplingService
}

// NewSamplingHandler constructs the handler.
func NewSamplingHandler(sampling interfaces.SamplingService) *SamplingHandler {
	return &SamplingHandler{sampling: sampling}
}

// samplingDecisionRequest is the verdict body.
type samplingDecisionRequest struct {
	Status string `json:"status"`
	Note   string `json:"note"`
}

// DrawSampling godoc
// @Summary      执行抽检
// @Description  按期抽检已发布内容（KnowAxis V3.0 §6.6）：高风险 100%、中风险 10%、低风险 2%。选样按 (文档, 周期) 哈希确定性取样，同一期重跑得到同一批样本。
// @Tags         抽检
// @Accept       json
// @Produce      json
// @Param        period  query  string  false  "周期（默认当月，YYYY-MM）"
// @Param        limit   query  int     false  "候选上限（默认 2000）"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /sampling/draw [post]
func (h *SamplingHandler) DrawSampling(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "2000"))
	if err != nil || limit <= 0 {
		limit = 2000
	}
	result, drawErr := h.sampling.Draw(ctx, tenantID, strings.TrimSpace(c.Query("period")), limit)
	if drawErr != nil {
		c.Error(drawErr)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": result})
}

// ListSampling godoc
// @Summary      抽检清单
// @Description  列出抽检样本与结论。
// @Tags         抽检
// @Produce      json
// @Param        period  query  string  false  "周期"
// @Param        status  query  string  false  "pending/passed/failed/waived"
// @Param        limit   query  int     false  "条数（默认 100）"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /sampling/inspections [get]
func (h *SamplingHandler) ListSampling(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "100"))
	if err != nil || limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, listErr := h.sampling.List(ctx, tenantID,
		strings.TrimSpace(c.Query("period")),
		types.SamplingDecisionStatus(strings.TrimSpace(c.Query("status"))), limit)
	if listErr != nil {
		c.Error(listErr)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": rows})
}

// DecideSampling godoc
// @Summary      提交抽检结论
// @Description  记录抽检结论。不通过时自动开一张纠错任务（同一篇已有未关闭任务时复用）；不通过与豁免必须写明理由。
// @Tags         抽检
// @Accept       json
// @Produce      json
// @Param        id       path  string                  true  "抽检记录 ID"
// @Param        request  body  samplingDecisionRequest true  "结论"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /sampling/inspections/{id}/decide [post]
func (h *SamplingHandler) DecideSampling(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req samplingDecisionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(apperrors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	decided, err := h.sampling.Decide(ctx, tenantID, caller.UserID, types.SamplingDecisionInput{
		InspectionID: c.Param("id"),
		Status:       req.Status,
		Note:         req.Note,
	})
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": decided})
}

// SamplingStats godoc
// @Summary      抽检统计
// @Description  抽检通过率与按风险的分布。通过率只在已结案的样本上计算（passed+failed），pending 与 waived 不计入分母。
// @Tags         抽检
// @Produce      json
// @Param        period  query  string  false  "周期（默认全部）"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /sampling/stats [get]
func (h *SamplingHandler) SamplingStats(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	stats, err := h.sampling.Stats(ctx, tenantID, strings.TrimSpace(c.Query("period")))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": stats})
}
