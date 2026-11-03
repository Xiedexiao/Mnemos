package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterRewardRoutes wires the §15.4 points-redemption and knowledge-star
// surfaces.
//
// 权限：
//   - 兑换目录与本人兑换单、公开表彰名单 = Viewer+（个人激励面）；
//   - 目录维护、全部兑换单、处理兑换、评选与公开 = Admin+（花的是组织福利
//     预算，且评选结果面向全租户）。
func RegisterRewardRoutes(
	r *gin.RouterGroup, h *handler.RewardHandler, g *rbacGuards,
) {
	if h == nil {
		return
	}
	stars := r.Group("/knowledge-stars")
	{
		stars.GET("", g.Viewer(), h.ListKnowledgeStars)
		stars.GET("/admin", g.Admin(), h.ListKnowledgeStarsAdmin)
		stars.GET("/candidates", g.Admin(), h.KnowledgeStarCandidates)
		stars.POST("", g.Admin(), h.SelectKnowledgeStars)
		stars.POST("/publish", g.Admin(), h.PublishKnowledgeStars)
	}

	rewards := r.Group("/rewards")
	{
		rewards.GET("/catalog", g.Viewer(), h.ListRewardCatalog)
		rewards.POST("/catalog", g.Admin(), h.CreateRewardItem)
		rewards.PUT("/catalog/:id", g.Admin(), h.UpdateRewardItem)
		rewards.POST("/catalog/:id/redeem", g.Viewer(), h.RedeemReward)
		rewards.GET("/redemptions", g.Viewer(), h.ListMyRedemptions)
		rewards.POST("/redemptions/:id/cancel", g.Viewer(), h.CancelRedemption)
		rewards.GET("/admin/redemptions", g.Admin(), h.ListAllRedemptions)
		rewards.POST("/admin/redemptions/:id/resolve", g.Admin(), h.ResolveRedemption)
	}
}
