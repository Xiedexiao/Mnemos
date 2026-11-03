package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterOperationsDashboardRoutes wires the §16.1 operations dashboard.
//
// 权限：看板汇总了全租户的知识规模、审核积压与成本相关计数，属于管理面，
// 因此 Admin+ 且 JWT-only（与 /review、/knowledge-health 同一取舍）。
func RegisterOperationsDashboardRoutes(
	r *gin.RouterGroup, h *handler.OperationsDashboardHandler, g *rbacGuards,
) {
	if h == nil {
		return
	}
	r.GET("/operations/dashboard", g.Admin(), h.GetOperationsDashboard)
}
