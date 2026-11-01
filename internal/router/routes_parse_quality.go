package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterParseQualityRoutes 装配解析质量评分路由（KnowAxis V3.0 §3.5 / §9.4）：
//
//	GET  /knowledge/:id/parse-quality           — 单条评分（读）
//	POST /knowledge/:id/parse-quality/recompute — 立即重算并落库（写）
//	GET  /parse-quality/queue?limit=N           — 解析异常队列（租户治理面）
//
// 装配方式（cmd/server 的 NewRouter 内，与其它 Register*Routes 并列，一行）：
//
//	RegisterParseQualityRoutes(v1, params.ParseQualityHandler, rbacGuards)
//
// 对应 RouterParams 需增加一行：ParseQualityHandler *handler.ParseQualityHandler。
//
// 权限矩阵（对照 rbac.go 的选守卫指引，与 freshness / lifecycle 同风格）：
//   - GET /knowledge/:id/parse-quality —— KB 内容读取边界：JWT Viewer+，
//     且对父 KB 有 read 权限（KBAccessReadFromKnowledgeIDParam 把共享 KB 的
//     执行租户改写成内容所属工作区，handler 读到的租户即评分行的租户）。
//   - POST /knowledge/:id/parse-quality/recompute —— 会重写评分行（写）：JWT
//     Contributor+，且对父 KB 有 write 权限（KBAccessWriteFromKnowledgeIDParam）。
//   - GET /parse-quality/queue —— 租户级治理待办（列出全租户低质文档）：
//     Admin+。
//
// 全部 JWT-only：注册在裸 group 上、不经 apiKeyGroup 声明 → API key 默认拒绝
// （评分与解析异常队列是治理面，不是检索内容；与 /review、/knowledge-health
// 同一取舍）。
//
// 路由树：/knowledge 子树既有通配符统一为 :id（routes_knowledge.go），
// parse-quality 作为其静态子节点挂载无冲突（与 /knowledge/:id/freshness
// 同样的形态）；/parse-quality 是新的顶层静态段，与其它段无冲突。
func RegisterParseQualityRoutes(
	r *gin.RouterGroup, h *handler.ParseQualityHandler, g *rbacGuards,
) {
	if h == nil {
		return
	}

	// 单条评分：读 / 重算。:id 是 knowledge id，守卫经它回溯父 KB。
	pq := r.Group("/knowledge/:id/parse-quality")
	pq.GET("", g.Viewer(), g.KBAccessReadFromKnowledgeIDParam("id"), h.GetParseQuality)
	pq.POST("/recompute", g.Contributor(),
		g.KBAccessWriteFromKnowledgeIDParam("id"), h.RecomputeParseQuality)

	// 解析异常队列：租户治理面，Admin+。
	queue := r.Group("/parse-quality")
	queue.GET("/queue", g.Admin(), h.ListParseQualityQueue)
}
