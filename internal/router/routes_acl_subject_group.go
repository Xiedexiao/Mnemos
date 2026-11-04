package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterACLSubjectGroupRoutes wires tenant-scoped ACL subject groups
// (KnowAxis V3.0 §2.1).
//
// 权限：组的成员变化会一次性改变一批人的文档访问（加入组即继承组上的全部
// 授权，退组即收权），属于治理面，统一 Admin+。
func RegisterACLSubjectGroupRoutes(
	r *gin.RouterGroup, h *handler.ACLSubjectGroupHandler, g *rbacGuards,
) {
	if h == nil {
		return
	}
	groups := r.Group("/acl/groups")
	{
		groups.GET("", g.Admin(), h.ListACLGroups)
		groups.POST("", g.Admin(), h.CreateACLGroup)
		groups.PUT("/:id", g.Admin(), h.UpdateACLGroup)
		groups.DELETE("/:id", g.Admin(), h.DeleteACLGroup)
		groups.GET("/:id/members", g.Admin(), h.ListACLGroupMembers)
		groups.POST("/:id/members", g.Admin(), h.AddACLGroupMembers)
		groups.DELETE("/:id/members/:user_id", g.Admin(), h.RemoveACLGroupMember)
	}
}
