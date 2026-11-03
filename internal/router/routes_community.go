package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterCommunityRoutes wires the knowledge Q&A community
// (KnowAxis V3.0 §15.6).
//
// 权限矩阵：
//   - 读取问题与回答 —— 社区对所有登录用户开放：Viewer+。
//   - 提问 / 回答 / 投票 —— 参与内容生产：Contributor+。
//   - 采纳 —— 服务层校验"只有提问者可以采纳"，路由仍是 Contributor+。
//   - 转知识 / 举报处理 —— 治理动作：Admin+。
func RegisterCommunityRoutes(
	r *gin.RouterGroup, h *handler.CommunityHandler, g *rbacGuards,
) {
	if h == nil {
		return
	}
	questions := r.Group("/community/questions")
	{
		questions.GET("", g.Viewer(), h.ListQuestions)
		questions.GET("/:id", g.Viewer(), h.GetQuestion)
		questions.POST("", g.Contributor(), h.AskQuestion)
		questions.POST("/:id/answers", g.Contributor(), h.AnswerQuestion)
		questions.POST("/:id/accept", g.Contributor(), h.AcceptAnswer)
		questions.POST("/:id/convert", g.Admin(), h.ConvertQuestion)
	}
	r.POST("/community/answers/:answer_id/vote", g.Contributor(), h.VoteAnswer)
	reports := r.Group("/community/reports")
	{
		reports.GET("", g.Admin(), h.ListReports)
		reports.POST("", g.Contributor(), h.ReportContent)
		reports.PATCH("/:id", g.Admin(), h.ResolveReport)
	}
}
