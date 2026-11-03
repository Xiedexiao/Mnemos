package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterEmbedPushRoutes wires the §15.5 embedded contextual push.
//
// 权限：
//   - resolve / preferences / events 是"本人 + 业务系统上下文"的读接口，
//     身份恒为调用者，Viewer+；
//   - 规则维护与效果统计是 Admin+（规则决定全租户工位上看到什么）。
func RegisterEmbedPushRoutes(
	r *gin.RouterGroup, h *handler.EmbedPushHandler, g *rbacGuards,
) {
	if h == nil {
		return
	}
	push := r.Group("/embed/push")
	{
		push.POST("/resolve", g.Viewer(), h.ResolveEmbedPush)
		push.GET("/preferences", g.Viewer(), h.GetEmbedPushPreference)
		push.PUT("/preferences", g.Viewer(), h.SetEmbedPushPreference)
		push.POST("/events", g.Viewer(), h.RecordEmbedPushEvent)
		push.GET("/rules", g.Admin(), h.ListEmbedPushRules)
		push.POST("/rules", g.Admin(), h.CreateEmbedPushRule)
		push.PUT("/rules/:id", g.Admin(), h.UpdateEmbedPushRule)
		push.DELETE("/rules/:id", g.Admin(), h.DeleteEmbedPushRule)
		push.GET("/stats", g.Admin(), h.EmbedPushRuleStats)
	}
}
