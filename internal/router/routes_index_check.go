package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterIndexCheckRoutes wires the §17.1 index-coverage self-check.
//
// 权限 = Admin+ 且 JWT-only：报告会暴露表结构与查询模式（哪个索引缺失、
// 哪张表在扫全表），属于运维信息，不给普通读者与 API key。
func RegisterIndexCheckRoutes(
	r *gin.RouterGroup, h *handler.IndexCheckHandler, g *rbacGuards,
) {
	if h == nil {
		return
	}
	r.GET("/operations/index-check", g.Admin(), h.RunIndexCheck)
}
