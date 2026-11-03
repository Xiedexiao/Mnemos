package handler

import (
	stderrors "errors"
	"net/http"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

// FeedbackHandler exposes the feedback & quality-task vertical
// (KnowAxis V3.0 §13.8 / §14.3 / §14.5):
//
//	POST /messages/:message_id/feedback   — rate one answer (any Viewer+)
//	POST /knowledge/:id/feedback          — report a knowledge entry (Viewer+ with KB read)
//	GET  /quality-tasks?status=           — triage queue (Admin+)
//	POST /quality-tasks/:task_id          — transition / reassign (Admin+)
//
// The caller's tenant and user come from the auth middleware; clients
// cannot pass a tenant_id or user_id. On knowledge feedback the KBAccess
// guard has already rewritten the tenant context to the owner workspace,
// so the task lands with the content's governance owner.
type FeedbackHandler struct {
	feedbackService interfaces.FeedbackService
	taskService     interfaces.QualityTaskService
}

// NewFeedbackHandler constructs the handler.
func NewFeedbackHandler(
	feedbackService interfaces.FeedbackService,
	taskService interfaces.QualityTaskService,
) *FeedbackHandler {
	return &FeedbackHandler{
		feedbackService: feedbackService,
		taskService:     taskService,
	}
}

// feedbackCaller resolves the (tenantID, userID) pair the handler scopes
// every operation to, mirroring the favorites handler's context contract.
func feedbackCaller(c *gin.Context) (uint64, string, bool) {
	tenantID := c.GetUint64(types.TenantIDContextKey.String())
	if tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("workspace ID not found"))
		return 0, "", false
	}
	userID, ok := types.UserIDFromContext(c.Request.Context())
	if !ok || userID == "" {
		c.Error(errors.NewUnauthorizedError("user ID not found"))
		return 0, "", false
	}
	return tenantID, userID, true
}

// RateMessageFeedbackRequest is the body for POST
// /messages/:message_id/feedback.
type RateMessageFeedbackRequest struct {
	// Rating: useful | useless.
	Rating string `json:"rating"`
	// ReasonCodes lists machine-readable reasons (useless ratings).
	ReasonCodes []string `json:"reason_codes"`
	// Comment carries the free-text explanation.
	Comment string `json:"comment"`
}

// RateMessage godoc
// @Summary      评价消息
// @Description  对一条回答记录 useful / useless 评价（同人对同一条消息重复评价为覆盖）。useless 会计入所属知识的近30天无用计数并可能自动生成质量任务。
// @Tags         反馈与质量任务
// @Accept       json
// @Produce      json
// @Param        message_id  path  string                        true  "消息ID"
// @Param        body        body  RateMessageFeedbackRequest    true  "评价内容"
// @Success      200  {object}  handler.SuccessResponse
// @Failure      400  {object}  errors.AppError  "rating 非法"
// @Security     Bearer
// @Router       /messages/{message_id}/feedback [post]
func (h *FeedbackHandler) RateMessage(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, userID, ok := feedbackCaller(c)
	if !ok {
		return
	}
	messageID := c.Param("message_id")

	var req RateMessageFeedbackRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError("invalid request body").WithDetails(err.Error()))
		return
	}

	feedback, err := h.feedbackService.RateMessage(
		ctx, tenantID, messageID, userID,
		types.FeedbackRating(req.Rating), req.ReasonCodes, req.Comment,
	)
	if err != nil {
		if stderrors.Is(err, service.ErrFeedbackInvalidRating) ||
			stderrors.Is(err, service.ErrFeedbackMissingMessage) {
			c.Error(errors.NewBadRequestError(err.Error()))
			return
		}
		c.Error(errors.NewInternalServerError(err.Error()))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": feedback})
}

// ReportKnowledgeFeedbackRequest is the body for POST
// /knowledge/:id/feedback.
type ReportKnowledgeFeedbackRequest struct {
	// TaskType: correction | stale_report.
	TaskType string `json:"task_type"`
	// ChunkID optionally pins the reported passage.
	ChunkID string `json:"chunk_id"`
	// Comment explains the problem.
	Comment string `json:"comment"`
}

// ReportKnowledge godoc
// @Summary      举报知识问题
// @Description  对知识条目提交 correction（内容有误）或 stale_report（内容过期）质量任务，进入管理员处理队列。
// @Tags         反馈与质量任务
// @Accept       json
// @Produce      json
// @Param        id    path  string                              true  "知识ID"
// @Param        body  body  ReportKnowledgeFeedbackRequest      true  "举报内容"
// @Success      200  {object}  handler.SuccessResponse
// @Failure      400  {object}  errors.AppError  "task_type 非法或缺少知识ID"
// @Security     Bearer
// @Router       /knowledge/{id}/feedback [post]
func (h *FeedbackHandler) ReportKnowledge(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, userID, ok := feedbackCaller(c)
	if !ok {
		return
	}
	knowledgeID := c.Param("id")

	var req ReportKnowledgeFeedbackRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError("invalid request body").WithDetails(err.Error()))
		return
	}

	task, err := h.feedbackService.ReportKnowledge(
		ctx, tenantID, knowledgeID, userID,
		types.QualityTaskType(req.TaskType), req.ChunkID, req.Comment,
	)
	if err != nil {
		if stderrors.Is(err, service.ErrKnowledgeFeedbackType) ||
			stderrors.Is(err, service.ErrKnowledgeFeedbackEmptyID) {
			c.Error(errors.NewBadRequestError(err.Error()))
			return
		}
		c.Error(errors.NewInternalServerError(err.Error()))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": task})
}

// ListQualityTasks godoc
// @Summary      质量任务列表
// @Description  返回本空间的质量任务队列，按创建时间倒序。可用 status 过滤（open / processing / done / waived）。
// @Tags         反馈与质量任务
// @Produce      json
// @Param        status  query  string  false  "任务状态过滤：open | processing | done | waived"
// @Success      200  {object}  handler.SuccessResponse
// @Failure      400  {object}  errors.AppError  "status 非法"
// @Security     Bearer
// @Router       /quality-tasks [get]
func (h *FeedbackHandler) ListQualityTasks(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, _, ok := feedbackCaller(c)
	if !ok {
		return
	}

	tasks, err := h.taskService.List(ctx, tenantID, types.QualityTaskStatus(c.Query("status")))
	if err != nil {
		if stderrors.Is(err, service.ErrQualityTaskInvalidStatus) {
			c.Error(errors.NewBadRequestError(err.Error()))
			return
		}
		c.Error(errors.NewInternalServerError(err.Error()))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": tasks})
}

// UpdateQualityTaskRequest is the body for POST /quality-tasks/:task_id.
// Pointer fields distinguish "unset" from zero: only provided fields change.
type UpdateQualityTaskRequest struct {
	// Status: open | processing | done | waived.
	Status *string `json:"status"`
	// AssigneeID reassigns the task (empty string clears the assignee).
	AssigneeID *string `json:"assignee_id"`
	// ResolutionNote — required to close as done; carries the waived reason.
	ResolutionNote *string `json:"resolution_note"`
}

// UpdateQualityTask godoc
// @Summary      处理质量任务
// @Description  变更质量任务的状态 / 负责人 / 处理说明。置为 done 需提供 resolution_note；waived 需在同一字段写明豁免理由；done/waived 可重新打开。
// @Tags         反馈与质量任务
// @Accept       json
// @Produce      json
// @Param        task_id  path  string                       true  "任务ID"
// @Param        body     body  UpdateQualityTaskRequest     true  "变更内容"
// @Success      200  {object}  handler.SuccessResponse
// @Failure      400  {object}  errors.AppError  "非法流转或缺少处理说明"
// @Failure      404  {object}  errors.AppError  "任务不存在"
// @Security     Bearer
// @Router       /quality-tasks/{task_id} [post]
func (h *FeedbackHandler) UpdateQualityTask(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, _, ok := feedbackCaller(c)
	if !ok {
		return
	}
	taskID := c.Param("task_id")

	var req UpdateQualityTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError("invalid request body").WithDetails(err.Error()))
		return
	}

	input := &types.QualityTaskUpdateInput{
		AssigneeID:     req.AssigneeID,
		ResolutionNote: req.ResolutionNote,
	}
	if req.Status != nil {
		status := types.QualityTaskStatus(*req.Status)
		input.Status = &status
	}

	task, err := h.taskService.Update(ctx, tenantID, taskID, input)
	if err != nil {
		switch {
		case stderrors.Is(err, service.ErrQualityTaskNotFound):
			c.Error(errors.NewNotFoundError(err.Error()))
		case stderrors.Is(err, service.ErrQualityTaskInvalidStatus),
			stderrors.Is(err, service.ErrQualityTaskIllegalTransition),
			stderrors.Is(err, service.ErrQualityTaskResolutionNoteRequired):
			c.Error(errors.NewBadRequestError(err.Error()))
		default:
			c.Error(errors.NewInternalServerError(err.Error()))
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": task})
}
