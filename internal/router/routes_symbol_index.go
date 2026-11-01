package router

import (
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/gin-gonic/gin"
)

// RegisterSymbolIndexRoutes wires the exact-retrieval channel endpoints
// (KnowAxis V3.0 §12.7 精准索引 v1)。权限矩阵对齐父资源：建索引是内容
// 写（Contributor + KB write，:id 反查 KB，own / org-shared editor 一致，
// 与 share-links/lifecycle 同轴）；查询/命中是 Viewer+ + KB read 或
// 租户面 Viewer+。
//
// 路由参数统一 :id 风格（/knowledge/:id/* 的既有树钉死了该位置）。
//
// JWT-only（无 apiKeyGroup 声明，API key default-deny，见 rbac.go）：
// 精确通道当前只服务租户内控制台与聊天管道（管道走 service 直调，
// 不过 HTTP），暂无 API-key 能力映射。
//
// Assembly example (one line, wiring lives in internal/router/router.go):
//
//	RegisterSymbolIndexRoutes(v1, params.SymbolIndexHandler, rbacGuards)
func RegisterSymbolIndexRoutes(r *gin.RouterGroup, h *handler.SymbolIndexHandler, g *rbacGuards) {
	if h == nil {
		return
	}
	// 建索引（写）：Contributor+ 且对 :id 所属 KB 有 write 权限；
	// chunk_id 空则对整篇建文档级索引。
	r.POST("/knowledge/:id/symbols",
		g.Contributor(), g.KBAccessWriteFromKnowledgeIDParam("id"), h.IndexKnowledgeSymbols)
	// 查看某篇已索引符号（读）：Viewer+ 且对 :id 所属 KB 有 read 权限。
	r.GET("/knowledge/:id/symbols",
		g.Viewer(), g.KBAccessReadFromKnowledgeIDParam("id"), h.ListKnowledgeSymbols)
	// 精确命中（读）：Viewer+；kb_ids 逗号分隔、缺省全租户（服务层按
	// 租户隔离兜底）。
	r.GET("/symbol-lookup", g.Viewer(), h.SymbolLookup)
}
