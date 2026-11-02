package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterModelGatewayRoutes wires the model gateway endpoints
// (KnowAxis V3.0 §16.5). Routing rules and the usage summary are tenant
// governance / cost surfaces → Admin+（对照 rbac.go 的选守卫指引：改路由
// 影响整个工作区的模型选择，用量是成本数据）; prompt version reads are
// Viewer+ (只读面从宽,模板内容对构建答案的人有用), new versions Admin+.
// Platform-level rules (tenant 0) additionally require a system
// administrator, enforced in the service layer. JWT-only like the other
// governance routes: no API-key capability grants model-gateway writes, so
// scoped keys are denied by default.
//
// 路由参数统一 :id/:key 风格（与既有 /knowledge/:id 等一致）。
//
// Not in this vertical (预留，见 types/model_gateway.go 尾注释): 调用链强制
// 路由（主会话接 GetChatModel）、成本限额、注入防护。
//
// Assembly example (wiring lives in internal/router/router.go):
//
//	RegisterModelGatewayRoutes(v1, params.ModelGatewayHandler, rbacGuards)
func RegisterModelGatewayRoutes(r *gin.RouterGroup, h *handler.ModelGatewayHandler, g *rbacGuards) {
	if h == nil {
		return
	}
	rules := r.Group("/model-gateway/routing-rules")
	{
		// 路由治理（写）= Admin+。
		rules.POST("", g.Admin(), h.CreateRule)
		rules.PUT("/:id", g.Admin(), h.UpdateRule)
		rules.DELETE("/:id", g.Admin(), h.DeleteRule)
		// 查询（含平台级规则的只读视图）= Admin+（治理面统一收紧）。
		rules.GET("", g.Admin(), h.ListRules)
		rules.GET("/:id", g.Admin(), h.GetRule)
	}
	prompts := r.Group("/model-gateway/prompts")
	{
		// 版本读取（读）= Viewer+。
		// 可覆盖的模板清单（读）= Viewer+；必须在 /:key 通配段之前无冲突：
		// 它是 /model-gateway/prompts 的静态根，与 /:key/... 不共享节点。
		prompts.GET("", g.Viewer(), h.ListPromptTemplates)
		prompts.GET("/:key/versions", g.Viewer(), h.ListPromptVersions)
		prompts.GET("/:key/versions/:version", g.Viewer(), h.GetPromptVersion)
		prompts.GET("/:key/latest", g.Viewer(), h.LatestPromptVersion)
		// 新版本（写）= Admin+。
		prompts.POST("/:key/versions", g.Admin(), h.SavePromptVersion)
	}
	usage := r.Group("/model-gateway")
	{
		// 成本数据（读）= Admin+。
		usage.GET("/usage", g.Admin(), h.UsageSummary)
	}
}
