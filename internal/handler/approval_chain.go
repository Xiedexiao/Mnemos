package handler

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// ApprovalChainHandler exposes the multi-stage approval chain: reading its
// state, the in-flight mutations (加签/转签) and delegations (KnowAxis V3.0
// §6.3).
type ApprovalChainHandler struct {
	chain interfaces.ApprovalChainService
}

// NewApprovalChainHandler constructs the handler.
func NewApprovalChainHandler(chain interfaces.ApprovalChainService) *ApprovalChainHandler {
	return &ApprovalChainHandler{chain: chain}
}

// tenantFromContext rejects unauthenticated contexts with a 401.
func tenantFromContext(c *gin.Context) (uint64, bool) {
	tenantID, ok := types.TenantIDFromContext(c.Request.Context())
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return 0, false
	}
	return tenantID, true
}

// GetApprovalChain godoc
// @Summary      读取审批链状态
// @Description  返回一张审核任务的节点链（逐级顺序、每层或签/会签、谁已投票、当前激活层）。多级审批上线前提交的历史任务会显示为一个隐式节点。
// @Tags         知识审核
// @Produce      json
// @Param        task_id  path  string  true  "审批任务ID"
// @Success      200  {object}  map[string]interface{}  "{success, data: ApprovalChainState}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /review/tasks/{task_id}/chain [get]
func (h *ApprovalChainHandler) GetApprovalChain(c *gin.Context) {
	tenantID, ok := tenantFromContext(c)
	if !ok {
		return
	}
	state, err := h.chain.State(c.Request.Context(), tenantID, c.Param("task_id"))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": state})
}

// approverMutationRequest is the body of the 加签/转签 endpoints.
type approverMutationRequest struct {
	NodeID     string `json:"node_id"`
	ApproverID string `json:"approver_id"`
	FromID     string `json:"from_id"`
	ToID       string `json:"to_id"`
}

// AddApprover godoc
// @Summary      加签
// @Description  在途给某个审批节点追加一位审批人（§6.3 加签）。会签节点同时把所需通过人数 +1，否则新增的人可以被忽略。
// @Tags         知识审核
// @Accept       json
// @Produce      json
// @Param        task_id  path  string                 true  "审批任务ID"
// @Param        request  body  approverMutationRequest true  "节点与审批人"
// @Success      200  {object}  map[string]interface{}  "{success, data: ApprovalChainState}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /review/tasks/{task_id}/chain/approvers [post]
func (h *ApprovalChainHandler) AddApprover(c *gin.Context) {
	tenantID, ok := tenantFromContext(c)
	if !ok {
		return
	}
	var req approverMutationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(c.Request.Context())
	state, err := h.chain.AddApprover(c.Request.Context(), tenantID,
		c.Param("task_id"), req.NodeID, req.ApproverID, caller.UserID)
	if err != nil {
		writeChainError(c, err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": state})
}

// TransferApprover godoc
// @Summary      转签
// @Description  把某个节点上的一位审批人替换为另一位（§6.3 转签）。已终结的节点不允许转签，否则审批记录与实际责任人脱节。
// @Tags         知识审核
// @Accept       json
// @Produce      json
// @Param        task_id  path  string                 true  "审批任务ID"
// @Param        request  body  approverMutationRequest true  "节点、原审批人与新审批人"
// @Success      200  {object}  map[string]interface{}  "{success, data: ApprovalChainState}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /review/tasks/{task_id}/chain/transfer [post]
func (h *ApprovalChainHandler) TransferApprover(c *gin.Context) {
	tenantID, ok := tenantFromContext(c)
	if !ok {
		return
	}
	var req approverMutationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(c.Request.Context())
	state, err := h.chain.TransferApprover(c.Request.Context(), tenantID,
		c.Param("task_id"), req.NodeID, req.FromID, req.ToID, caller.UserID)
	if err != nil {
		writeChainError(c, err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": state})
}

// delegationRequest is the body of POST /approval-delegations.
type delegationRequest struct {
	DelegatorID string `json:"delegator_id"`
	DelegateID  string `json:"delegate_id"`
	Reason      string `json:"reason"`
	// ExpiresInHours bounds the delegation; 0 means no expiry (must be revoked
	// explicitly). A finite default keeps a forgotten delegation from lasting
	// forever.
	ExpiresInHours int `json:"expires_in_hours"`
}

// CreateDelegation godoc
// @Summary      设置审批委托
// @Description  允许某人在一段时间内代自己投票（§6.3 委托/休假代理）。票记在代理人名下，但保留授权来源，审计可追溯。
// @Tags         知识审核
// @Accept       json
// @Produce      json
// @Param        request  body  delegationRequest  true  "委托"
// @Success      200  {object}  map[string]interface{}  "{success, data: ApprovalDelegation}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /approval-delegations [post]
func (h *ApprovalChainHandler) CreateDelegation(c *gin.Context) {
	tenantID, ok := tenantFromContext(c)
	if !ok {
		return
	}
	var req delegationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	// 默认 30 天：忘记撤销的委托不应永久有效（节假日代理的典型长度）。
	hours := req.ExpiresInHours
	if hours <= 0 {
		hours = 24 * 30
	}
	expiresAt := time.Now().Add(time.Duration(hours) * time.Hour)
	delegation, err := h.chain.Delegate(c.Request.Context(), tenantID,
		strings.TrimSpace(req.DelegatorID), strings.TrimSpace(req.DelegateID),
		req.Reason, &expiresAt)
	if err != nil {
		writeChainError(c, err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": delegation})
}

// ListDelegations godoc
// @Summary      列出委托
// @Description  返回某位代理人当前持有的有效委托（缺省为调用者自己）。
// @Tags         知识审核
// @Produce      json
// @Param        delegate_id  query  string  false  "代理人ID（缺省为当前用户）"
// @Success      200  {object}  map[string]interface{}  "{success, data: [ApprovalDelegation]}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /approval-delegations [get]
func (h *ApprovalChainHandler) ListDelegations(c *gin.Context) {
	tenantID, ok := tenantFromContext(c)
	if !ok {
		return
	}
	delegateID := strings.TrimSpace(c.Query("delegate_id"))
	if delegateID == "" {
		delegateID = types.CallerFromContext(c.Request.Context()).UserID
	}
	rows, err := h.chain.ListDelegations(c.Request.Context(), tenantID, delegateID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": rows})
}

// RevokeDelegation godoc
// @Summary      撤销委托
// @Description  立即结束一条委托；撤销后代理人未投的票不再被接受。
// @Tags         知识审核
// @Produce      json
// @Param        delegation_id  path  string  true  "委托ID"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /approval-delegations/{delegation_id} [delete]
func (h *ApprovalChainHandler) RevokeDelegation(c *gin.Context) {
	tenantID, ok := tenantFromContext(c)
	if !ok {
		return
	}
	caller := types.CallerFromContext(c.Request.Context())
	if err := h.chain.RevokeDelegation(c.Request.Context(), tenantID,
		c.Param("delegation_id"), caller.UserID); err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true})
}

// writeChainError maps chain sentinels onto HTTP semantics consistently with the
// review handler: 403 for "not your vote", 409 for a state conflict.
func writeChainError(c *gin.Context, err error) {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "not assigned"), strings.Contains(msg, "delegation"):
		c.Error(errors.NewForbiddenError(msg))
	case strings.Contains(msg, "already"), strings.Contains(msg, "not accepting"),
		strings.Contains(msg, "not found"):
		c.Error(errors.NewConflictError(msg))
	default:
		c.Error(err)
	}
}
