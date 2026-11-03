package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

// KnowledgeLifecycleHandler exposes the lifecycle state machine over HTTP
// (KnowAxis V3.0 §7.1): governance transitions, published-version history and
// the read-time resolved behaviour used by UI badges.
type KnowledgeLifecycleHandler struct {
	lifecycleService interfaces.KnowledgeLifecycleService
}

// NewKnowledgeLifecycleHandler constructs the handler.
func NewKnowledgeLifecycleHandler(
	lifecycleService interfaces.KnowledgeLifecycleService,
) *KnowledgeLifecycleHandler {
	return &KnowledgeLifecycleHandler{lifecycleService: lifecycleService}
}

// lifecycleTransitionRequest is the body of POST /knowledge/:id/lifecycle/transition.
type lifecycleTransitionRequest struct {
	// Action is one of the KnowledgeLifecycleAction constants.
	Action string `json:"action" binding:"required"`
	// ReplacedByID is required for the replace action.
	ReplacedByID string `json:"replaced_by_id"`
	// ChangeNote is stored on the published version snapshot (publish action).
	ChangeNote string `json:"change_note"`
	// ReceiptID links a publish to its approval receipt (filled by the review
	// service; accepted here for direct administrative publishing).
	ReceiptID string `json:"receipt_id"`
	// Classification / EffectiveAt / ExpiresAt / ReviewCycleDays apply to the
	// update_settings action.
	Classification  string     `json:"classification"`
	EffectiveAt     *time.Time `json:"effective_at"`
	ExpiresAt       *time.Time `json:"expires_at"`
	ReviewCycleDays *int       `json:"review_cycle_days"`
}

// lifecycleResponse wraps a knowledge row plus its resolved lifecycle
// behaviour for UI badges.
type lifecycleResponse struct {
	Success  bool                             `json:"success"`
	Data     *types.Knowledge                 `json:"data"`
	Resolved types.KnowledgeLifecycleResolved `json:"resolved"`
}

// TransitionKnowledgeLifecycle godoc
// @Summary      执行知识生命周期动作
// @Description  按状态机应用一个生命周期动作（提交审核/发布/复审确认/标记过期/替代/冲突/下架/归档/恢复/治理配置）。非法迁移返回 400 并记录审计。
// @Tags         知识生命周期
// @Accept       json
// @Produce      json
// @Param        knowledge_id  path  string                     true  "知识ID"
// @Param        request       body  lifecycleTransitionRequest true  "生命周期动作"
// @Success      200  {object}  lifecycleResponse
// @Failure      400  {object}  errors.AppError
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge/{knowledge_id}/lifecycle/transition [post]
func (h *KnowledgeLifecycleHandler) TransitionKnowledgeLifecycle(c *gin.Context) {
	ctx := c.Request.Context()
	knowledgeID := c.Param("id")
	if knowledgeID == "" {
		c.Error(errors.NewBadRequestError("knowledge_id is required"))
		return
	}
	var req lifecycleTransitionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}

	caller := types.CallerFromContext(ctx)
	input := &types.KnowledgeLifecycleTransitionInput{
		KnowledgeID:  knowledgeID,
		Action:       types.KnowledgeLifecycleAction(req.Action),
		ActorID:      caller.UserID,
		ReplacedByID: req.ReplacedByID,
		ChangeNote:   req.ChangeNote,
		ReceiptID:    req.ReceiptID,
		EffectiveAt:  req.EffectiveAt,
		ExpiresAt:    req.ExpiresAt,
	}
	if req.Classification != "" {
		classification := types.ParseKnowledgeClassification(req.Classification)
		input.Classification = &classification
	}
	if req.ReviewCycleDays != nil {
		input.ReviewCycleDays = req.ReviewCycleDays
	}

	knowledge, err := h.lifecycleService.Transition(ctx, input)
	if err != nil {
		c.Error(err)
		return
	}
	resolved := h.lifecycleService.ResolvedForDisplay(knowledge, time.Now())
	c.JSON(http.StatusOK, lifecycleResponse{Success: true, Data: knowledge, Resolved: resolved})
}

// GetKnowledgeLifecycle godoc
// @Summary      查询知识生命周期状态
// @Description  返回知识行与读取时解析出的生效行为（含到期/生效窗口），用于页面徽章与问答提示。
// @Tags         知识生命周期
// @Produce      json
// @Param        knowledge_id  path  string  true  "知识ID"
// @Success      200  {object}  lifecycleResponse
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge/{knowledge_id}/lifecycle [get]
func (h *KnowledgeLifecycleHandler) GetKnowledgeLifecycle(c *gin.Context) {
	ctx := c.Request.Context()
	knowledgeID := c.Param("id")
	knowledge, resolved, err := h.lifecycleService.GetResolved(ctx, knowledgeID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(http.StatusOK, lifecycleResponse{Success: true, Data: knowledge, Resolved: resolved})
}

// ListKnowledgeVersions godoc
// @Summary      知识版本历史
// @Description  返回已发布版本快照（不可变），按版本倒序。
// @Tags         知识生命周期
// @Produce      json
// @Param        knowledge_id  path   string  true   "知识ID"
// @Param        limit         query  int     false  "返回条数，1-200，默认 50"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge/{knowledge_id}/versions [get]
func (h *KnowledgeLifecycleHandler) ListKnowledgeVersions(c *gin.Context) {
	ctx := c.Request.Context()
	knowledgeID := c.Param("id")
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	versions, err := h.lifecycleService.ListVersions(ctx, knowledgeID, limit)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": versions})
}
