package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterGoldenSetRoutes wires the golden question set endpoints
// (KnowAxis V3.0 §12.5/§14.8). Writes and run triggers are Admin-only —
// 黄金问题集是发布门禁的基准面：改题、重跑评测、删集都直接改变"能不能
// 发布"的结论，且影响是租户级的（对照 rbac.go 的选守卫指引：租户级治理
// 面 → Admin）；reads（含门禁查询）是 Viewer+（只读面从宽）。JWT-only
// like glossary/ontology governance routes: no API-key capability grants
// golden-set governance, so scoped keys are denied by default.
//
// 路由参数统一 :id 风格（与既有 /knowledge/:id 等一致）；题目用 :qid 与
// 集合 :id 区分。
//
// Assembly example (wiring lives in internal/router/router.go — add the
// handler to RouterParams and call next to RegisterGlossaryRoutes):
//
//	RegisterGoldenSetRoutes(v1, params.GoldenSetHandler, rbacGuards)
func RegisterGoldenSetRoutes(r *gin.RouterGroup, h *handler.GoldenSetHandler, g *rbacGuards) {
	if h == nil {
		return
	}
	sets := r.Group("/golden-sets")
	{
		// 集合治理（写）= Admin+。
		sets.POST("", g.Admin(), h.CreateSet)
		sets.PUT("/:id", g.Admin(), h.UpdateSet)
		sets.DELETE("/:id", g.Admin(), h.DeleteSet)
		// 查询（读）= Viewer+。
		sets.GET("", g.Viewer(), h.ListSets)
		sets.GET("/:id", g.Viewer(), h.GetSet)

		// 题目：批量导入/增删改（写）= Admin+，读 = Viewer+。
		questions := sets.Group("/:id/questions")
		{
			questions.POST("", g.Admin(), h.ImportQuestions)
			questions.GET("", g.Viewer(), h.ListQuestions)
			questions.PUT("/:qid", g.Admin(), h.UpdateQuestion)
			questions.DELETE("/:qid", g.Admin(), h.DeleteQuestion)
		}

		// 评测：触发跑批（写）= Admin+，记录查询（读）= Viewer+。
		sets.POST("/:id/run", g.Admin(), h.RunEvaluation)
		sets.GET("/:id/runs", g.Viewer(), h.ListRuns)

		// 发布门禁查询（读）= Viewer+。
		sets.GET("/:id/gate", g.Viewer(), h.CheckGate)
	}
	// 评测记录按 ID 直查（跨集合的 run 详情页）。
	runs := r.Group("/golden-runs")
	{
		runs.GET("/:run_id", g.Viewer(), h.GetRun)
	}
}
