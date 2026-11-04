package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterWebhookRoutes wires the §16.4 outbound webhook management.
//
// 权限：订阅配置决定把哪些事件推给哪个外部地址，属于治理面 = Admin+。
// 投递记录含事件 payload 摘要，也一并限制。
func RegisterWebhookRoutes(
	r *gin.RouterGroup, h *handler.WebhookHandler, g *rbacGuards,
) {
	if h == nil {
		return
	}
	webhooks := r.Group("/webhooks")
	{
		webhooks.GET("/events", g.Admin(), h.ListWebhookEventTypes)
		webhooks.GET("/endpoints", g.Admin(), h.ListWebhookEndpoints)
		webhooks.POST("/endpoints", g.Admin(), h.CreateWebhookEndpoint)
		webhooks.PUT("/endpoints/:id", g.Admin(), h.UpdateWebhookEndpoint)
		webhooks.DELETE("/endpoints/:id", g.Admin(), h.DeleteWebhookEndpoint)
		webhooks.GET("/deliveries", g.Admin(), h.ListWebhookDeliveries)
		webhooks.POST("/deliveries/:id/replay", g.Admin(), h.ReplayWebhookDelivery)
	}
}
