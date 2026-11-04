package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterReadingRoutes wires §15.2 reading-page outline and progress.
//
// 权限：目录与正文同级（Viewer+，服务层再按 ACL 判定）；阅读进度是本人数据，
// 仓储按 (tenant, user) 过滤。
func RegisterReadingRoutes(
	r *gin.RouterGroup, h *handler.ReadingHandler, g *rbacGuards,
) {
	if h == nil {
		return
	}
	r.GET("/knowledge/:id/reading-toc", g.Viewer(), h.GetKnowledgeTOC)
	reading := r.Group("/reading")
	{
		reading.PUT("/progress", g.Viewer(), h.SaveReadingProgress)
		reading.GET("/progress", g.Viewer(), h.GetReadingProgress)
		reading.GET("/continue", g.Viewer(), h.ContinueReading)
	}
}
