package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterExternalSubscriptionRoutes wires the §7.3 external-subscription
// collector.
//
// 权限：订阅源配置决定了哪些知识会被卷进合规复审，跨租户 sweep 更是系统级
// 动作，所以统一 Admin+；sweep additionally允许系统管理员（调度器语义）。
func RegisterExternalSubscriptionRoutes(
	r *gin.RouterGroup, h *handler.ExternalSubscriptionHandler, g *rbacGuards,
) {
	if h == nil {
		return
	}
	feeds := r.Group("/external-subscriptions")
	{
		feeds.GET("/feeds", g.Admin(), h.ListExternalFeeds)
		feeds.POST("/feeds", g.Admin(), h.CreateExternalFeed)
		feeds.PUT("/feeds/:id", g.Admin(), h.UpdateExternalFeed)
		feeds.DELETE("/feeds/:id", g.Admin(), h.DeleteExternalFeed)
		feeds.POST("/feeds/:id/sync", g.Admin(), h.SyncExternalFeed)
		feeds.GET("/entries", g.Admin(), h.ListExternalEntries)
		feeds.POST("/sweep", g.AdminOrSystemAdmin(), h.SweepExternalFeeds)
	}
}
