package handler

import (
	"encoding/json"
	"strconv"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

// GamificationHandler serves the points/level and learning-path endpoints
// (KnowAxis V3.0 §15.4/§15.5). Path writes are Admin-only (path is a
// tenant-wide onboarding surface), reads and the self-service start/step
// actions are Viewer+; the role floor lives on the routes. /me/points and
// the start/complete-step actions never accept a user id from the request —
// the subject is always the caller, so "本人" holds by construction.
type GamificationHandler struct {
	gamificationService interfaces.GamificationService
}

// NewGamificationHandler constructs the handler.
func NewGamificationHandler(gamificationService interfaces.GamificationService) *GamificationHandler {
	return &GamificationHandler{gamificationService: gamificationService}
}

// gamificationCreatePathRequest is the body of POST /learning-paths.
type gamificationCreatePathRequest struct {
	Name     string                   `json:"name" binding:"required"`
	Audience string                   `json:"audience"`
	Steps    []types.LearningPathStep `json:"steps"`
}

// gamificationUpdatePathRequest is the body of PUT /learning-paths/:id.
// Pointer fields distinguish "not provided" from "".
type gamificationUpdatePathRequest struct {
	Name     *string                   `json:"name"`
	Audience *string                   `json:"audience"`
	Steps    *[]types.LearningPathStep `json:"steps"`
}

// gamificationCompleteStepRequest is the body of
// POST /learning-paths/:id/complete-step.
type gamificationCompleteStepRequest struct {
	StepOrder int `json:"step_order" binding:"required,min=1"`
}

// GetMyPoints godoc
// @Summary      查询我的积分
// @Description  返回当前用户的积分余额、等级（bronze/silver/gold/diamond/expert）与最近流水。
// @Tags         积分与学习路径
// @Produce      json
// @Param        recent  query  int  false  "最近流水条数（默认 20，上限 100）"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /me/points [get]
func (h *GamificationHandler) GetMyPoints(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	recent := gamificationQueryLimit(c.Query("recent"), 20)
	summary, err := h.gamificationService.GetPointsSummary(ctx, caller.TenantID, caller.UserID, recent)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": summary})
}

// Leaderboard godoc
// @Summary      查询积分排行榜
// @Description  返回租户内积分余额降序的前 N 名（rank 从 1 起；v1 返回原始 user_id，脱敏由展示层处理）。
// @Tags         积分与学习路径
// @Produce      json
// @Param        limit  query  int  false  "条数（默认 20，上限 100）"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /me/points/leaderboard [get]
func (h *GamificationHandler) Leaderboard(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	limit := gamificationQueryLimit(c.Query("limit"), 20)
	entries, err := h.gamificationService.Leaderboard(ctx, caller.TenantID, limit)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": entries})
}

// CreatePath godoc
// @Summary      新建学习路径
// @Description  新增一条学习路径（岗位/部门 → 有序知识清单）。steps 为 [{order, title, knowledge_id, description}]，order 从 1 起且唯一，写入时按 order 升序归一。
// @Tags         积分与学习路径
// @Accept       json
// @Produce      json
// @Param        request body gamificationCreatePathRequest true "路径"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {object}  errors.AppError
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /learning-paths [post]
func (h *GamificationHandler) CreatePath(c *gin.Context) {
	ctx := c.Request.Context()
	var req gamificationCreatePathRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	path := &types.LearningPath{
		TenantID:  caller.TenantID,
		Name:      req.Name,
		Audience:  req.Audience,
		Steps:     gamificationStepsPayload(req.Steps),
		CreatedBy: caller.UserID,
	}
	saved, err := h.gamificationService.CreatePath(ctx, path)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": saved})
}

// UpdatePath godoc
// @Summary      更新学习路径
// @Description  按字段更新路径；nil 字段保持不变，steps 替换时整体重新校验（order 唯一、按序归一）。
// @Tags         积分与学习路径
// @Accept       json
// @Produce      json
// @Param        id      path  string                           true  "路径ID"
// @Param        request body  gamificationUpdatePathRequest    true  "更新"
// @Success      200  {object}  map[string]interface{}
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /learning-paths/{id} [put]
func (h *GamificationHandler) UpdatePath(c *gin.Context) {
	ctx := c.Request.Context()
	var req gamificationUpdatePathRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	input := &types.LearningPathUpdate{
		Name:     req.Name,
		Audience: req.Audience,
		Steps:    req.Steps,
	}
	saved, err := h.gamificationService.UpdatePath(ctx, caller.TenantID, c.Param("id"), input, caller.UserID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": saved})
}

// DeletePath godoc
// @Summary      删除学习路径
// @Description  物理删除路径及其全部学习进度（v1：硬删，审计留痕 gamification.path_deleted）。
// @Tags         积分与学习路径
// @Produce      json
// @Param        id  path  string  true  "路径ID"
// @Success      200  {object}  map[string]interface{}
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /learning-paths/{id} [delete]
func (h *GamificationHandler) DeletePath(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	if err := h.gamificationService.DeletePath(ctx, caller.TenantID, c.Param("id"), caller.UserID); err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true})
}

// ListPaths godoc
// @Summary      列出学习路径
// @Description  返回租户内全部学习路径（最新在前）。
// @Tags         积分与学习路径
// @Produce      json
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /learning-paths [get]
func (h *GamificationHandler) ListPaths(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	paths, err := h.gamificationService.ListPaths(ctx, caller.TenantID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": paths})
}

// GetPath godoc
// @Summary      查询单个学习路径
// @Description  按 ID 返回路径（含 steps）。
// @Tags         积分与学习路径
// @Produce      json
// @Param        id  path  string  true  "路径ID"
// @Success      200  {object}  map[string]interface{}
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /learning-paths/{id} [get]
func (h *GamificationHandler) GetPath(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	path, err := h.gamificationService.GetPath(ctx, caller.TenantID, c.Param("id"))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": path})
}

// StartPath godoc
// @Summary      开始学习路径
// @Description  为当前用户创建（或续用）路径进度（幂等：已开始则原样返回）。主题固定为调用者本人。
// @Tags         积分与学习路径
// @Produce      json
// @Param        id  path  string  true  "路径ID"
// @Success      200  {object}  map[string]interface{}
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /learning-paths/{id}/start [post]
func (h *GamificationHandler) StartPath(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	progress, err := h.gamificationService.AssignProgress(ctx, caller.TenantID, c.Param("id"), caller.UserID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": progress})
}

// CompleteStep godoc
// @Summary      完成学习路径步骤
// @Description  记录当前用户完成路径的某一步（按 step_order，幂等）；progress_pct=完成步数/总步数，达 100% 时落 completed_at 并触发 path_completed 加分。主题固定为调用者本人。
// @Tags         积分与学习路径
// @Accept       json
// @Produce      json
// @Param        id      path  string                            true  "路径ID"
// @Param        request body  gamificationCompleteStepRequest   true  "步骤"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {object}  errors.AppError
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /learning-paths/{id}/complete-step [post]
func (h *GamificationHandler) CompleteStep(c *gin.Context) {
	ctx := c.Request.Context()
	var req gamificationCompleteStepRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	progress, err := h.gamificationService.CompleteStep(
		ctx, caller.TenantID, c.Param("id"), caller.UserID, req.StepOrder)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": progress})
}

// gamificationQueryLimit parses a limit query param with the given default,
// clamping negatives up to the default (the service clamps the upper bound).
func gamificationQueryLimit(raw string, defaultValue int) int {
	if raw == "" {
		return defaultValue
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed <= 0 {
		return defaultValue
	}
	return parsed
}

// gamificationStepsPayload converts the request's step slice into the JSONB
// payload, falling back to an empty array so the NOT NULL column always holds
// a JSON array. Kept local to this vertical.
func gamificationStepsPayload(steps []types.LearningPathStep) types.JSON {
	if steps == nil {
		return types.JSON([]byte("[]"))
	}
	data, err := json.Marshal(steps)
	if err != nil {
		return types.JSON([]byte("[]"))
	}
	return types.JSON(data)
}
