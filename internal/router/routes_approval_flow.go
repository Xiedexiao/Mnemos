package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterApprovalFlowRoutes wires the approval-routing configuration surface
// (KnowAxis V3.0 §6.3 流程设计器 / 条件路由 / SLA / 规则模拟).
//
// 权限矩阵：
//   - 读取配置（GET /approval-flows、/:kb_id）——空间治理面：Admin+。
//   - 写入配置（PUT/DELETE）——直接决定内容要走几道审批：Admin+。
//   - 试算（GET /:kb_id/resolve）——只读投影，但会暴露"谁能审"：Admin+。
//   - SLA 台账（GET /review/tasks/:task_id/sla-events）——审计面：Admin+。
//
// 全部 JWT-only（注册在裸 group 上、不经 apiKeyGroup 声明 → API key 默认
// 拒绝）：审批链配置是"人"的责任，机器身份不该改写它。
func RegisterApprovalFlowRoutes(
	r *gin.RouterGroup, h *handler.ApprovalFlowHandler, g *rbacGuards,
) {
	if h == nil {
		return
	}
	flows := r.Group("/approval-flows")
	{
		flows.GET("", g.Admin(), h.ListApprovalFlows)
		flows.GET("/:kb_id", g.Admin(), h.GetApprovalFlow)
		flows.PUT("/:kb_id", g.Admin(), h.UpsertApprovalFlow)
		flows.DELETE("/:kb_id", g.Admin(), h.DeleteApprovalFlow)
		// 规则试算：改了配置会影响谁，先看再保存（§6.3 "规则可模拟"）。
		flows.GET("/:kb_id/resolve", g.Admin(), h.ResolveApprovalChain)
	}
	// SLA 处置台账挂在 /review 子树下，与其它审核治理面同档。
	r.GET("/review/tasks/:task_id/sla-events", g.Admin(), h.ListApprovalSLAEvents)
}
