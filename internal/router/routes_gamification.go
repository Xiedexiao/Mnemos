package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterGamificationRoutes wires the points/level and learning-path
// endpoints (KnowAxis V3.0 §15.4/§15.5 v1).
//
// 守卫矩阵（对照 rbac.go 的选守卫指引）：
//   - 学习路径是租户级 onboarding 面：写（创建/更新/删除）= Admin+（一条
//     路径面向全租户岗位，与 glossary 治理面同级）；读 = Viewer+（只读面
//     从宽）。
//   - /me/points 与排行榜是本人/租户只读面 = Viewer+。
//   - start / complete-step 是本人动作：路由压 Viewer 底线，且 handler 不从
//     请求取 user_id（主题恒为 caller），"本人"由构造保证而非校验。
//
// JWT-only，与 glossary/ontology 治理路由一致：API-key capability 体系尚无
// 积分/学习路径语义，未声明的路由对 scoped key 默认拒绝。
//
// 路由参数统一 :id 风格（与既有 /knowledge/:id 等一致）。
//
// Assembly example (one line, wiring lives in internal/router/router.go):
//
//	RegisterGamificationRoutes(v1, params.GamificationHandler, rbacGuards)
func RegisterGamificationRoutes(r *gin.RouterGroup, h *handler.GamificationHandler, g *rbacGuards) {
	if h == nil {
		return
	}
	points := r.Group("/me/points")
	{
		points.GET("", g.Viewer(), h.GetMyPoints)
		points.GET("/leaderboard", g.Viewer(), h.Leaderboard)
	}
	paths := r.Group("/learning-paths")
	{
		// 路径管理（写）= Admin+。
		paths.POST("", g.Admin(), h.CreatePath)
		paths.PUT("/:id", g.Admin(), h.UpdatePath)
		paths.DELETE("/:id", g.Admin(), h.DeletePath)
		// 查询（读）= Viewer+。
		paths.GET("", g.Viewer(), h.ListPaths)
		paths.GET("/:id", g.Viewer(), h.GetPath)
		// 本人动作（主题恒为 caller）。
		paths.POST("/:id/start", g.Viewer(), h.StartPath)
		paths.POST("/:id/complete-step", g.Viewer(), h.CompleteStep)
	}
}
