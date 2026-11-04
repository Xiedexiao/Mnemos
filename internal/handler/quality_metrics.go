package handler

import (
	"strconv"

	"github.com/gin-gonic/gin"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// QualityMetricsHandler exposes the §14.7 quality operations metrics.
type QualityMetricsHandler struct {
	metrics interfaces.QualityMetricsService
}

// NewQualityMetricsHandler constructs the handler.
func NewQualityMetricsHandler(metrics interfaces.QualityMetricsService) *QualityMetricsHandler {
	return &QualityMetricsHandler{metrics: metrics}
}

// GetQualityMetrics godoc
// @Summary      质量运营指标
// @Description  及时性（源变更→更新时延、复审按时率、纠错关闭时长）、准确性（错误发布率、抽检不合格率、回答引用覆盖率）、价值（复用率、解决率、重复问题下降、缺口关闭率）三组指标（KnowAxis V3.0 §14.7）。每个比率都带分子/分母；分母为 0 时返回 null 而不是 0——"没有样本"与"全部失败"是两回事。
// @Tags         质量运营
// @Produce      json
// @Param        days  query  int  false  "窗口天数（默认 30，上限 365）"
// @Success      200  {object}  map[string]interface{}  "{success, data: QualityMetrics}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /quality/metrics [get]
func (h *QualityMetricsHandler) GetQualityMetrics(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	days, err := strconv.Atoi(c.DefaultQuery("days", "30"))
	if err != nil {
		days = 30
	}
	metrics, metricsErr := h.metrics.Get(ctx, tenantID, days)
	if metricsErr != nil {
		c.Error(metricsErr)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": metrics})
}
