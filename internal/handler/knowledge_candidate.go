package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Tencent/WeKnora/internal/application/service"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

// KnowledgeCandidateHandler 暴露过程经验候选卡片（KnowAxis V3.0 §10 v1）：
// 创建（Contributor+，手工/未来连接器共用）、查询（Viewer+）与审核发布
// 闭环（确认/发布/驳回/撤回）。租户/用户身份一律从 types.CallerFromContext
// 取（路由层的 RBAC 守卫保证其可信）。
type KnowledgeCandidateHandler struct {
	candidateService interfaces.KnowledgeCandidateService
}

// NewKnowledgeCandidateHandler constructs the knowledge candidate handler.
func NewKnowledgeCandidateHandler(candidateService interfaces.KnowledgeCandidateService) *KnowledgeCandidateHandler {
	return &KnowledgeCandidateHandler{candidateService: candidateService}
}

// createCandidateRequest 是创建候选卡片的请求体。source_type/level/confidence
// 的白名单校验在 service 层（binding 只做长度限制）。
type createCandidateRequest struct {
	SourceType string                             `json:"source_type" binding:"omitempty,max=24"`
	SourceID   string                             `json:"source_id"   binding:"omitempty,max=64"`
	Title      string                             `json:"title"       binding:"required,max=255"`
	Level      string                             `json:"level"       binding:"omitempty,max=8"`
	Confidence string                             `json:"confidence"  binding:"omitempty,max=16"`
	Card       *types.KnowledgeCandidateCard      `json:"card"`
	Evidence   []types.KnowledgeCandidateEvidence `json:"evidence"`
}

// Create godoc
// @Summary      创建过程经验候选卡片
// @Description  创建一张候选卡片（九类来源共用；手工/未来连接器共用入口）。自动做 v1 查重：同租户同标题精确命中标记 duplicate，标题 LIKE 相似命中标记 conflict，否则 unique。
// @Tags         过程经验沉淀
// @Accept       json
// @Produce      json
// @Param        request  body  handler.createCandidateRequest  true  "{title, source_type?, source_id?, level?(L0-L3,默认L1), confidence?(EXTRACTED|INFERRED|AMBIGUOUS,默认AMBIGUOUS), card?, evidence?}"
// @Success      200  {object}  map[string]interface{}  "{success, data: 候选卡片}"
// @Failure      400  {object}  errors.AppError  "参数/来源/层级/置信非法"
// @Failure      401  {object}  errors.AppError  "缺少租户上下文"
// @Security     Bearer
// @x-agent      {"id":"candidate.create","risk":"write","role":"contributor"}
// @Router       /knowledge-candidates [post]
func (h *KnowledgeCandidateHandler) Create(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	if caller.TenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is missing"))
		return
	}

	var req createCandidateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(apperrors.NewValidationError("invalid request body: " + err.Error()))
		return
	}

	candidate, err := h.candidateService.CreateCandidate(ctx, types.KnowledgeCandidateCreateInput{
		SourceType:  types.KnowledgeCandidateSourceType(req.SourceType),
		SourceID:    req.SourceID,
		Title:       req.Title,
		Level:       types.KnowledgeCandidateLevel(req.Level),
		Card:        req.Card,
		Evidence:    req.Evidence,
		Confidence:  types.KnowledgeCandidateConfidence(req.Confidence),
		RequestedBy: caller.UserID,
	})
	if err != nil {
		writeCandidateError(c, ctx, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": candidate})
}

// List godoc
// @Summary      获取候选卡片列表
// @Description  分页返回本租户候选卡片（created_at 倒序），可按 status/level 过滤。
// @Tags         过程经验沉淀
// @Produce      json
// @Param        status  query  string  false  "状态过滤：candidate|in_review|confirmed|published|rejected|withdrawn"
// @Param        level   query  string  false  "层级过滤：L0|L1|L2|L3"
// @Param        limit   query  int     false  "条数，默认50，上限200"
// @Param        offset  query  int     false  "偏移量"
// @Success      200  {object}  map[string]interface{}  "{success, data: {items, total}}"
// @Failure      401  {object}  errors.AppError  "缺少租户上下文"
// @Security     Bearer
// @x-agent      {"id":"candidate.list","risk":"read","role":"viewer"}
// @Router       /knowledge-candidates [get]
func (h *KnowledgeCandidateHandler) List(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	if caller.TenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is missing"))
		return
	}

	filter := types.KnowledgeCandidateListFilter{
		Status: strings.TrimSpace(c.Query("status")),
		Level:  strings.TrimSpace(c.Query("level")),
	}
	if raw := c.Query("limit"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			filter.Limit = v
		}
	}
	if raw := c.Query("offset"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			filter.Offset = v
		}
	}

	candidates, total, err := h.candidateService.ListCandidates(ctx, filter)
	if err != nil {
		writeCandidateError(c, ctx, err)
		return
	}
	if candidates == nil {
		candidates = []*types.KnowledgeCandidate{}
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"items": candidates, "total": total}})
}

// Get godoc
// @Summary      获取单张候选卡片
// @Description  按候选 ID 返回候选卡片（租户隔离）。
// @Tags         过程经验沉淀
// @Produce      json
// @Param        id  path  string  true  "候选ID"
// @Success      200  {object}  map[string]interface{}  "{success, data: 候选卡片}"
// @Failure      401  {object}  errors.AppError  "缺少租户上下文"
// @Failure      404  {object}  errors.AppError  "候选不存在"
// @Security     Bearer
// @x-agent      {"id":"candidate.get","risk":"read","role":"viewer"}
// @Router       /knowledge-candidates/{id} [get]
func (h *KnowledgeCandidateHandler) Get(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	if caller.TenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is missing"))
		return
	}
	candidateID := c.Param("id")
	if candidateID == "" {
		c.Error(apperrors.NewBadRequestError("candidate id is required"))
		return
	}

	candidate, err := h.candidateService.GetCandidate(ctx, candidateID)
	if err != nil {
		writeCandidateError(c, ctx, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": candidate})
}

// Confirm godoc
// @Summary      确认推进候选卡片
// @Description  状态机推进：candidate→in_review 由提交人触发（提交人本人或 Admin）；in_review→confirmed 由审核人触发（v1 审核人 = Admin+，L1→L2 晋级）。重复/越权返回 403/409。
// @Tags         过程经验沉淀
// @Produce      json
// @Param        id  path  string  true  "候选ID"
// @Success      200  {object}  map[string]interface{}  "{success, data: 候选卡片}"
// @Failure      400  {object}  errors.AppError  "缺少候选ID"
// @Failure      403  {object}  errors.AppError  "非提交人本人且非 Admin（或审核人不足 Admin）"
// @Failure      404  {object}  errors.AppError  "候选不存在"
// @Failure      409  {object}  errors.AppError  "状态机不允许该动作"
// @Security     Bearer
// @x-agent      {"id":"candidate.confirm","risk":"write","role":"admin"}
// @Router       /knowledge-candidates/{id}/confirm [post]
func (h *KnowledgeCandidateHandler) Confirm(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	if caller.TenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is missing"))
		return
	}
	candidateID := c.Param("id")
	if candidateID == "" {
		c.Error(apperrors.NewBadRequestError("candidate id is required"))
		return
	}

	candidate, err := h.candidateService.ConfirmCandidate(ctx, candidateID, caller.UserID)
	if err != nil {
		writeCandidateError(c, ctx, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": candidate})
}

// publishCandidateRequest 是发布候选的请求体。kb_id 可选：提供时校验存在
// 并把生成的 manual 知识落入该 KB；为空生成未归档知识行。
type publishCandidateRequest struct {
	KBID string `json:"kb_id" binding:"omitempty,max=36"`
}

// Publish godoc
// @Summary      发布候选卡片为正式知识
// @Description  confirmed→published（§10.4 门禁：level=L3 需 Admin 且 confidence!=AMBIGUOUS，否则 400）。创建 manual 正式知识（title=候选标题、content=卡片字段拼 Markdown、lifecycle 默认 valid、level 写 CustomMetadata），knowledge_id 回填候选。
// @Tags         过程经验沉淀
// @Accept       json
// @Produce      json
// @Param        id       path  string                        true  "候选ID"
// @Param        request  body  handler.publishCandidateRequest  false "{kb_id?}"
// @Success      200  {object}  map[string]interface{}  "{success, data: 候选卡片（含 knowledge_id）}"
// @Failure      400  {object}  errors.AppError  "L3 门禁不满足/缺少候选ID"
// @Failure      401  {object}  errors.AppError  "缺少租户上下文"
// @Failure      404  {object}  errors.AppError  "候选或目标 KB 不存在"
// @Failure      409  {object}  errors.AppError  "状态机不允许该动作"
// @Security     Bearer
// @x-agent      {"id":"candidate.publish","risk":"write","role":"admin"}
// @Router       /knowledge-candidates/{id}/publish [post]
func (h *KnowledgeCandidateHandler) Publish(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	if caller.TenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is missing"))
		return
	}
	candidateID := c.Param("id")
	if candidateID == "" {
		c.Error(apperrors.NewBadRequestError("candidate id is required"))
		return
	}

	var req publishCandidateRequest
	// 发布体可选：空 body 视为不指定目标 KB。
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(apperrors.NewValidationError("invalid request body: " + err.Error()))
		return
	}

	candidate, err := h.candidateService.PublishCandidate(ctx, candidateID, caller.UserID, req.KBID)
	if err != nil {
		writeCandidateError(c, ctx, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": candidate})
}

// rejectCandidateRequest 是审核驳回的请求体。意见可选（v1 不强制原因码）。
type rejectCandidateRequest struct {
	Comment string `json:"comment" binding:"max=2000"`
}

// Reject godoc
// @Summary      审核驳回候选卡片
// @Description  in_review→rejected（终态），记录审核人与意见。
// @Tags         过程经验沉淀
// @Accept       json
// @Produce      json
// @Param        id       path  string                        true  "候选ID"
// @Param        request  body  handler.rejectCandidateRequest  false "{comment?}"
// @Success      200  {object}  map[string]interface{}  "{success, data: 候选卡片}"
// @Failure      400  {object}  errors.AppError  "缺少候选ID"
// @Failure      404  {object}  errors.AppError  "候选不存在"
// @Failure      409  {object}  errors.AppError  "状态机不允许该动作"
// @Security     Bearer
// @x-agent      {"id":"candidate.reject","risk":"write","role":"admin"}
// @Router       /knowledge-candidates/{id}/reject [post]
func (h *KnowledgeCandidateHandler) Reject(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	if caller.TenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is missing"))
		return
	}
	candidateID := c.Param("id")
	if candidateID == "" {
		c.Error(apperrors.NewBadRequestError("candidate id is required"))
		return
	}

	var req rejectCandidateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(apperrors.NewValidationError("invalid request body: " + err.Error()))
		return
	}

	candidate, err := h.candidateService.RejectCandidate(ctx, candidateID, caller.UserID, req.Comment)
	if err != nil {
		writeCandidateError(c, ctx, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": candidate})
}

// Withdraw godoc
// @Summary      撤回候选卡片
// @Description  提交人撤回（candidate|in_review→withdrawn；终态）。仅提交人本人可撤。
// @Tags         过程经验沉淀
// @Produce      json
// @Param        id  path  string  true  "候选ID"
// @Success      200  {object}  map[string]interface{}  "{success, data: 候选卡片}"
// @Failure      400  {object}  errors.AppError  "缺少候选ID"
// @Failure      403  {object}  errors.AppError  "仅提交人本人可撤回"
// @Failure      404  {object}  errors.AppError  "候选不存在"
// @Failure      409  {object}  errors.AppError  "状态机不允许该动作"
// @Security     Bearer
// @x-agent      {"id":"candidate.withdraw","risk":"write","role":"contributor"}
// @Router       /knowledge-candidates/{id}/withdraw [post]
func (h *KnowledgeCandidateHandler) Withdraw(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	if caller.TenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is missing"))
		return
	}
	candidateID := c.Param("id")
	if candidateID == "" {
		c.Error(apperrors.NewBadRequestError("candidate id is required"))
		return
	}

	candidate, err := h.candidateService.WithdrawCandidate(ctx, candidateID, caller.UserID)
	if err != nil {
		writeCandidateError(c, ctx, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": candidate})
}

// writeCandidateError 把候选卡片的业务哨兵错误映射为 HTTP 语义。映射表：
// 状态机冲突 → 409，身份门槛 → 403，L3 门禁/参数问题 → 400，资源缺失 →
// 404，缺租户上下文 → 401，其余记日志后 500（不外泄内部细节）。
func writeCandidateError(c *gin.Context, ctx context.Context, err error) {
	switch {
	case errors.Is(err, service.ErrCandidateInvalidState):
		c.Error(apperrors.NewConflictError(err.Error()))
	case errors.Is(err, service.ErrCandidateForbidden),
		errors.Is(err, service.ErrCandidateNotSubmitter):
		c.Error(apperrors.NewForbiddenError(err.Error()))
	case errors.Is(err, service.ErrCandidateTitleRequired),
		errors.Is(err, service.ErrCandidateTitleTooLong),
		errors.Is(err, service.ErrCandidateInvalidSourceType),
		errors.Is(err, service.ErrCandidateInvalidLevel),
		errors.Is(err, service.ErrCandidateInvalidConfidence),
		errors.Is(err, service.ErrCandidateActorMissing),
		errors.Is(err, service.ErrCandidateLevelGate):
		c.Error(apperrors.NewBadRequestError(err.Error()))
	case errors.Is(err, service.ErrCandidateNotFound),
		errors.Is(err, service.ErrCandidateKBNotFound):
		c.Error(apperrors.NewNotFoundError(err.Error()))
	case errors.Is(err, service.ErrCandidateSourceUnavailable):
		// 派生权限无法证明不比源证据更宽（§2.4）：这是拒绝发布的业务冲突，
		// 不是服务端故障。
		c.Error(apperrors.NewConflictError(err.Error()))
	case errors.Is(err, service.ErrCandidateTenantMissing):
		c.Error(apperrors.NewUnauthorizedError(err.Error()))
	default:
		logger.ErrorWithFields(ctx, err, map[string]interface{}{"handler": "knowledge_candidate"})
		c.Error(apperrors.NewInternalServerError("knowledge candidate operation failed").WithDetails(err.Error()))
	}
}
