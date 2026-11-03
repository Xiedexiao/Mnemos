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

// AIPreReviewHandler 暴露 AI 预审（KnowAxis V3.0 §6.5）：
//   - POST /review/tasks/:task_id/pre-review  触发一次预审，返回检查清单；
//   - GET  /review/tasks/:task_id/pre-review  读历史运行（最新在前）。
//
// 两个端点都是 Admin+ 的治理面（与 /review/tasks/:task_id/decide 同档）。
// 身份一律从 types.CallerFromContext 取，租户由路由层 RBAC 守卫保证可信。
//
// 注意该接口的语义边界：预审只产出提示，从不返回"通过/不通过"，也不推进
// 审批状态 —— 审批人仍必须在 decide 上显式表态。
type AIPreReviewHandler struct {
	preReviewService interfaces.AIPreReviewService
}

// NewAIPreReviewHandler constructs the AI pre-review handler.
func NewAIPreReviewHandler(preReviewService interfaces.AIPreReviewService) *AIPreReviewHandler {
	return &AIPreReviewHandler{preReviewService: preReviewService}
}

// RunPreReview godoc
// @Summary      触发 AI 预审
// @Description  对一次审批任务执行 AI 预审（KnowAxis V3.0 §6.5），返回"检查清单 + 风险提示"：完整性/引用/冲突/敏感/表述/术语/时效/重复八项。每条提示带 evidence（依据原文）、locator（位置）与 suggestion（修改建议）；不返回通过/不通过，也不改变审批状态。规则类检查始终执行；模型类检查（引用/冲突）不可用时降级为一条 info 提示。
// @Tags         知识审核
// @Produce      json
// @Param        task_id  path  string  true  "审批任务ID"
// @Success      200  {object}  map[string]interface{}  "{success, task_id, data: [PreReviewFinding], summary: PreReviewSummary}"
// @Failure      400  {object}  apperrors.AppError  "knowledge_id 与任务不一致"
// @Failure      401  {object}  apperrors.AppError  "缺少租户上下文"
// @Failure      404  {object}  apperrors.AppError  "任务或知识不存在"
// @Security     Bearer
// @Router       /review/tasks/{task_id}/pre-review [post]
func (h *AIPreReviewHandler) RunPreReview(c *gin.Context) {
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
	// knowledge_id 为可选 query 参数：默认取任务上的知识。显式传入时必须与
	// 任务一致（service 校验），避免预审结论挂到别的任务上。
	knowledgeID := c.Query("knowledge_id")

	findings, err := h.preReviewService.RunPreReview(ctx, caller.TenantID, taskID, knowledgeID)
	if err != nil {
		writeAIPreReviewError(c, ctx, err)
		return
	}
	if findings == nil {
		findings = []types.PreReviewFinding{}
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"task_id": taskID,
		"data":    findings,
		"summary": types.SummarizePreReviewFindings(findings),
	})
}

// ListRuns godoc
// @Summary      获取 AI 预审历史
// @Description  返回该审批任务的历史预审运行（最新在前），每次运行含 findings、模型版本与 Prompt 版本，供审批人对比改稿前后提示的差异与追溯口径。
// @Tags         知识审核
// @Produce      json
// @Param        task_id  path   string  true   "审批任务ID"
// @Param        limit    query  int     false  "条数，1-50，默认 20"
// @Success      200  {object}  map[string]interface{}  "{success, task_id, data: [AIPreReviewRun]}"
// @Failure      401  {object}  apperrors.AppError  "缺少租户上下文"
// @Failure      404  {object}  apperrors.AppError  "任务不存在"
// @Security     Bearer
// @Router       /review/tasks/{task_id}/pre-review [get]
func (h *AIPreReviewHandler) ListRuns(c *gin.Context) {
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
	// limit 解析容错：非法值交给服务层/仓储侧钳制，与 review 的 limit 同风格。
	limit := 0
	if raw := c.Query("limit"); raw != "" {
		if v, convErr := strconv.Atoi(raw); convErr == nil && v > 0 {
			limit = v
		}
	}

	runs, err := h.preReviewService.ListRuns(ctx, caller.TenantID, taskID, limit)
	if err != nil {
		writeAIPreReviewError(c, ctx, err)
		return
	}
	if runs == nil {
		runs = []*types.AIPreReviewRun{}
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "task_id": taskID, "data": runs})
}

// writeAIPreReviewError 把预审的业务哨兵错误映射为 HTTP 语义。
// 映射表：缺租户 → 401；任务/知识不存在 → 404；知识不匹配 → 400；
// 其余记日志后 500（不外泄内部细节）。
func writeAIPreReviewError(c *gin.Context, ctx context.Context, err error) {
	switch {
	case errors.Is(err, service.ErrAIPreReviewTenantMissing):
		c.Error(apperrors.NewUnauthorizedError(err.Error()))
	case errors.Is(err, service.ErrAIPreReviewTaskNotFound),
		errors.Is(err, service.ErrAIPreReviewKnowledgeNotFound):
		c.Error(apperrors.NewNotFoundError(err.Error()))
	case errors.Is(err, service.ErrAIPreReviewKnowledgeMismatch):
		c.Error(apperrors.NewBadRequestError(err.Error()))
	default:
		logger.ErrorWithFields(ctx, err, map[string]interface{}{"handler": "ai_pre_review"})
		c.Error(apperrors.NewInternalServerError("ai pre-review operation failed").WithDetails(err.Error()))
	}
}
