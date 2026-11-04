package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterKnowledgeMapRoutes wires §4.6 knowledge maps and §15.5 role maps.
//
// 权限：两者都是读面，Viewer+ 即可。地图的服务层已经按调用者的全部主体
// （user/role/group）过滤 ACL，并在无权限结论时返回空图，因此路由层不需要
// 额外的租户级门禁——加 Admin 反而会让普通读者看不到自己有权看的地图。
func RegisterKnowledgeMapRoutes(
	r *gin.RouterGroup, h *handler.KnowledgeMapHandler, g *rbacGuards,
) {
	if h == nil {
		return
	}
	r.GET("/knowledge-bases/:id/knowledge-map", g.Viewer(), h.GetSpaceKnowledgeMap)
	r.GET("/discovery/role-map", g.Viewer(), h.GetRoleKnowledgeMap)
}
