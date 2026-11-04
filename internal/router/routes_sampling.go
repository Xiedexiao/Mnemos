package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterSamplingRoutes wires the §6.6 sampling inspections.
//
// 权限 = Admin+：抽检是"复核已发布内容"的治理动作，样本与结论都属于审核
// 质量数据，与审批工作台同档。
func RegisterSamplingRoutes(
	r *gin.RouterGroup, h *handler.SamplingHandler, g *rbacGuards,
) {
	if h == nil {
		return
	}
	sampling := r.Group("/sampling")
	{
		sampling.POST("/draw", g.Admin(), h.DrawSampling)
		sampling.GET("/inspections", g.Admin(), h.ListSampling)
		sampling.POST("/inspections/:id/decide", g.Admin(), h.DecideSampling)
		sampling.GET("/stats", g.Admin(), h.SamplingStats)
	}
}
