package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterFreshnessRoutes 装配新鲜度/复审调度路由（KnowAxis V3.0
// §7.7 待复审清单 + §7.9 新鲜度评分）。
//
// 装配方式（cmd/server 的 NewRouter 内，与其它 Register*Routes 并列）：
//
//	RegisterFreshnessRoutes(v1, params.FreshnessHandler, rbacGuards)
//
// 对应 RouterParams 需增加一行：FreshnessHandler *handler.FreshnessHandler。
//
// 权限矩阵（对照 rbac.go 的选守卫指引）：
//   - GET /knowledge/:id/freshness —— KB 内容读取边界：JWT Viewer+，
//     且对父 KB 有 read 权限（KBAccessReadFromKnowledgeIDParam）；scoped
//     API key 需 retrieve 能力（只读面，同 /knowledge-bases/:id 图片组）。
//   - GET /review/queue —— 租户治理/审计面：Admin+，且保持 JWT-only
//     （注册在裸 group 上、不经 apiKeyGroup 声明 → API key 默认拒绝；
//     复审队列是"人"的待办，与 /review/tasks 同一取舍）。
//
// 路由树：/knowledge 子树既有通配符统一为 :id（routes_knowledge.go），
// freshness 作为其静态子节点挂载无冲突；/review/queue 与既有
// /review/tasks、/review/receipts 为并列静态段，无冲突。
func RegisterFreshnessRoutes(r *gin.RouterGroup, h *handler.FreshnessHandler, g *rbacGuards) {
	if h == nil {
		return
	}

	// 单条新鲜度评分：只读组，scoped key 声明 retrieve 能力。
	fresh := g.apiKeyGroup(r.Group("/knowledge/:id/freshness"), apiKeyRetrieve(apiKeyFullAccess()))
	fresh.GET("", g.Viewer(), g.KBAccessReadFromKnowledgeIDParam("id"), h.GetKnowledgeFreshness)

	// 待复审清单：Admin+ 的治理面。
	reviewOps := r.Group("/review")
	reviewOps.GET("/queue", g.Admin(), h.ListReviewQueue)
}
