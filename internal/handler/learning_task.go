package handler

import (
	"encoding/json"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// LearningTaskHandler exposes learning tasks and exams (KnowAxis V3.0 §15.5).
type LearningTaskHandler struct {
	tasks interfaces.LearningTaskService
}

// NewLearningTaskHandler constructs the handler.
func NewLearningTaskHandler(tasks interfaces.LearningTaskService) *LearningTaskHandler {
	return &LearningTaskHandler{tasks: tasks}
}

// jsonArray serialises a string slice into the JSON column, degrading to "[]"
// so a marshal failure cannot block question creation.
func jsonArray(values []string) types.JSON {
	raw, err := json.Marshal(values)
	if err != nil {
		return types.JSON("[]")
	}
	return types.JSON(raw)
}

// ListLearningTasks godoc
// @Summary      我的学习任务
// @Description  返回当前用户的学习任务（§15.5 SOP 变更联动派发的任务在这里）。只读当前登录者。
// @Tags         学习认证
// @Produce      json
// @Param        status  query  string  false  "open|done|waived"
// @Success      200  {object}  map[string]interface{}  "{success, data: [LearningTask]}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /learning-tasks [get]
func (h *LearningTaskHandler) ListLearningTasks(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	caller := types.CallerFromContext(ctx)
	rows, err := h.tasks.List(ctx, tenantID, caller.UserID,
		types.LearningTaskStatus(c.Query("status")))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": rows})
}

// CompleteLearningTask godoc
// @Summary      完成学习任务
// @Description  标记一条学习任务已完成（只能完成自己的任务）。
// @Tags         学习认证
// @Produce      json
// @Param        id  path  string  true  "任务ID"
// @Success      200  {object}  map[string]interface{}  "{success, data: LearningTask}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /learning-tasks/{id}/complete [post]
func (h *LearningTaskHandler) CompleteLearningTask(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	caller := types.CallerFromContext(ctx)
	task, err := h.tasks.Complete(ctx, tenantID, c.Param("id"), caller.UserID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": task})
}

// assignLearningTasksRequest is the body of POST /learning-tasks/assign.
type assignLearningTasksRequest struct {
	KnowledgeID string   `json:"knowledge_id"`
	AssigneeIDs []string `json:"assignee_ids"`
	Source      string   `json:"source"`
}

// AssignLearningTasks godoc
// @Summary      派发学习任务
// @Description  把一条已变更的知识派给指定人员学习（§15.5 SOP 变更联动）。同一 (知识, 人员) 已有未完成任务时不重复派发。
// @Tags         学习认证
// @Accept       json
// @Produce      json
// @Param        request  body  assignLearningTasksRequest  true  "指派"
// @Success      200  {object}  map[string]interface{}  "{success, data: [LearningTask]}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /learning-tasks/assign [post]
func (h *LearningTaskHandler) AssignLearningTasks(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req assignLearningTasksRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	source := types.LearningTaskSource(strings.TrimSpace(req.Source))
	if source == "" {
		source = types.LearningTaskSourceManual
	}
	tasks, err := h.tasks.AssignForKnowledge(ctx, tenantID,
		strings.TrimSpace(req.KnowledgeID), req.AssigneeIDs, source, nil)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": tasks})
}

// createExamQuestionRequest is the body of POST /learning-paths/:path_id/exam-questions.
type createExamQuestionRequest struct {
	Prompt       string   `json:"prompt"`
	Options      []string `json:"options"`
	CorrectIndex int      `json:"correct_index"`
	Explanation  string   `json:"explanation"`
}

// CreateExamQuestion godoc
// @Summary      新增考试题
// @Description  为学习路径添加一道认证考题（§15.5 与考试/上岗资格关联）。
// @Tags         学习认证
// @Accept       json
// @Produce      json
// @Param        path_id  path  string                    true  "学习路径ID"
// @Param        request  body  createExamQuestionRequest true  "题目"
// @Success      200  {object}  map[string]interface{}  "{success, data: LearningExamQuestion}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /learning-paths/{path_id}/exam-questions [post]
func (h *LearningTaskHandler) CreateExamQuestion(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req createExamQuestionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	question, err := h.tasks.CreateExamQuestion(ctx, &types.LearningExamQuestion{
		TenantID:     tenantID,
		PathID:       c.Param("path_id"),
		Prompt:       req.Prompt,
		Options:      jsonArray(req.Options),
		CorrectIndex: req.CorrectIndex,
		Explanation:  req.Explanation,
		CreatedBy:    caller.UserID,
	})
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": question})
}

// ListExamQuestions godoc
// @Summary      考试题库
// @Description  返回某条学习路径的考题。
// @Tags         学习认证
// @Produce      json
// @Param        path_id  path  string  true  "学习路径ID"
// @Success      200  {object}  map[string]interface{}  "{success, data: [LearningExamQuestion]}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /learning-paths/{path_id}/exam-questions [get]
func (h *LearningTaskHandler) ListExamQuestions(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	rows, err := h.tasks.ListExamQuestions(ctx, tenantID, c.Param("path_id"))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": rows})
}

// submitExamRequest is the body of POST /learning-paths/:path_id/exam.
type submitExamRequest struct {
	// Choices maps question_id → selected option index.
	Choices map[string]int `json:"choices"`
}

// SubmitExam godoc
// @Summary      提交考试
// @Description  提交作答并按题库判分（§15.5）。判分只信题库的正确答案，不信提交内容；达到 80 分即通过。
// @Tags         学习认证
// @Accept       json
// @Produce      json
// @Param        path_id  path  string            true  "学习路径ID"
// @Param        request  body  submitExamRequest true  "作答"
// @Success      200  {object}  map[string]interface{}  "{success, data: LearningExamAttempt}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /learning-paths/{path_id}/exam [post]
func (h *LearningTaskHandler) SubmitExam(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req submitExamRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	attempt, err := h.tasks.SubmitExam(ctx, tenantID, c.Param("path_id"), caller.UserID, req.Choices)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": attempt})
}
