package handler

import (
	"encoding/json"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// ApprovalFlowHandler exposes the per-space approval routing configuration
// (KnowAxis V3.0 §6.3 流程设计器 + 条件路由 + SLA).
type ApprovalFlowHandler struct {
	flows interfaces.ApprovalFlowService
	sla   interfaces.ApprovalSLAService
}

// NewApprovalFlowHandler constructs the handler.
func NewApprovalFlowHandler(
	flows interfaces.ApprovalFlowService,
	sla interfaces.ApprovalSLAService,
) *ApprovalFlowHandler {
	return &ApprovalFlowHandler{flows: flows, sla: sla}
}

// approvalFlowRequest is the body of PUT /approval-flows/:kb_id.
type approvalFlowRequest struct {
	Mode          string     `json:"mode"`
	SLAHours      int        `json:"sla_hours"`
	ReminderHours int        `json:"reminder_hours"`
	EscalateRole  string     `json:"escalate_role"`
	Enabled       *bool      `json:"enabled"`
	Rules         types.JSON `json:"rules"`
	// Nodes is the approval chain (§6.3): 或签 / 会签 / 逐级 都由它表达。
	// Omit to clear the definition and fall back to the implicit single node.
	Nodes []approvalFlowNodeRequest `json:"nodes"`
}

// approvalFlowNodeRequest is one node of the chain definition.
type approvalFlowNodeRequest struct {
	Seq          int      `json:"seq"`
	Name         string   `json:"name"`
	Mode         string   `json:"mode"`          // or | and
	AssigneeKind string   `json:"assignee_kind"` // role | users
	AssigneeRole string   `json:"assignee_role"`
	AssigneeIDs  []string `json:"assignee_ids"`
}

// GetApprovalFlow godoc
// @Summary      读取空间审批流配置
// @Description  返回某个知识空间的审批流配置（模式/SLA/催办/升级角色/条件路由）。未配置时返回 single 模式默认值，不返回 404。
// @Tags         知识审核
// @Produce      json
// @Param        kb_id  path  string  true  "知识库ID"
// @Success      200  {object}  map[string]interface{}  "{success, data: ApprovalFlow}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /approval-flows/{kb_id} [get]
func (h *ApprovalFlowHandler) GetApprovalFlow(c *gin.Context) {
	tenantID, ok := types.TenantIDFromContext(c.Request.Context())
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	flow, err := h.flows.Get(c.Request.Context(), tenantID, c.Param("kb_id"))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": flow})
}

// ListApprovalFlows godoc
// @Summary      列出空间审批流配置
// @Description  列出租户内已配置审批流的知识空间（管理面）。
// @Tags         知识审核
// @Produce      json
// @Success      200  {object}  map[string]interface{}  "{success, data: [ApprovalFlow]}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /approval-flows [get]
func (h *ApprovalFlowHandler) ListApprovalFlows(c *gin.Context) {
	tenantID, ok := types.TenantIDFromContext(c.Request.Context())
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	flows, err := h.flows.List(c.Request.Context(), tenantID, 0)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": flows})
}

// UpsertApprovalFlow godoc
// @Summary      保存空间审批流配置
// @Description  创建或更新某个知识空间的审批流配置。每次保存都会让流程版本前进，在途审核任务仍按其提交时冻结的版本走完（§6.3）。
// @Tags         知识审核
// @Accept       json
// @Produce      json
// @Param        kb_id    path  string              true  "知识库ID"
// @Param        request  body  approvalFlowRequest true  "审批流配置"
// @Success      200  {object}  map[string]interface{}  "{success, data: ApprovalFlow}"
// @Failure      400  {object}  errors.AppError
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /approval-flows/{kb_id} [put]
func (h *ApprovalFlowHandler) UpsertApprovalFlow(c *gin.Context) {
	tenantID, ok := types.TenantIDFromContext(c.Request.Context())
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req approvalFlowRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	flow := &types.ApprovalFlow{
		TenantID:      tenantID,
		KBID:          strings.TrimSpace(c.Param("kb_id")),
		Mode:          req.Mode,
		SLAHours:      req.SLAHours,
		ReminderHours: req.ReminderHours,
		EscalateRole:  req.EscalateRole,
		Rules:         req.Rules,
		Enabled:       enabled,
	}
	nodes := make([]*types.ApprovalFlowNode, 0, len(req.Nodes))
	for i, node := range req.Nodes {
		seq := node.Seq
		if seq <= 0 {
			seq = i + 1
		}
		raw, _ := json.Marshal(node.AssigneeIDs)
		nodes = append(nodes, &types.ApprovalFlowNode{
			Seq:          seq,
			Name:         node.Name,
			Mode:         node.Mode,
			AssigneeKind: node.AssigneeKind,
			AssigneeRole: node.AssigneeRole,
			AssigneeIDs:  types.JSON(raw),
		})
	}
	saved, err := h.flows.Save(c.Request.Context(), flow, nodes)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": saved})
}

// DeleteApprovalFlow godoc
// @Summary      删除空间审批流配置
// @Description  删除配置后该空间回到 single 单人审批兜底。
// @Tags         知识审核
// @Produce      json
// @Param        kb_id  path  string  true  "知识库ID"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /approval-flows/{kb_id} [delete]
func (h *ApprovalFlowHandler) DeleteApprovalFlow(c *gin.Context) {
	tenantID, ok := types.TenantIDFromContext(c.Request.Context())
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	if err := h.flows.Delete(c.Request.Context(), tenantID, c.Param("kb_id")); err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true})
}

// ResolveApprovalChain godoc
// @Summary      预览审批链
// @Description  按当前配置与内容风险试算一次提交会走什么审批链（模式/法定人数/触发规则）。用于 §6.3 "规则可模拟"：配置改了会影响谁，先看再保存。
// @Tags         知识审核
// @Produce      json
// @Param        kb_id       path   string  true  "知识库ID"
// @Param        risk_level  query  string  false "内容风险级（low|mid|high），缺省 low"
// @Success      200  {object}  map[string]interface{}  "{success, data: ApprovalResolution}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /approval-flows/{kb_id}/resolve [get]
func (h *ApprovalFlowHandler) ResolveApprovalChain(c *gin.Context) {
	tenantID, ok := types.TenantIDFromContext(c.Request.Context())
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	risk := types.ApprovalRiskLevel(strings.TrimSpace(c.Query("risk_level")))
	if risk == "" {
		risk = types.ApprovalRiskLow
	}
	if !validApprovalRiskLevel(risk) {
		c.Error(errors.NewBadRequestError("risk_level must be low, mid or high"))
		return
	}
	resolution, err := h.flows.Resolve(c.Request.Context(), tenantID, c.Param("kb_id"), risk)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": resolution})
}

// ListApprovalSLAEvents godoc
// @Summary      审核 SLA 处置台账
// @Description  返回某张审核任务的催办/升级记录（§6.3）。
// @Tags         知识审核
// @Produce      json
// @Param        task_id  path  string  true  "审批任务ID"
// @Success      200  {object}  map[string]interface{}  "{success, data: [ApprovalSLAEvent]}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /review/tasks/{task_id}/sla-events [get]
func (h *ApprovalFlowHandler) ListApprovalSLAEvents(c *gin.Context) {
	tenantID, ok := types.TenantIDFromContext(c.Request.Context())
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	if h.sla == nil {
		c.JSON(200, gin.H{"success": true, "data": []interface{}{}})
		return
	}
	events, err := h.sla.Events(c.Request.Context(), tenantID, c.Param("task_id"))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": events})
}

// validApprovalRiskLevel mirrors the service-side whitelist for query params.
func validApprovalRiskLevel(level types.ApprovalRiskLevel) bool {
	switch level {
	case types.ApprovalRiskLow, types.ApprovalRiskMid, types.ApprovalRiskHigh:
		return true
	default:
		return false
	}
}
