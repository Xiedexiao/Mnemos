package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterApprovalChainRoutes wires the multi-stage approval chain surface
// (KnowAxis V3.0 §6.3: 或签/会签/逐级 + 加签/转签/委托).
//
// 权限矩阵：
//   - GET /review/tasks/:task_id/chain —— 审批链状态，含审批人名单：Admin+。
//   - POST .../chain/approvers、.../chain/transfer —— 改变谁能签，等于改变
//     审批责任：Admin+。
//   - POST/DELETE/GET /approval-delegations —— 委托是"把自己的投票权交给
//     别人"，属于治理动作：Admin+。
//
// 全部 JWT-only：审批链的参与者是"人"，机器身份不得代签或改签。
func RegisterApprovalChainRoutes(
	r *gin.RouterGroup, h *handler.ApprovalChainHandler, g *rbacGuards,
) {
	if h == nil {
		return
	}
	chain := r.Group("/review/tasks/:task_id/chain")
	{
		chain.GET("", g.Admin(), h.GetApprovalChain)
		chain.POST("/approvers", g.Admin(), h.AddApprover)
		chain.POST("/transfer", g.Admin(), h.TransferApprover)
	}
	delegations := r.Group("/approval-delegations")
	{
		delegations.GET("", g.Admin(), h.ListDelegations)
		delegations.POST("", g.Admin(), h.CreateDelegation)
		delegations.DELETE("/:delegation_id", g.Admin(), h.RevokeDelegation)
	}
}
