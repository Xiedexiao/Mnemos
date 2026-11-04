package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterQualityMetricsRoutes wires the §14.7 quality operations metrics.
//
// 权限 = Admin+：指标跨全租户聚合，且含错误发布率这类治理结论。
func RegisterQualityMetricsRoutes(
	r *gin.RouterGroup, h *handler.QualityMetricsHandler, g *rbacGuards,
) {
	if h == nil {
		return
	}
	r.GET("/quality/metrics", g.Admin(), h.GetQualityMetrics)
}
