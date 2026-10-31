package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterKnowledgeHealthRoutes wires the knowledge-health dashboard
// endpoints (KnowAxis V3.0 §14.6 / §16.1 v1).
//
// Authorization: the dashboard is a read-only governance surface —
//   - GET: Viewer+ at the route floor; the tenant scope is a workspace-wide
//     governance view, so the handler re-checks Admin for scope=tenant
//     (query-param-dependent, same pattern as InitializationHandler);
//   - POST /compute: Admin+ (强制重算会追加快照行).
//
// JWT-only like the glossary/review governance routes: no API-key capability
// grants the health dashboard, so scoped keys are denied by default.
//
// /knowledge-health 与 /knowledge/:id 不冲突（gin 静态段优先匹配）。
//
// Assembly example (one line, wiring lives in internal/router/router.go):
//
//	RegisterKnowledgeHealthRoutes(v1, params.KnowledgeHealthHandler, rbacGuards)
func RegisterKnowledgeHealthRoutes(r *gin.RouterGroup, h *handler.KnowledgeHealthHandler, g *rbacGuards) {
	if h == nil {
		return
	}
	health := r.Group("/knowledge-health")
	{
		// 看板（读）= Viewer+；scope=tenant 的 Admin 门槛在 handler 内按
		// query 参数判定（依赖参数的角色门槛无法在路由层表达）。
		health.GET("", g.Viewer(), h.GetHealth)
		// 强制重算（写）= Admin+。
		health.POST("/compute", g.Admin(), h.ComputeHealth)
	}
}
