package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterKnowledgeVersionRoutes wires §7.4 diff / as-of / rollback.
//
// 权限：diff 与 as-of 是读面 = Viewer+；回滚会撤销一次已批准内容并产生新
// 版本，属于治理动作 = Admin+（与生命周期 publish 同档）。
func RegisterKnowledgeVersionRoutes(
	r *gin.RouterGroup, h *handler.KnowledgeVersionHandler, g *rbacGuards,
) {
	if h == nil {
		return
	}
	versions := r.Group("/knowledge/:id/versions")
	{
		versions.GET("/diff", g.Viewer(), h.DiffKnowledgeVersions)
		versions.GET("/as-of", g.Viewer(), h.AsOfKnowledgeVersion)
		versions.POST("/rollback", g.Admin(), h.RollbackKnowledgeVersion)
	}
}
