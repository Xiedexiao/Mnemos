package handler

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// OfficialAnswerHandler exposes the official-answer lifecycle
// (KnowAxis V3.0 §13.7).
type OfficialAnswerHandler struct {
	answers interfaces.OfficialAnswerService
}

// NewOfficialAnswerHandler constructs the handler.
func NewOfficialAnswerHandler(answers interfaces.OfficialAnswerService) *OfficialAnswerHandler {
	return &OfficialAnswerHandler{answers: answers}
}

// officialAnswerRequest is the body of POST/PUT /official-answers.
type officialAnswerRequest struct {
	ID              string     `json:"id"`
	Question        string     `json:"question"`
	Answer          string     `json:"answer"`
	Conditions      string     `json:"conditions"`
	Exceptions      string     `json:"exceptions"`
	Scope           string     `json:"scope"`
	RiskLevel       string     `json:"risk_level"`
	OwnerID         string     `json:"owner_id"`
	Evidence        []string   `json:"evidence"`
	KnowledgeBaseID string     `json:"knowledge_base_id"`
	EffectiveAt     *time.Time `json:"effective_at"`
	ExpiresAt       *time.Time `json:"expires_at"`
}

// SaveOfficialAnswer godoc
// @Summary      新建/编辑官方答案
// @Description  创建或编辑一条标准答案草稿。已批准或已发布的答案被编辑时会自动退回草稿并清空回执——被改过的正文不能沿用原来的审批（§13.7）。
// @Tags         官方答案
// @Accept       json
// @Produce      json
// @Param        request  body  officialAnswerRequest  true  "官方答案"
// @Success      200  {object}  map[string]interface{}  "{success, data: OfficialAnswer}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /official-answers [post]
func (h *OfficialAnswerHandler) SaveOfficialAnswer(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req officialAnswerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	answer := &types.OfficialAnswer{
		ID:              strings.TrimSpace(req.ID),
		Question:        req.Question,
		Answer:          req.Answer,
		Conditions:      req.Conditions,
		Exceptions:      req.Exceptions,
		Scope:           req.Scope,
		RiskLevel:       req.RiskLevel,
		OwnerID:         req.OwnerID,
		KnowledgeBaseID: req.KnowledgeBaseID,
		EffectiveAt:     req.EffectiveAt,
		ExpiresAt:       req.ExpiresAt,
	}
	answer.SetEvidenceIDs(req.Evidence)
	caller := types.CallerFromContext(ctx)
	saved, err := h.answers.Save(ctx, tenantID, answer, caller.UserID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": saved})
}

// ListOfficialAnswers godoc
// @Summary      列出官方答案
// @Description  返回租户内的官方答案，可按状态过滤（§13.7）。
// @Tags         官方答案
// @Produce      json
// @Param        status  query  string  false  "draft|in_review|approved|published|expired|superseded|retired"
// @Success      200  {object}  map[string]interface{}  "{success, data: [OfficialAnswer]}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /official-answers [get]
func (h *OfficialAnswerHandler) ListOfficialAnswers(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	rows, err := h.answers.List(ctx, tenantID,
		types.OfficialAnswerStatus(c.Query("status")), 0)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": rows})
}

// GetOfficialAnswer godoc
// @Summary      读取官方答案
// @Description  返回单条官方答案，附读取时解析出的生效状态（未到生效日/已过期）。
// @Tags         官方答案
// @Produce      json
// @Param        id  path  string  true  "答案ID"
// @Success      200  {object}  map[string]interface{}  "{success, data: OfficialAnswer}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /official-answers/{id} [get]
func (h *OfficialAnswerHandler) GetOfficialAnswer(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	answer, err := h.answers.Get(ctx, tenantID, c.Param("id"))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": answer})
}

// LookupOfficialAnswer godoc
// @Summary      按问题查官方答案
// @Description  用归一化问题查生效中的官方答案（§13.7）。返回 effective_status 以便调用方区分"未到生效日"与"已过期"；没有匹配时返回 data=null（不是 404）。
// @Tags         官方答案
// @Produce      json
// @Param        question  query  string  true   "问题原文"
// @Param        kb_id     query  string  false  "限定知识库（缺省查全局答案）"
// @Success      200  {object}  map[string]interface{}  "{success, data: OfficialAnswerMatch}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /official-answers/lookup [get]
func (h *OfficialAnswerHandler) LookupOfficialAnswer(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	question := strings.TrimSpace(c.Query("question"))
	if question == "" {
		c.Error(errors.NewBadRequestError("question is required"))
		return
	}
	match, err := h.answers.Lookup(ctx, tenantID, question, c.Query("kb_id"))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": match})
}

// SubmitOfficialAnswerReview godoc
// @Summary      提交官方答案审核
// @Description  草稿 → 待审。没有绑定证据的答案不能提交：无来源的标准答案无法审计或撤回（§13.7）。
// @Tags         官方答案
// @Produce      json
// @Param        id  path  string  true  "答案ID"
// @Success      200  {object}  map[string]interface{}  "{success, data: OfficialAnswer}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /official-answers/{id}/submit [post]
func (h *OfficialAnswerHandler) SubmitOfficialAnswerReview(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	caller := types.CallerFromContext(ctx)
	answer, err := h.answers.SubmitReview(ctx, tenantID, c.Param("id"), caller.UserID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": answer})
}

// approveAnswerRequest is the body of POST /official-answers/:id/approve.
type approveAnswerRequest struct {
	ReceiptID string `json:"receipt_id"`
}

// ApproveOfficialAnswer godoc
// @Summary      批准官方答案
// @Description  待审 → 已批准。高风险答案必须提供审批回执 ID（§13.7 高风险内容禁止绕过审批），且作者不能批准自己写的答案。
// @Tags         官方答案
// @Accept       json
// @Produce      json
// @Param        id       path  string               true  "答案ID"
// @Param        request  body  approveAnswerRequest false "审批回执"
// @Success      200  {object}  map[string]interface{}  "{success, data: OfficialAnswer}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /official-answers/{id}/approve [post]
func (h *OfficialAnswerHandler) ApproveOfficialAnswer(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req approveAnswerRequest
	_ = c.ShouldBindJSON(&req)
	caller := types.CallerFromContext(ctx)
	answer, err := h.answers.Approve(ctx, tenantID, c.Param("id"), caller.UserID, req.ReceiptID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": answer})
}

// PublishOfficialAnswer godoc
// @Summary      发布官方答案
// @Description  已批准 → 已发布。同一问题只能有一份生效答案，冲突返回 409（§13.7）。
// @Tags         官方答案
// @Produce      json
// @Param        id  path  string  true  "答案ID"
// @Success      200  {object}  map[string]interface{}  "{success, data: OfficialAnswer}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /official-answers/{id}/publish [post]
func (h *OfficialAnswerHandler) PublishOfficialAnswer(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	caller := types.CallerFromContext(ctx)
	answer, err := h.answers.Publish(ctx, tenantID, c.Param("id"), caller.UserID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": answer})
}

// retireAnswerRequest is the body of POST /official-answers/:id/retire.
type retireAnswerRequest struct {
	SupersededByID string `json:"superseded_by_id"`
}

// RetireOfficialAnswer godoc
// @Summary      下线/替代官方答案
// @Description  撤回一条官方答案；提供 superseded_by_id 时标记为"被替代"，否则为"已下线"（§13.7 替代/回滚）。
// @Tags         官方答案
// @Accept       json
// @Produce      json
// @Param        id       path  string              true  "答案ID"
// @Param        request  body  retireAnswerRequest false "替代关系"
// @Success      200  {object}  map[string]interface{}  "{success, data: OfficialAnswer}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /official-answers/{id}/retire [post]
func (h *OfficialAnswerHandler) RetireOfficialAnswer(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req retireAnswerRequest
	_ = c.ShouldBindJSON(&req)
	caller := types.CallerFromContext(ctx)
	answer, err := h.answers.Retire(ctx, tenantID, c.Param("id"), caller.UserID, req.SupersededByID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": answer})
}
