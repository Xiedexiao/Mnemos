package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterLearningTaskRoutes wires learning tasks and exams
// (KnowAxis V3.0 §15.5).
//
// 权限矩阵：
//   - GET /learning-tasks、POST /:id/complete —— 读/完成自己的任务：Viewer+
//     （服务层校验只能完成自己的）。
//   - POST /learning-tasks/assign —— 派发任务是治理动作：Admin+。
//   - 考试题库的增删与作答 —— 出题 Admin+，作答 Viewer+。
func RegisterLearningTaskRoutes(
	r *gin.RouterGroup, h *handler.LearningTaskHandler, g *rbacGuards,
) {
	if h == nil {
		return
	}
	tasks := r.Group("/learning-tasks")
	{
		tasks.GET("", g.Viewer(), h.ListLearningTasks)
		tasks.POST("/assign", g.Admin(), h.AssignLearningTasks)
		tasks.POST("/:id/complete", g.Viewer(), h.CompleteLearningTask)
	}
	exam := r.Group("/learning-paths/:path_id")
	{
		exam.GET("/exam-questions", g.Viewer(), h.ListExamQuestions)
		exam.POST("/exam-questions", g.Admin(), h.CreateExamQuestion)
		exam.POST("/exam", g.Viewer(), h.SubmitExam)
	}
}
