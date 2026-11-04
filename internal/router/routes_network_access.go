package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterNetworkAccessRoutes wires the §16.3 IP allow-list configuration.
//
// 权限 = Owner：这条配置决定"谁能进入工作区"，一旦设错会让管理员自己也被
// 挡在外面，因此只交给最高权限角色，而不是 Admin。
func RegisterNetworkAccessRoutes(
	r *gin.RouterGroup, h *handler.NetworkAccessHandler, g *rbacGuards,
) {
	if h == nil {
		return
	}
	r.GET("/network-access", g.Owner(), h.GetNetworkAccessPolicy)
	r.PUT("/network-access", g.Owner(), h.UpdateNetworkAccessPolicy)
}
