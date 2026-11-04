package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterAnswerConversionRoutes wires the §13 answer→knowledge conversion.
//
// 权限：Contributor+ 且 JWT-only。
//   - 转换会创建候选卡片（一次治理写入），Viewer 不该能发起；
//   - 候选本身仍要过既有的审核与发布门禁，所以这里不需要 Admin——
//     转换只是"提交一条待审经验"，与手工建候选同一档；
//   - 不进 API key 面：答案里可能含只有提问者可见的内容，把它变成知识
//     属于内容治理动作，不适合交给第三方集成。
func RegisterAnswerConversionRoutes(
	r *gin.RouterGroup, h *handler.AnswerConversionHandler, g *rbacGuards,
) {
	if h == nil {
		return
	}
	r.POST("/knowledge-candidates/from-answer", g.Contributor(), h.ConvertAnswerToKnowledge)
}
