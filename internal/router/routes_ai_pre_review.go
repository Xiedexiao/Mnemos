package router

import (
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/gin-gonic/gin"
)

// RegisterAIPreReviewRoutes 装配 AI 预审路由（KnowAxis V3.0 §6.5）。
//
// 装配方式（cmd/server 的 NewRouter 内，与其它 Register*Routes 并列，一行）：
//
//	RegisterAIPreReviewRoutes(v1, rbacGuards, params.AIPreReviewHandler)
//
// 依赖装配（container.go，与 review/glossary 并列）：
//
//	must(container.Provide(repository.NewAIPreReviewRepository))
//	must(container.Provide(func(
//	    runs interfaces.AIPreReviewRepository,
//	    tasks interfaces.ApprovalTaskRepository,
//	    knowledges interfaces.KnowledgeRepository,
//	    kbs interfaces.KnowledgeBaseRepository,
//	    chunks interfaces.ChunkRepository,
//	    audit interfaces.AuditLogService,
//	    glossary interfaces.GlossaryService,
//	    dlp interfaces.DLPService,
//	    models interfaces.ModelService,
//	) interfaces.AIPreReviewService {
//	    return service.NewAIPreReviewService(
//	        runs, tasks, knowledges, kbs, chunks, audit, glossary, dlp, models)
//	}))
//	must(container.Provide(handler.NewAIPreReviewHandler))
//
// 路由：
//   - POST /review/tasks/:task_id/pre-review  触发一次预审（Admin+）
//   - GET  /review/tasks/:task_id/pre-review  读历史运行（Admin+）
//
// 权限矩阵（对照 rbac.go 的选守卫指引）：
//   - 两者都挂在与 /review/tasks/:task_id/decide 同一组治理面上：Admin+，
//     且保持 JWT-only（注册在裸 group 上、不经 apiKeyGroup 声明 → API key
//     默认拒绝）。预审是"人看清单"的前置步骤，与审批同档；用 API key 替
//     人做预审没有意义，反而会绕开审批留痕。
//
// 路由树安全性：:task_id 与既有 POST /review/tasks/:task_id/decide 使用同一
// 通配符名，gin 在同一位置的通配符名一致，不会 panic；GET 侧 /review 子树
// 此前只有静态 GET /review/tasks、/review/queue、/review/receipts[/:receipt_id]，
// 新增的 /review/tasks/:task_id/pre-review 是"静态 tasks + 通配 + 静态
// pre-review"，与 GET /review/tasks 不冲突（后者是完整静态段）。
func RegisterAIPreReviewRoutes(r *gin.RouterGroup, g *rbacGuards, h *handler.AIPreReviewHandler) {
	if h == nil {
		return
	}

	reviewOps := r.Group("/review")
	{
		reviewOps.POST("/tasks/:task_id/pre-review", g.Admin(), h.RunPreReview)
		reviewOps.GET("/tasks/:task_id/pre-review", g.Admin(), h.ListRuns)
	}
}
