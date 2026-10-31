package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterKnowledgeACLRoutes wires the per-document ACL endpoints
// (KnowAxis V3.0 §2.1). Admin floor = the authorize action's default ceiling;
// 不能自授 is enforced in the handler. Assembly: provide
// handler.NewKnowledgeACLHandler and call from NewRouter with
// params.KnowledgeACLHandler.
func RegisterKnowledgeACLRoutes(r *gin.RouterGroup, handler *handler.KnowledgeACLHandler, g *rbacGuards) {
	acl := g.apiKeyGroup(r.Group("/knowledge/:id/acl"), apiKeyIngest(apiKeyFullAccess()))
	{
		acl.POST("", g.Admin(), g.KBAccessWriteFromKnowledgeIDParam("id"), handler.GrantKnowledgeACL)
		acl.GET("", g.Contributor(), g.KBAccessReadFromKnowledgeIDParam("id"), handler.ListKnowledgeACL)
		acl.DELETE("/:acl_id", g.Admin(), g.KBAccessWriteFromKnowledgeIDParam("id"), handler.RevokeKnowledgeACL)
	}
}
