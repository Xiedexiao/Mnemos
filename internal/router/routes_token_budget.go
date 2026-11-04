package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterTokenBudgetRoutes wires the §16.5 budget and model fallback chains.
//
// 权限 = Admin+ 且 JWT-only：预算与降级链决定"花多少钱"和"哪个模型回答
// 用户"，是租户级治理配置；API key 不开口子——机器身份不该改写成本与模型
// 路由策略。
func RegisterTokenBudgetRoutes(
	r *gin.RouterGroup, h *handler.TokenBudgetHandler, g *rbacGuards,
) {
	if h == nil {
		return
	}
	r.GET("/token-budget", g.Admin(), h.GetTokenBudget)
	r.PUT("/token-budget", g.Admin(), h.SaveTokenBudget)
	chains := r.Group("/model-fallback-chains")
	{
		chains.GET("", g.Admin(), h.ListFallbackChains)
		chains.PUT("", g.Admin(), h.SaveFallbackChain)
		chains.DELETE("/:scene", g.Admin(), h.DeleteFallbackChain)
	}
}
