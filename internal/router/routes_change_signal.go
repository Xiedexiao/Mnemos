package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterChangeSignalRoutes wires the §7.3 change-signal catalogue.
//
// 权限：信号队列是治理待办，转单会创建变更单，均为 Admin+ 且 JWT-only。
func RegisterChangeSignalRoutes(
	r *gin.RouterGroup, h *handler.ChangeSignalHandler, g *rbacGuards,
) {
	if h == nil {
		return
	}
	signals := r.Group("/change-signals")
	{
		signals.GET("", g.Admin(), h.ListChangeSignals)
		signals.POST("", g.Admin(), h.CollectChangeSignal)
		signals.POST("/:id/convert", g.Admin(), h.ConvertChangeSignal)
		signals.POST("/:id/dismiss", g.Admin(), h.DismissChangeSignal)
	}
}
