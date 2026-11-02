package handler

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// GovernanceApprovalHandler exposes the fourteen governance approval subjects
// (KnowAxis V3.0 §6.2).
type GovernanceApprovalHandler struct {
	governance interfaces.GovernanceApprovalService
}

// NewGovernanceApprovalHandler constructs the handler.
func NewGovernanceApprovalHandler(
	governance interfaces.GovernanceApprovalService,
) *GovernanceApprovalHandler {
	return &GovernanceApprovalHandler{governance: governance}
}

// governanceSubmitRequest is the body of POST /governance-approvals.
type governanceSubmitRequest struct {
	SubjectType string     `json:"subject_type"`
	SubjectID   string     `json:"subject_id"`
	KBID        string     `json:"kb_id"`
	Title       string     `json:"title"`
	Reason      string     `json:"reason"`
	RiskLevel   string     `json:"risk_level"`
	Snapshot    types.JSON `json:"snapshot"`
}

// SubmitGovernanceApproval godoc
// @Summary      提交治理动作审批
// @Description  为任何需要审批的治理动作创建审批任务（KnowAxis V3.0 §6.2）：官方答案、规则政策、本体变更、权限提权、外部分享、批量导出、删除下架、合并拆分、术语模板变更、模型 Prompt 上线、Agent 工具授权等。审批链由空间的审批流配置决定（或签/会签/逐级）。
// @Tags         治理审批
// @Accept       json
// @Produce      json
// @Param        request  body  governanceSubmitRequest  true  "治理动作"
// @Success      200  {object}  map[string]interface{}  "{success, data: ApprovalTask}"
// @Failure      409  {object}  errors.AppError  "该主体已有待审任务"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /governance-approvals [post]
func (h *GovernanceApprovalHandler) SubmitGovernanceApproval(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req governanceSubmitRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	task, err := h.governance.Submit(ctx, tenantID, caller.UserID, &types.GovernanceApprovalRequest{
		SubjectType: types.ApprovalSubjectType(strings.TrimSpace(req.SubjectType)),
		SubjectID:   strings.TrimSpace(req.SubjectID),
		KBID:        req.KBID,
		Title:       req.Title,
		Reason:      req.Reason,
		RiskLevel:   types.ApprovalRiskLevel(req.RiskLevel),
		Snapshot:    req.Snapshot,
	})
	if err != nil {
		writeGovernanceError(c, err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": task})
}

// governanceDecideRequest is the body of POST /governance-approvals/:task_id/decide.
type governanceDecideRequest struct {
	Decision string `json:"decision"`
	Comment  string `json:"comment"`
}

// DecideGovernanceApproval godoc
// @Summary      裁决治理动作审批
// @Description  对治理审批任务投票。达到审批链要求后生成审批回执，动作方可生效；提交人不能批自己的动作。
// @Tags         治理审批
// @Accept       json
// @Produce      json
// @Param        task_id  path  string                 true  "审批任务ID"
// @Param        request  body  governanceDecideRequest true "裁决"
// @Success      200  {object}  map[string]interface{}  "{success, data: ApprovalTask}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /governance-approvals/{task_id}/decide [post]
func (h *GovernanceApprovalHandler) DecideGovernanceApproval(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req governanceDecideRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	task, err := h.governance.Decide(ctx, tenantID, c.Param("task_id"), caller.UserID,
		types.ApprovalDecision(req.Decision), req.Comment)
	if err != nil {
		writeGovernanceError(c, err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": task})
}

// GetGovernanceApprovalStatus godoc
// @Summary      查询治理动作审批状态
// @Description  返回某个治理主体的最新审批状态与回执。未提交过审批时返回空状态（不是 404）。
// @Tags         治理审批
// @Produce      json
// @Param        subject_type  query  string  true  "主体类型（§6.2）"
// @Param        subject_id    query  string  true  "主体ID"
// @Success      200  {object}  map[string]interface{}  "{success, data: GovernanceApprovalStatus}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /governance-approvals/status [get]
func (h *GovernanceApprovalHandler) GetGovernanceApprovalStatus(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	status, err := h.governance.Status(ctx, tenantID,
		types.ApprovalSubjectType(strings.TrimSpace(c.Query("subject_type"))),
		strings.TrimSpace(c.Query("subject_id")))
	if err != nil {
		writeGovernanceError(c, err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": status})
}

// ListGovernanceApprovals godoc
// @Summary      治理审批待办
// @Description  按主体类型列出待审的治理动作，用于审核工作台按 §6.2 的对象种类筛选。
// @Tags         治理审批
// @Produce      json
// @Param        subject_type  query  string  false  "主体类型（缺省为全部）"
// @Param        limit         query  int     false  "返回条数（默认 100，上限 200）"
// @Success      200  {object}  map[string]interface{}  "{success, data: [ApprovalTask]}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /governance-approvals [get]
func (h *GovernanceApprovalHandler) ListGovernanceApprovals(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "100"))
	if err != nil || limit <= 0 || limit > 200 {
		limit = 100
	}
	tasks, listErr := h.governance.ListBySubjectType(ctx, tenantID,
		types.ApprovalSubjectType(strings.TrimSpace(c.Query("subject_type"))), limit)
	if listErr != nil {
		writeGovernanceError(c, listErr)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": tasks})
}

// writeGovernanceError maps governance sentinels onto HTTP semantics.
func writeGovernanceError(c *gin.Context, err error) {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "already pending"):
		c.Error(errors.NewConflictError(msg))
	case strings.Contains(msg, "not approved"):
		c.Error(errors.NewForbiddenError(msg))
	case strings.Contains(msg, "requester cannot decide"),
		strings.Contains(msg, "cannot decide their own"):
		c.Error(errors.NewForbiddenError(msg))
	case strings.Contains(msg, "not found"):
		c.Error(errors.NewNotFoundError(msg))
	default:
		c.Error(err)
	}
}
