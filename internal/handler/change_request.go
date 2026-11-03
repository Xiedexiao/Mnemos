package handler

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Tencent/WeKnora/internal/application/service"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

// ChangeRequestHandler 暴露变更单 + 影响任务（KnowAxis V3.0 §7.2/§7.6 v1）。
// 变更单是治理动作：写路由一律 Admin+（路由层守卫），读路由 Viewer+。
// 租户/用户身份一律从 types.CallerFromContext 取。
type ChangeRequestHandler struct {
	changeRequestService interfaces.ChangeRequestService
}

// NewChangeRequestHandler constructs the change request handler.
func NewChangeRequestHandler(changeRequestService interfaces.ChangeRequestService) *ChangeRequestHandler {
	return &ChangeRequestHandler{changeRequestService: changeRequestService}
}

// createChangeRequestRequest 是 POST /change-requests 的请求体。reason /
// change_type / risk_level 用 oneof 白名单，标题长度在绑定层钳制。
type createChangeRequestRequest struct {
	Title      string `json:"title"       binding:"required,max=255"`
	Reason     string `json:"reason"      binding:"required,oneof=regulation process product correction customer other"`
	ChangeType string `json:"change_type" binding:"required,oneof=text_fix content_revision param_change replacement delist restructure"`
	RiskLevel  string `json:"risk_level"  binding:"omitempty,oneof=low mid high"`
	// Note 创建人补充说明，冻结进 baseline。
	Note string `json:"note" binding:"max=2000"`
	// RollbackPlan 回滚方案。
	RollbackPlan string `json:"rollback_plan" binding:"max=5000"`
	// EffectiveAt / AnnounceAt / ExpiresAt 生效/公告/失效时间（可选）。
	EffectiveAt *time.Time `json:"effective_at"`
	AnnounceAt  *time.Time `json:"announce_at"`
	ExpiresAt   *time.Time `json:"expires_at"`
	// CloseConditions 结构化关闭前置条件（v1 只读展示）。
	CloseConditions []types.ChangeCloseCondition `json:"close_conditions" binding:"omitempty,max=20,dive"`
}

// addItemRequest 是 POST /change-requests/:id/items 的请求体。
type addItemRequest struct {
	KnowledgeID string `json:"knowledge_id" binding:"required,max=36"`
}

// scheduleRequest 是 POST /change-requests/:id/schedule 的请求体。
// effective_at 缺省时服务层取当前时刻（"批准即生效"）。
type scheduleRequest struct {
	EffectiveAt *time.Time `json:"effective_at"`
}

// updateImpactTaskRequest 是 POST /change-requests/:id/impact-tasks/:task_id
// 的请求体：更新任务状态（open/done/waived）与备注。
type updateImpactTaskRequest struct {
	Status string `json:"status" binding:"required,oneof=open done waived"`
	Note   string `json:"note"   binding:"max=2000"`
}

// listChangeRequestsRequest 是 GET /change-requests 的查询参数。
type listChangeRequestsRequest struct {
	Status string `form:"status" binding:"omitempty,oneof=draft analyzing in_approval scheduled publishing verifying closed rolled_back"`
	Limit  int    `form:"limit" binding:"omitempty,min=1,max=200"`
	Offset int    `form:"offset" binding:"omitempty,min=0"`
}

// Create godoc
// @Summary      创建变更单
// @Description  创建一张变更单（draft 状态），生成租户内唯一单号 CR-YYYYMMDD-序号。reason/change_type/risk_level 按白名单校验。
// @Tags         变更单
// @Accept       json
// @Produce      json
// @Param        request  body  handler.createChangeRequestRequest  true  "{title, reason, change_type, risk_level?, note?, rollback_plan?, effective_at?, close_conditions?}"
// @Success      200  {object}  map[string]interface{}  "{success, data: 变更单}"
// @Failure      400  {object}  apperrors.AppError  "参数非法"
// @Failure      401  {object}  apperrors.AppError  "缺少租户上下文"
// @Security     Bearer
// @Router       /change-requests [post]
func (h *ChangeRequestHandler) Create(c *gin.Context) {
	ctx := c.Request.Context()
	if forbidden := changeRequestCallerGuard(c); forbidden {
		return
	}

	var req createChangeRequestRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(apperrors.NewValidationError("invalid request body: " + err.Error()))
		return
	}
	input := types.ChangeRequestCreateInput{
		Title:           req.Title,
		Reason:          types.ChangeRequestReason(req.Reason),
		ChangeType:      types.ChangeType(req.ChangeType),
		RiskLevel:       types.ChangeRiskLevel(req.RiskLevel),
		RollbackPlan:    req.RollbackPlan,
		Note:            req.Note,
		EffectiveAt:     req.EffectiveAt,
		AnnounceAt:      req.AnnounceAt,
		ExpiresAt:       req.ExpiresAt,
		CloseConditions: req.CloseConditions,
	}
	cr, err := h.changeRequestService.CreateCR(ctx, input)
	if err != nil {
		writeChangeRequestError(c, ctx, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": cr})
}

// List godoc
// @Summary      变更单列表
// @Description  分页返回本租户变更单（created_at 倒序，聚合 items 与影响任务）。可按 status 过滤。
// @Tags         变更单
// @Produce      json
// @Param        status  query  string  false  "变更单状态过滤"
// @Param        limit   query  int     false  "条数，1-200"  default(50)
// @Param        offset  query  int     false  "偏移量"       default(0)
// @Success      200  {object}  map[string]interface{}  "{success, data: [变更单], total}"
// @Failure      401  {object}  apperrors.AppError  "缺少租户上下文"
// @Security     Bearer
// @Router       /change-requests [get]
func (h *ChangeRequestHandler) List(c *gin.Context) {
	ctx := c.Request.Context()
	if forbidden := changeRequestCallerGuard(c); forbidden {
		return
	}

	var req listChangeRequestsRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.Error(apperrors.NewValidationError("invalid query parameters: " + err.Error()))
		return
	}
	crs, total, err := h.changeRequestService.List(ctx, types.ChangeRequestListFilter{
		Status: req.Status,
		Limit:  req.Limit,
		Offset: req.Offset,
	})
	if err != nil {
		writeChangeRequestError(c, ctx, err)
		return
	}
	if crs == nil {
		crs = []*types.ChangeRequest{}
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": crs, "total": total})
}

// Get godoc
// @Summary      获取变更单详情
// @Description  按单据 ID 返回变更单（聚合 items 与影响任务）。
// @Tags         变更单
// @Produce      json
// @Param        id  path  string  true  "变更单ID"
// @Success      200  {object}  map[string]interface{}  "{success, data: 变更单}"
// @Failure      401  {object}  apperrors.AppError  "缺少租户上下文"
// @Failure      404  {object}  apperrors.AppError  "变更单不存在"
// @Security     Bearer
// @Router       /change-requests/{id} [get]
func (h *ChangeRequestHandler) Get(c *gin.Context) {
	ctx := c.Request.Context()
	if forbidden := changeRequestCallerGuard(c); forbidden {
		return
	}
	crID := c.Param("id")
	if crID == "" {
		c.Error(apperrors.NewBadRequestError("change request id is required"))
		return
	}
	cr, err := h.changeRequestService.Get(ctx, crID)
	if err != nil {
		writeChangeRequestError(c, ctx, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": cr})
}

// AddItem godoc
// @Summary      追加变更条目
// @Description  把一条知识加入变更单（仅 draft/analyzing 可改；同一知识幂等防重）。
// @Tags         变更单
// @Accept       json
// @Produce      json
// @Param        id      path  string  true  "变更单ID"
// @Param        request body  handler.addItemRequest  true  "{knowledge_id}"
// @Success      200  {object}  map[string]interface{}  "{success, data: 变更单（聚合）}"
// @Failure      404  {object}  apperrors.AppError  "变更单/知识不存在"
// @Failure      409  {object}  apperrors.AppError  "状态不允许或知识已在单内"
// @Security     Bearer
// @Router       /change-requests/{id}/items [post]
func (h *ChangeRequestHandler) AddItem(c *gin.Context) {
	ctx := c.Request.Context()
	if forbidden := changeRequestCallerGuard(c); forbidden {
		return
	}
	crID := c.Param("id")
	if crID == "" {
		c.Error(apperrors.NewBadRequestError("change request id is required"))
		return
	}
	var req addItemRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(apperrors.NewValidationError("invalid request body: " + err.Error()))
		return
	}
	cr, err := h.changeRequestService.AddItem(ctx, crID, req.KnowledgeID)
	if err != nil {
		writeChangeRequestError(c, ctx, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": cr})
}

// RemoveItem godoc
// @Summary      移除变更条目
// @Description  把一条知识移出变更单（仅 draft/analyzing 可改）。
// @Tags         变更单
// @Produce      json
// @Param        id            path  string  true  "变更单ID"
// @Param        knowledge_id  path  string  true  "知识ID"
// @Success      200  {object}  map[string]interface{}  "{success, data: 变更单（聚合）}"
// @Failure      404  {object}  apperrors.AppError  "变更单/条目不存在"
// @Failure      409  {object}  apperrors.AppError  "状态不允许修改条目"
// @Security     Bearer
// @Router       /change-requests/{id}/items/{knowledge_id} [delete]
func (h *ChangeRequestHandler) RemoveItem(c *gin.Context) {
	ctx := c.Request.Context()
	if forbidden := changeRequestCallerGuard(c); forbidden {
		return
	}
	crID := c.Param("id")
	knowledgeID := c.Param("knowledge_id")
	if crID == "" || knowledgeID == "" {
		c.Error(apperrors.NewBadRequestError("change request id and knowledge id are required"))
		return
	}
	cr, err := h.changeRequestService.RemoveItem(ctx, crID, knowledgeID)
	if err != nil {
		writeChangeRequestError(c, ctx, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": cr})
}

// Analyze godoc
// @Summary      影响分析
// @Description  draft→analyzing：为每个条目建 knowledge 类影响任务（priority 按 risk_level），并对同 KB 共享标签的近邻知识生成波及任务（v1 简化）；条目清单冻结进 baseline。
// @Tags         变更单
// @Produce      json
// @Param        id  path  string  true  "变更单ID"
// @Success      200  {object}  map[string]interface{}  "{success, data: 变更单（聚合）}"
// @Failure      400  {object}  apperrors.AppError  "状态机不允许/无条目/条目知识缺失"
// @Failure      404  {object}  apperrors.AppError  "变更单不存在"
// @Security     Bearer
// @Router       /change-requests/{id}/analyze [post]
func (h *ChangeRequestHandler) Analyze(c *gin.Context) {
	ctx := c.Request.Context()
	if forbidden := changeRequestCallerGuard(c); forbidden {
		return
	}
	cr, err := h.changeRequestService.Analyze(ctx, c.Param("id"))
	if err != nil {
		writeChangeRequestError(c, ctx, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": cr})
}

// SubmitForApproval godoc
// @Summary      提交审批
// @Description  analyzing→in_approval（v1 占位：真实审批对接 A2 审核流为后续版本）。
// @Tags         变更单
// @Produce      json
// @Param        id  path  string  true  "变更单ID"
// @Success      200  {object}  map[string]interface{}  "{success, data: 变更单（聚合）}"
// @Failure      404  {object}  apperrors.AppError  "变更单不存在"
// @Failure      409  {object}  apperrors.AppError  "状态机不允许"
// @Security     Bearer
// @Router       /change-requests/{id}/submit [post]
func (h *ChangeRequestHandler) SubmitForApproval(c *gin.Context) {
	ctx := c.Request.Context()
	if forbidden := changeRequestCallerGuard(c); forbidden {
		return
	}
	cr, err := h.changeRequestService.SubmitForApproval(ctx, c.Param("id"))
	if err != nil {
		writeChangeRequestError(c, ctx, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": cr})
}

// Schedule godoc
// @Summary      排期
// @Description  in_approval→scheduled：设定生效时间（缺省取当前时刻）。
// @Tags         变更单
// @Accept       json
// @Produce      json
// @Param        id      path  string  true  "变更单ID"
// @Param        request body  handler.scheduleRequest  false  "{effective_at?}"
// @Success      200  {object}  map[string]interface{}  "{success, data: 变更单（聚合）}"
// @Failure      404  {object}  apperrors.AppError  "变更单不存在"
// @Failure      409  {object}  apperrors.AppError  "状态机不允许"
// @Security     Bearer
// @Router       /change-requests/{id}/schedule [post]
func (h *ChangeRequestHandler) Schedule(c *gin.Context) {
	ctx := c.Request.Context()
	if forbidden := changeRequestCallerGuard(c); forbidden {
		return
	}
	// body 可选：缺省即"批准即生效"。空 body / 非 JSON body 均放行到服务层默认值。
	var req scheduleRequest
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.Error(apperrors.NewValidationError("invalid request body: " + err.Error()))
			return
		}
	}
	cr, err := h.changeRequestService.Schedule(ctx, c.Param("id"), req.EffectiveAt)
	if err != nil {
		writeChangeRequestError(c, ctx, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": cr})
}

// Publish godoc
// @Summary      发布
// @Description  scheduled→publishing→verifying：对每个条目调 lifecycle Transition（v1 一律 mark_stale），成功写 applied、失败写 failed；存在失败时单据留在 publishing 可重试，全部成功进 verifying。
// @Tags         变更单
// @Produce      json
// @Param        id  path  string  true  "变更单ID"
// @Success      200  {object}  map[string]interface{}  "{success, data: 变更单（聚合，failed 项可见）}"
// @Failure      400  {object}  apperrors.AppError  "状态机不允许/无条目"
// @Failure      404  {object}  apperrors.AppError  "变更单不存在"
// @Security     Bearer
// @Router       /change-requests/{id}/publish [post]
func (h *ChangeRequestHandler) Publish(c *gin.Context) {
	ctx := c.Request.Context()
	if forbidden := changeRequestCallerGuard(c); forbidden {
		return
	}
	cr, err := h.changeRequestService.Publish(ctx, c.Param("id"))
	if err != nil {
		writeChangeRequestError(c, ctx, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": cr})
}

// Close godoc
// @Summary      关闭变更单
// @Description  verifying→closed。门禁：所有影响任务 done/waived 且所有条目非 pending；不满足返回 400 并列出未闭环项。
// @Tags         变更单
// @Produce      json
// @Param        id  path  string  true  "变更单ID"
// @Success      200  {object}  map[string]interface{}  "{success, data: 变更单（聚合）}"
// @Failure      400  {object}  apperrors.AppError  "关闭门禁未满足（含未闭环清单）"
// @Failure      404  {object}  apperrors.AppError  "变更单不存在"
// @Failure      409  {object}  apperrors.AppError  "状态机不允许"
// @Security     Bearer
// @Router       /change-requests/{id}/close [post]
func (h *ChangeRequestHandler) Close(c *gin.Context) {
	ctx := c.Request.Context()
	if forbidden := changeRequestCallerGuard(c); forbidden {
		return
	}
	cr, err := h.changeRequestService.Close(ctx, c.Param("id"))
	if err != nil {
		writeChangeRequestError(c, ctx, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": cr})
}

// MarkVerified godoc
// @Summary      记录变更验证结论
// @Description  填写发布后的验证结论（KnowAxis V3.0 §7.2 关闭门禁条件②）。未填写验证结论的变更单不能关闭。
// @Tags         变更单
// @Accept       json
// @Produce      json
// @Param        id       path  string                      true  "变更单ID"
// @Param        request  body  changeVerifyRequest         true  "验证结论"
// @Success      200  {object}  map[string]interface{}  "{success, data: 变更单（聚合）}"
// @Security     Bearer
// @Router       /change-requests/{id}/verify [post]
func (h *ChangeRequestHandler) MarkVerified(c *gin.Context) {
	ctx := c.Request.Context()
	if forbidden := changeRequestCallerGuard(c); forbidden {
		return
	}
	var req changeVerifyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(apperrors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	cr, err := h.changeRequestService.MarkVerified(ctx, c.Param("id"), req.Result, caller.UserID)
	if err != nil {
		writeChangeRequestError(c, ctx, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": cr})
}

// MarkNotified godoc
// @Summary      记录影响通知已送达
// @Description  标记受影响 Owner 已通知（§7.2 关闭门禁条件④）。未通知的变更单不能关闭。
// @Tags         变更单
// @Produce      json
// @Param        id  path  string  true  "变更单ID"
// @Success      200  {object}  map[string]interface{}  "{success, data: 变更单（聚合）}"
// @Security     Bearer
// @Router       /change-requests/{id}/notify [post]
func (h *ChangeRequestHandler) MarkNotified(c *gin.Context) {
	ctx := c.Request.Context()
	if forbidden := changeRequestCallerGuard(c); forbidden {
		return
	}
	caller := types.CallerFromContext(ctx)
	cr, err := h.changeRequestService.MarkNotified(ctx, c.Param("id"), caller.UserID)
	if err != nil {
		writeChangeRequestError(c, ctx, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": cr})
}

// Rollback godoc
// @Summary      回滚变更单
// @Description  verifying→rolled_back（终态，留痕审计）。已应用条目保持其状态，恢复需走 lifecycle 审批。
// @Tags         变更单
// @Produce      json
// @Param        id  path  string  true  "变更单ID"
// @Success      200  {object}  map[string]interface{}  "{success, data: 变更单（聚合）}"
// @Failure      404  {object}  apperrors.AppError  "变更单不存在"
// @Failure      409  {object}  apperrors.AppError  "状态机不允许"
// @Security     Bearer
// @Router       /change-requests/{id}/rollback [post]
func (h *ChangeRequestHandler) Rollback(c *gin.Context) {
	ctx := c.Request.Context()
	if forbidden := changeRequestCallerGuard(c); forbidden {
		return
	}
	cr, err := h.changeRequestService.Rollback(ctx, c.Param("id"))
	if err != nil {
		writeChangeRequestError(c, ctx, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": cr})
}

// UpdateImpactTask godoc
// @Summary      更新影响任务
// @Description  更新变更单下某条影响任务的状态（open/done/waived）与备注；任务必须属于该变更单。
// @Tags         变更单
// @Accept       json
// @Produce      json
// @Param        id       path  string  true  "变更单ID"
// @Param        task_id  path  string  true  "影响任务ID"
// @Param        request  body  handler.updateImpactTaskRequest  true  "{status: open|done|waived, note?}"
// @Success      200  {object}  map[string]interface{}  "{success, data: 影响任务}"
// @Failure      400  {object}  apperrors.AppError  "status 非法"
// @Failure      404  {object}  apperrors.AppError  "变更单/任务不存在（或任务不属于该单）"
// @Security     Bearer
// @Router       /change-requests/{id}/impact-tasks/{task_id} [post]
func (h *ChangeRequestHandler) UpdateImpactTask(c *gin.Context) {
	ctx := c.Request.Context()
	if forbidden := changeRequestCallerGuard(c); forbidden {
		return
	}
	crID := c.Param("id")
	taskID := c.Param("task_id")
	if crID == "" || taskID == "" {
		c.Error(apperrors.NewBadRequestError("change request id and task id are required"))
		return
	}
	var req updateImpactTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(apperrors.NewValidationError("invalid request body: " + err.Error()))
		return
	}
	task, err := h.changeRequestService.UpdateImpactTask(
		ctx, crID, taskID, types.ImpactTaskStatus(req.Status), req.Note,
	)
	if err != nil {
		writeChangeRequestError(c, ctx, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": task})
}

// changeRequestCallerGuard 校验租户/调用者上下文；缺失时写入 401 并返回 true。
func changeRequestCallerGuard(c *gin.Context) bool {
	ctx := c.Request.Context()
	if types.CallerFromContext(ctx).TenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is missing"))
		return true
	}
	return false
}

// writeChangeRequestError 把变更单的业务哨兵错误映射为 HTTP 语义。映射表：
// 幂等/状态冲突 → 409，关闭门禁与参数/状态机门槛 → 400，资源缺失 → 404，
// 缺租户上下文 → 401，其余记日志后 500（不外泄内部细节）。
// changeVerifyRequest is the body of POST /change-requests/:id/verify.
type changeVerifyRequest struct {
	// Result is the verification conclusion (§7.2 关闭条件②).
	Result string `json:"result"`
}

func writeChangeRequestError(c *gin.Context, ctx context.Context, err error) {
	var blocked *types.ChangeCloseBlockedError
	switch {
	case errors.As(err, &blocked):
		c.Error(apperrors.NewBadRequestError(blocked.Error()))
	case errors.Is(err, service.ErrCRItemAlreadyAdded),
		errors.Is(err, service.ErrCRNotEditable),
		errors.Is(err, service.ErrCRNotAnalyzed),
		errors.Is(err, service.ErrCRNotApproved),
		errors.Is(err, service.ErrCRVerificationRequired),
		errors.Is(err, service.ErrCRInvalidState):
		c.Error(apperrors.NewConflictError(err.Error()))
	case errors.Is(err, service.ErrCRTitleRequired),
		errors.Is(err, service.ErrCRInvalidReason),
		errors.Is(err, service.ErrCRInvalidChangeType),
		errors.Is(err, service.ErrCRInvalidRiskLevel),
		errors.Is(err, service.ErrCRInvalidTaskStatus),
		errors.Is(err, service.ErrCRNoItems):
		c.Error(apperrors.NewBadRequestError(err.Error()))
	case errors.Is(err, service.ErrCRNotFound),
		errors.Is(err, service.ErrCRItemNotFound),
		errors.Is(err, service.ErrCRImpactTaskNotFound),
		errors.Is(err, service.ErrCRKnowledgeNotFound):
		c.Error(apperrors.NewNotFoundError(err.Error()))
	case errors.Is(err, service.ErrCRTenantMissing):
		c.Error(apperrors.NewUnauthorizedError(err.Error()))
	case errors.Is(err, service.ErrCRActorMissing):
		c.Error(apperrors.NewUnauthorizedError(err.Error()))
	default:
		logger.ErrorWithFields(ctx, err, map[string]interface{}{"handler": "change_request"})
		c.Error(apperrors.NewInternalServerError("change request operation failed").WithDetails(err.Error()))
	}
}
