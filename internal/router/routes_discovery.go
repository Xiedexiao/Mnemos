package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterDiscoveryRoutes wires §15.3 discovery and follow subscriptions.
//
// 权限：推荐、浏览历史、订阅与摘要都是"本人"的读/写，身份恒为调用者，
// Viewer+ 即可。推荐位与摘要都在服务层做权限过滤，路由层不需要额外门禁。
func RegisterDiscoveryRoutes(
	r *gin.RouterGroup, h *handler.DiscoveryHandler, g *rbacGuards,
) {
	if h == nil {
		return
	}
	discovery := r.Group("/discovery")
	{
		discovery.GET("/recommendations", g.Viewer(), h.GetHomeRecommendations)
		discovery.POST("/views", g.Viewer(), h.RecordKnowledgeView)
		discovery.GET("/history", g.Viewer(), h.GetViewHistory)
		discovery.POST("/subscriptions", g.Viewer(), h.Subscribe)
		discovery.GET("/subscriptions", g.Viewer(), h.ListSubscriptions)
		discovery.PATCH("/subscriptions/:id", g.Viewer(), h.SetSubscription)
		discovery.DELETE("/subscriptions/:id", g.Viewer(), h.Unsubscribe)
		discovery.GET("/digest", g.Viewer(), h.GetDigest)
	}
	r.GET("/knowledge/:id/recommendations", g.Viewer(), h.GetKnowledgeRecommendations)
}
