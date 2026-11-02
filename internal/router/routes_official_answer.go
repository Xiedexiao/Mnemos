package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterOfficialAnswerRoutes wires the official-answer lifecycle
// (KnowAxis V3.0 §13.7 官方答案与规则知识).
//
// 权限矩阵：
//   - GET /official-answers、/:id、/lookup —— 官方答案是对外可用的标准口径，
//     任何能问答的人都要能读：Viewer+。
//   - POST /official-answers（新建/编辑草稿）、/:id/submit —— 内容生产：
//     Contributor+，且 submit 由服务层校验"必须绑定证据"。
//   - POST /:id/approve、/publish、/retire —— 治理决策，直接决定用户拿到的
//     标准答案：Admin+。approve 另有服务层四眼校验（作者不能批自己）。
//
// 全部 JWT-only：高风险官方答案绑定审批回执，机器身份不该签发标准口径。
func RegisterOfficialAnswerRoutes(
	r *gin.RouterGroup, h *handler.OfficialAnswerHandler, g *rbacGuards,
) {
	if h == nil {
		return
	}
	answers := r.Group("/official-answers")
	{
		// lookup 必须注册在 /:id 之前：/lookup 是静态段，与通配段同层时
		// gin 会按注册顺序匹配。
		answers.GET("/lookup", g.Viewer(), h.LookupOfficialAnswer)
		answers.GET("", g.Viewer(), h.ListOfficialAnswers)
		answers.GET("/:id", g.Viewer(), h.GetOfficialAnswer)
		answers.POST("", g.Contributor(), h.SaveOfficialAnswer)
		answers.POST("/:id/submit", g.Contributor(), h.SubmitOfficialAnswerReview)
		answers.POST("/:id/approve", g.Admin(), h.ApproveOfficialAnswer)
		answers.POST("/:id/publish", g.Admin(), h.PublishOfficialAnswer)
		answers.POST("/:id/retire", g.Admin(), h.RetireOfficialAnswer)
	}
}
