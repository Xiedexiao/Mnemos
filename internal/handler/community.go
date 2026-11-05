package handler

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// CommunityHandler exposes the knowledge Q&A community (KnowAxis V3.0 §15.6).
type CommunityHandler struct {
	community interfaces.CommunityService
}

// NewCommunityHandler constructs the handler.
func NewCommunityHandler(community interfaces.CommunityService) *CommunityHandler {
	return &CommunityHandler{community: community}
}

// askQuestionRequest is the body of POST /community/questions.
type askQuestionRequest struct {
	Title       string `json:"title"`
	Body        string `json:"body"`
	KnowledgeID string `json:"knowledge_id"`
	ExpertID    string `json:"expert_id"`
	Bounty      int    `json:"bounty"`
}

// AskQuestion godoc
// @Summary      提问
// @Description  在知识问答社区提问（KnowAxis V3.0 §15.6），可关联一篇知识、@一位专家、设置积分悬赏。
// @Tags         知识问答社区
// @Accept       json
// @Produce      json
// @Param        request  body  askQuestionRequest  true  "问题"
// @Success      200  {object}  map[string]interface{}  "{success, data: CommunityQuestion}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /community/questions [post]
func (h *CommunityHandler) AskQuestion(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req askQuestionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	question, err := h.community.Ask(ctx, tenantID, caller.UserID, &types.CommunityQuestion{
		Title:       req.Title,
		Body:        req.Body,
		KnowledgeID: strings.TrimSpace(req.KnowledgeID),
		ExpertID:    strings.TrimSpace(req.ExpertID),
		Bounty:      req.Bounty,
	})
	if err != nil {
		writeCommunityError(c, err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": question})
}

// ListQuestions godoc
// @Summary      问题列表
// @Description  按状态列出社区问题（open/answered/resolved/closed）。
// @Tags         知识问答社区
// @Produce      json
// @Param        status  query  string  false  "状态过滤"
// @Param        limit   query  int     false  "返回条数（默认 50）"
// @Success      200  {object}  map[string]interface{}  "{success, data: [CommunityQuestion]}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /community/questions [get]
func (h *CommunityHandler) ListQuestions(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if err != nil || limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, listErr := h.community.List(ctx, tenantID,
		types.CommunityQuestionStatus(c.Query("status")), limit)
	if listErr != nil {
		writeCommunityError(c, listErr)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": rows})
}

// GetQuestion godoc
// @Summary      问题详情
// @Description  返回问题及其全部回答（已采纳的排在最前）。
// @Tags         知识问答社区
// @Produce      json
// @Param        id  path  string  true  "问题ID"  extensions(x-agent=question_id)
// @Success      200  {object}  handler.DataResponse{data=types.CommunityQuestion}  "{success, data: CommunityQuestion}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @x-agent      {"id":"community_question.get","risk":"read"}
// @x-agent-hint "Reads one community question with its answers, the adopted answer first. Use it to surface how a question was actually answered in practice, including answers not yet in the knowledge base."
// @Router       /community/questions/{id} [get]
func (h *CommunityHandler) GetQuestion(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	question, err := h.community.Get(ctx, tenantID, c.Param("id"))
	if err != nil {
		writeCommunityError(c, err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": question})
}

// answerQuestionRequest is the body of POST /community/questions/:id/answers.
type answerQuestionRequest struct {
	Body string `json:"body"`
}

// AnswerQuestion godoc
// @Summary      回答
// @Description  回答问题并自动把问题状态推进为"已回答"。
// @Tags         知识问答社区
// @Accept       json
// @Produce      json
// @Param        id       path  string                 true  "问题ID"
// @Param        request  body  answerQuestionRequest  true  "回答"
// @Success      200  {object}  map[string]interface{}  "{success, data: CommunityAnswer}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /community/questions/{id}/answers [post]
func (h *CommunityHandler) AnswerQuestion(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req answerQuestionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	answer, err := h.community.Answer(ctx, tenantID, c.Param("id"), caller.UserID, req.Body)
	if err != nil {
		writeCommunityError(c, err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": answer})
}

// VoteAnswer godoc
// @Summary      答案有用
// @Description  给一条回答投"有用"票（§15.6 答案评价）。
// @Tags         知识问答社区
// @Produce      json
// @Param        answer_id  path  string  true  "回答ID"
// @Success      200  {object}  map[string]interface{}  "{success, data: CommunityAnswer}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /community/answers/{answer_id}/vote [post]
func (h *CommunityHandler) VoteAnswer(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	answer, err := h.community.Vote(ctx, tenantID, c.Param("answer_id"))
	if err != nil {
		writeCommunityError(c, err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": answer})
}

// acceptAnswerRequest is the body of POST /community/questions/:id/accept.
type acceptAnswerRequest struct {
	AnswerID string `json:"answer_id"`
}

// AcceptAnswer godoc
// @Summary      采纳答案
// @Description  提问者采纳一条答案（§15.6），问题转为"已解决"。只有提问者可以采纳。
// @Tags         知识问答社区
// @Accept       json
// @Produce      json
// @Param        id       path  string              true  "问题ID"
// @Param        request  body  acceptAnswerRequest true  "回答ID"
// @Success      200  {object}  map[string]interface{}  "{success, data: CommunityQuestion}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /community/questions/{id}/accept [post]
func (h *CommunityHandler) AcceptAnswer(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req acceptAnswerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	question, err := h.community.Accept(ctx, tenantID, c.Param("id"),
		strings.TrimSpace(req.AnswerID), caller.UserID)
	if err != nil {
		writeCommunityError(c, err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": question})
}

// convertQuestionRequest is the body of POST /community/questions/:id/convert.
type convertQuestionRequest struct {
	KnowledgeID string `json:"knowledge_id"`
}

// ConvertQuestion godoc
// @Summary      问答转知识
// @Description  把社区中的优质问答登记为正式知识（§15.6 一键转知识），问题随之关闭。知识本身仍走审核流程。
// @Tags         知识问答社区
// @Accept       json
// @Produce      json
// @Param        id       path  string                 true  "问题ID"
// @Param        request  body  convertQuestionRequest true  "知识ID"
// @Success      200  {object}  map[string]interface{}  "{success, data: CommunityQuestion}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /community/questions/{id}/convert [post]
func (h *CommunityHandler) ConvertQuestion(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req convertQuestionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	if strings.TrimSpace(req.KnowledgeID) == "" {
		c.Error(errors.NewBadRequestError("knowledge_id is required"))
		return
	}
	caller := types.CallerFromContext(ctx)
	question, err := h.community.MarkConverted(ctx, tenantID, c.Param("id"),
		strings.TrimSpace(req.KnowledgeID), caller.UserID)
	if err != nil {
		writeCommunityError(c, err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": question})
}

// reportRequest is the body of POST /community/reports.
type reportRequest struct {
	TargetType string `json:"target_type"`
	TargetID   string `json:"target_id"`
	Reason     string `json:"reason"`
}

// ReportContent godoc
// @Summary      举报内容
// @Description  举报问题或回答（§15.6 治理），进入版主队列。
// @Tags         知识问答社区
// @Accept       json
// @Produce      json
// @Param        request  body  reportRequest  true  "举报"
// @Success      200  {object}  map[string]interface{}  "{success, data: CommunityReport}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /community/reports [post]
func (h *CommunityHandler) ReportContent(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req reportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	report, err := h.community.Report(ctx, tenantID, req.TargetType, req.TargetID,
		req.Reason, caller.UserID)
	if err != nil {
		writeCommunityError(c, err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": report})
}

// ListReports godoc
// @Summary      举报队列
// @Description  版主查看待处理举报（§15.6 治理）。
// @Tags         知识问答社区
// @Produce      json
// @Param        status  query  string  false  "open|dismissed|removed"
// @Success      200  {object}  map[string]interface{}  "{success, data: [CommunityReport]}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /community/reports [get]
func (h *CommunityHandler) ListReports(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	rows, err := h.community.ListReports(ctx, tenantID,
		types.CommunityReportStatus(c.Query("status")), 0)
	if err != nil {
		writeCommunityError(c, err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": rows})
}

// resolveReportRequest is the body of PATCH /community/reports/:id.
type resolveReportRequest struct {
	Status string `json:"status"`
}

// ResolveReport godoc
// @Summary      处理举报
// @Description  版主驳回或移除内容（§15.6 治理）。判定移除时同时关闭被举报的问题。
// @Tags         知识问答社区
// @Accept       json
// @Produce      json
// @Param        id       path  string               true  "举报ID"
// @Param        request  body  resolveReportRequest true  "处置"
// @Success      200  {object}  map[string]interface{}  "{success, data: CommunityReport}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /community/reports/{id} [patch]
func (h *CommunityHandler) ResolveReport(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req resolveReportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	report, err := h.community.ResolveReport(ctx, tenantID, c.Param("id"),
		types.CommunityReportStatus(req.Status), caller.UserID)
	if err != nil {
		writeCommunityError(c, err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": report})
}

// writeCommunityError maps community sentinels onto HTTP semantics.
func writeCommunityError(c *gin.Context, err error) {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "only the asker"):
		c.Error(errors.NewForbiddenError(msg))
	case strings.Contains(msg, "not found"):
		c.Error(errors.NewNotFoundError(msg))
	default:
		c.Error(err)
	}
}
