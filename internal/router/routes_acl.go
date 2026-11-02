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
		// 移动前访问差异预览（V3.0 §2.3）：只读，但输出"谁将获得/失去访问"，
		// 按写权限而不是读权限把关——能执行移动的人才该看到这份差异。
		acl.GET("/move-preview", g.Contributor(), g.KBAccessWriteFromKnowledgeIDParam("id"), handler.PreviewKnowledgeMove)
	}
}
