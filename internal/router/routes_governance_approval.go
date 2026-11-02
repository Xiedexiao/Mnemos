package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterGovernanceApprovalRoutes wires the fourteen governance approval
// subjects (KnowAxis V3.0 §6.2).
//
// 权限矩阵：
//   - POST /governance-approvals、/status、列表 —— 治理面：Admin+。
//   - POST /:task_id/decide —— 审批裁决：Admin+；服务层另有四眼校验。
//
// 全部 JWT-only：审批是"人"的责任，机器身份不得签发治理凭据。
func RegisterGovernanceApprovalRoutes(
	r *gin.RouterGroup, h *handler.GovernanceApprovalHandler, g *rbacGuards,
) {
	if h == nil {
		return
	}
	governance := r.Group("/governance-approvals")
	{
		governance.GET("", g.Admin(), h.ListGovernanceApprovals)
		// status 是静态段，注册在通配段之前。
		governance.GET("/status", g.Admin(), h.GetGovernanceApprovalStatus)
		governance.POST("", g.Admin(), h.SubmitGovernanceApproval)
		governance.POST("/:task_id/decide", g.Admin(), h.DecideGovernanceApproval)
	}
}
