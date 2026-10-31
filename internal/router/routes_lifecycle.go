package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterKnowledgeLifecycleRoutes wires the knowledge lifecycle endpoints
// (KnowAxis V3.0 §7.1): read-time resolved behaviour, published-version
// history and governance transitions.
//
// Mutating routes addressed via :knowledge_id inherit per-KB ownership from
// the owning knowledge entry's KB, same chain as RegisterChunkRoutes.
// Assembly (DI container): provide handler.NewKnowledgeLifecycleHandler and
// call this from NewRouter with params.KnowledgeLifecycleHandler.
func RegisterKnowledgeLifecycleRoutes(r *gin.RouterGroup, handler *handler.KnowledgeLifecycleHandler, g *rbacGuards) {
	lifecycle := g.apiKeyGroup(r.Group("/knowledge/:id/lifecycle"), apiKeyIngest(apiKeyFullAccess()))
	lifecycleRead := lifecycle.With(apiKeyRetrieve(apiKeyFullAccess()))
	{
		// 读取时解析出的生效行为（UI 徽章 / 问答提示的数据源）。
		lifecycleRead.GET("", g.Viewer(), g.KBAccessReadFromKnowledgeIDParam("knowledge_id"), handler.GetKnowledgeLifecycle)
		// 已发布版本快照（不可变，倒序）。
		lifecycleRead.GET("/versions", g.Viewer(), g.KBAccessReadFromKnowledgeIDParam("knowledge_id"), handler.ListKnowledgeVersions)
		// 生命周期动作：提交审核/发布/复审确认/标记过期/替代/冲突/下架/归档/恢复/治理配置。
		// 发布与恢复最终由审核流（A2）驱动；这里的直通入口同样写审计。
		lifecycle.POST("/transition", g.Contributor(), g.KBAccessWriteFromKnowledgeIDParam("knowledge_id"), handler.TransitionKnowledgeLifecycle)
	}
}
