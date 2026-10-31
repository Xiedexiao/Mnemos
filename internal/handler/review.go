package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/Tencent/WeKnora/internal/application/service"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

// ReviewHandler 暴露知识审核流（KnowAxis V3.0 §6 的 v1 子集）：
// 提交审核（KB 内容写边界）、审批待办与裁决（租户治理面，Admin+）、
// 审批回执查看与哈希链回放。租户/用户身份一律从 types.CallerFromContext
// 取（路由层的 RBAC + KBAccess 守卫保证其可信）。
type ReviewHandler struct {
	reviewService interfaces.ReviewService
}

// NewReviewHandler constructs the review handler.
func NewReviewHandler(reviewService interfaces.ReviewService) *ReviewHandler {
	return &ReviewHandler{reviewService: reviewService}
}

// reviewKnowledgeIDParam 兼容两种挂载：/knowledge/:knowledge_id/... 与
// /knowledge/:id/...。gin 的路由树在同一位置只允许一个通配符名，本仓库
// /knowledge 既有路由统一用 :id（routes_knowledge.go），因此提交路由复用
// :id；handler 双读保持对两种挂载都可用。
func reviewKnowledgeIDParam(c *gin.Context) string {
	if v := c.Param("knowledge_id"); v != "" {
		return v
	}
	return c.Param("id")
}

// SubmitReview godoc
// @Summary      提交知识进入审核
// @Description  冻结知识当前内容基线（content hash + chunk 数 + 标题 + custom_metadata 摘要），按密级路由风险级（confidential→high、secret→mid、其余 low），创建审批任务并把知识推进到 pending_review。同一知识已有待审任务时返回 409。
// @Tags         知识审核
// @Produce      json
// @Param        id  path  string  true  "知识ID"  extensions(x-agent=knowledge_id)
// @Success      200  {object}  map[string]interface{}  "{success, data: 审批任务}"
// @Failure      400  {object}  errors.AppError  "知识当前状态不允许提交审核"
// @Failure      401  {object}  errors.AppError  "缺少租户上下文"
// @Failure      404  {object}  errors.AppError  "知识不存在"
// @Failure      409  {object}  errors.AppError  "该知识已有待审任务"
// @Security     Bearer
// @Security     ApiKeyAuth
// @x-agent      {"id":"knowledge.review_submit","risk":"write","role":"contributor"}
// @Router       /knowledge/{id}/review/submit [post]
func (h *ReviewHandler) SubmitReview(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	if caller.TenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is missing"))
		return
	}
	knowledgeID := reviewKnowledgeIDParam(c)
	if knowledgeID == "" {
		c.Error(apperrors.NewBadRequestError("knowledge id is required"))
		return
	}

	task, err := h.reviewService.SubmitReview(ctx, knowledgeID, caller.UserID)
	if err != nil {
		writeReviewError(c, ctx, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": task})
}

// ListTasks godoc
// @Summary      获取审批待办列表
// @Description  返回本租户全部待审任务（v1：Admin+ 看全量待办池，按提交时间升序）。
// @Tags         知识审核
// @Produce      json
// @Success      200  {object}  map[string]interface{}  "{success, data: [审批任务]}"
// @Failure      401  {object}  errors.AppError  "缺少租户上下文"
// @Security     Bearer
// @x-agent      {"id":"review.list_tasks","risk":"read","role":"admin"}
// @Router       /review/tasks [get]
func (h *ReviewHandler) ListTasks(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	if caller.TenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is missing"))
		return
	}

	tasks, err := h.reviewService.ListPendingTasks(ctx, caller.UserID)
	if err != nil {
		writeReviewError(c, ctx, err)
		return
	}
	if tasks == nil {
		tasks = []*types.ApprovalTask{}
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": tasks})
}

// decideReviewRequest 是审批裁决的请求体。decision 用 oneof 白名单，
// reason_code 语义校验（驳回必填 + 枚举）在 service 层。
type decideReviewRequest struct {
	Decision   string `json:"decision"    binding:"required,oneof=approved rejected"`
	ReasonCode string `json:"reason_code" binding:"omitempty,max=32"`
	Comment    string `json:"comment"     binding:"max=2000"`
}

// Decide godoc
// @Summary      审批裁决
// @Description  审批人对待审任务投一票。四眼校验（不得审批自己提交的任务）；驳回立即退回草稿；通过票数达到法定人数（dual 模式或 high 风险需 2 个不同审批人）才生成回执并发布。
// @Tags         知识审核
// @Accept       json
// @Produce      json
// @Param        task_id  path  string  true  "任务ID"
// @Param        request  body  handler.decideReviewRequest  true  "{decision: approved|rejected, reason_code?, comment?}"
// @Success      200  {object}  map[string]interface{}  "{success, data: 审批任务}"
// @Failure      400  {object}  errors.AppError  "参数/原因码非法"
// @Failure      403  {object}  errors.AppError  "四眼校验失败：不能审批自己提交的任务"
// @Failure      404  {object}  errors.AppError  "任务不存在"
// @Failure      409  {object}  errors.AppError  "任务已不在待审状态"
// @Security     Bearer
// @x-agent      {"id":"review.decide","risk":"write","role":"admin"}
// @Router       /review/tasks/{task_id}/decide [post]
func (h *ReviewHandler) Decide(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	if caller.TenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is missing"))
		return
	}
	taskID := c.Param("task_id")
	if taskID == "" {
		c.Error(apperrors.NewBadRequestError("task id is required"))
		return
	}

	var req decideReviewRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(apperrors.NewValidationError("invalid request body: " + err.Error()))
		return
	}

	task, err := h.reviewService.Decide(
		ctx, taskID, caller.UserID,
		types.ApprovalDecision(req.Decision),
		types.ApprovalReasonCode(req.ReasonCode),
		req.Comment,
	)
	if err != nil {
		writeReviewError(c, ctx, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": task})
}

// GetReceipt godoc
// @Summary      获取单条审批回执
// @Description  按回执 ID 返回审批回执（发布凭据 + 内容基线快照 + 哈希链锚点）。
// @Tags         知识审核
// @Produce      json
// @Param        receipt_id  path  string  true  "回执ID"
// @Success      200  {object}  map[string]interface{}  "{success, data: 回执}"
// @Failure      401  {object}  errors.AppError  "缺少租户上下文"
// @Failure      404  {object}  errors.AppError  "回执不存在"
// @Security     Bearer
// @x-agent      {"id":"review.get_receipt","risk":"read","role":"viewer"}
// @Router       /review/receipts/{receipt_id} [get]
func (h *ReviewHandler) GetReceipt(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	if caller.TenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is missing"))
		return
	}
	receiptID := c.Param("receipt_id")
	if receiptID == "" {
		c.Error(apperrors.NewBadRequestError("receipt id is required"))
		return
	}

	receipt, err := h.reviewService.GetReceipt(ctx, receiptID)
	if err != nil {
		writeReviewError(c, ctx, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": receipt})
}

// GetReceiptChain godoc
// @Summary      审批回执链回放
// @Description  按创建时间倒序返回本租户审批回执哈希链（从链尾向链首逐行验证 prev_hash）。limit 默认 200，硬上限 200。
// @Tags         知识审核
// @Produce      json
// @Param        limit  query  int  false  "条数，1-200"  default(200)  extensions(x-agent)
// @Success      200  {object}  map[string]interface{}  "{success, data: [回执]}"
// @Failure      401  {object}  errors.AppError  "缺少租户上下文"
// @Security     Bearer
// @x-agent      {"id":"review.receipt_chain","risk":"read","role":"admin"}
// @Router       /review/receipts [get]
func (h *ReviewHandler) GetReceiptChain(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	if caller.TenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is missing"))
		return
	}
	// limit 解析容错：非法值交给服务层钳制，与 audit-log 游标同风格。
	limit := 0
	if raw := c.Query("limit"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			limit = v
		}
	}

	receipts, err := h.reviewService.GetReceiptChain(ctx, caller.TenantID, limit)
	if err != nil {
		writeReviewError(c, ctx, err)
		return
	}
	if receipts == nil {
		receipts = []*types.ApprovalReceipt{}
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": receipts})
}

// writeReviewError 把审核流的业务哨兵错误映射为 HTTP 语义。映射表：
// 幂等/状态冲突 → 409，四眼校验 → 403，参数与状态机门槛 → 400，
// 资源缺失 → 404，缺租户上下文 → 401，其余记日志后 500（不外泄内部细节）。
func writeReviewError(c *gin.Context, ctx context.Context, err error) {
	switch {
	case errors.Is(err, service.ErrReviewTaskAlreadyPending),
		errors.Is(err, service.ErrReviewTaskNotPending):
		c.Error(apperrors.NewConflictError(err.Error()))
	case errors.Is(err, service.ErrReviewSelfDecision):
		c.Error(apperrors.NewForbiddenError(err.Error()))
	case errors.Is(err, service.ErrReviewInvalidDecision),
		errors.Is(err, service.ErrReviewReasonRequired),
		errors.Is(err, service.ErrReviewInvalidReasonCode),
		errors.Is(err, service.ErrReviewKnowledgeNotReady):
		c.Error(apperrors.NewBadRequestError(err.Error()))
	case errors.Is(err, service.ErrReviewKnowledgeNotFound),
		errors.Is(err, service.ErrReviewTaskNotFound),
		errors.Is(err, service.ErrReviewReceiptNotFound):
		c.Error(apperrors.NewNotFoundError(err.Error()))
	case errors.Is(err, service.ErrReviewTenantMissing):
		c.Error(apperrors.NewUnauthorizedError(err.Error()))
	default:
		logger.ErrorWithFields(ctx, err, map[string]interface{}{"handler": "review"})
		c.Error(apperrors.NewInternalServerError("review operation failed").WithDetails(err.Error()))
	}
}
