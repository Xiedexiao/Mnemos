package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterGlossaryRoutes wires the controlled-vocabulary endpoints
// (KnowAxis V3.0 §4.4). Writes are Admin-only — the glossary is a
// tenant-wide governance surface, one editor's term redirect affects every
// retrieval in the workspace (对照 rbac.go 的选守卫指引：租户级治理面 → Admin)；
// reads and synonym expansion are Viewer+ (只读面从宽). JWT-only like
// ontology/review governance routes: no API-key capability grants term
// governance, so scoped keys are denied by default.
//
// 路由参数统一 :id 风格（与既有 /knowledge/:id 等一致）。
//
// Assembly example (one line, wiring lives in internal/router/router.go):
//
//	RegisterGlossaryRoutes(v1, params.GlossaryHandler, rbacGuards)
func RegisterGlossaryRoutes(r *gin.RouterGroup, h *handler.GlossaryHandler, g *rbacGuards) {
	if h == nil {
		return
	}
	terms := r.Group("/glossary/terms")
	{
		// 术语治理（写）= Admin+。
		terms.POST("", g.Admin(), h.CreateTerm)
		terms.PUT("/:id", g.Admin(), h.UpdateTerm)
		terms.DELETE("/:id", g.Admin(), h.DeleteTerm)
		terms.POST("/merge", g.Admin(), h.MergeTerms)
		// 查询/扩展（读）= Viewer+。
		terms.GET("", g.Viewer(), h.ListTerms)
		terms.GET("/:id", g.Viewer(), h.GetTerm)
	}
	expand := r.Group("/glossary")
	{
		expand.GET("/expand", g.Viewer(), h.ExpandSynonyms)
	}
}
