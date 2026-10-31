package router

import (
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/gin-gonic/gin"
)

// RegisterEvidenceRoutes 装配 Evidence Pack 查询路由（KnowAxis V3.0 §13.4
// 的 v1 子集）。写入不经过 HTTP：证据包由聊天管道在答案定稿后落行
// （Save 挂钩点建议见 routes_evidence.go 尾部注释）。
//
// 装配方式（cmd/server 的 NewRouter 内，与其它 Register*Routes 并列，一行示例）：
//
//	evidenceSvc := service.NewEvidencePackService(repository.NewEvidencePackRepository(db))
//	router.RegisterEvidenceRoutes(apiV1, guards, handler.NewEvidencePackHandler(evidenceSvc))
//
// 权限矩阵：两个读取端点都是 Viewer+ 且 JWT-only —— 注册在裸 group 上、
// 不经 apiKeyGroup 声明，API key 默认拒绝（见 rbac.go 的 default-deny
// 说明）。证据包是审计/回放数据，按"人"授权，与 /review 治理面同一取舍；
// 服务层按租户隔离，会话内本人可见性与现有 /sessions 路由口径一致。
//
// 注意：gin 的路由树在同一位置只允许一个通配符名，/sessions 的 GET 路由
// 统一用 :id（routes_chat.go），因此会话时间线路由复用 :id（handler 内
// 双读 :session_id/:id 兼容两种挂载，对外路径仍是
// /sessions/:session_id/evidence-packs）。
func RegisterEvidenceRoutes(r *gin.RouterGroup, g *rbacGuards, h *handler.EvidencePackHandler) {
	if h == nil {
		return
	}

	packs := r.Group("/evidence-packs")
	packs.GET("/:request_id", g.Viewer(), h.GetByRequestID)

	// 复用 /sessions GET 树的 :id 通配符，避免 wildcard conflict panic。
	sessions := r.Group("/sessions")
	sessions.GET("/:id/evidence-packs", g.Viewer(), h.ListBySession)
}
