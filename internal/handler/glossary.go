package handler

import (
	"encoding/json"
	goerrors "errors"
	"io"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

// GlossaryHandler serves the controlled-vocabulary endpoints
// (KnowAxis V3.0 §4.4). Writes are Admin-only (术语治理), reads Viewer+;
// the role floor lives on the routes.
type GlossaryHandler struct {
	glossaryService interfaces.GlossaryService
}

// NewGlossaryHandler constructs the handler.
func NewGlossaryHandler(glossaryService interfaces.GlossaryService) *GlossaryHandler {
	return &GlossaryHandler{glossaryService: glossaryService}
}

// createTermRequest is the body of POST /glossary/terms.
type createTermRequest struct {
	StandardName    string   `json:"standard_name" binding:"required"`
	Aliases         []string `json:"aliases"`
	Definition      string   `json:"definition"`
	CounterExamples string   `json:"counter_examples"`
	Domain          string   `json:"domain"`
	OwnerID         string   `json:"owner_id"`
}

// updateTermRequest is the body of PUT /glossary/terms/:id. Pointer fields
// distinguish "not provided" from "".
type updateTermRequest struct {
	StandardName    *string   `json:"standard_name"`
	Aliases         *[]string `json:"aliases"`
	Definition      *string   `json:"definition"`
	CounterExamples *string   `json:"counter_examples"`
	Domain          *string   `json:"domain"`
	OwnerID         *string   `json:"owner_id"`
}

// deleteTermRequest is the optional body of DELETE /glossary/terms/:id: a
// redirect target turns the deprecation into "已废弃 → 见 X".
type deleteTermRequest struct {
	SupersededByID string `json:"superseded_by_id"`
}

// mergeTermsRequest is the body of POST /glossary/terms/merge.
type mergeTermsRequest struct {
	SourceID string `json:"source_id" binding:"required"`
	TargetID string `json:"target_id" binding:"required"`
}

// CreateTerm godoc
// @Summary      新建术语
// @Description  新增一条标准术语（租户内 standard_name 唯一；新术语必须 active）。别名/缩写/英文名放 aliases。
// @Tags         术语表
// @Accept       json
// @Produce      json
// @Param        request body createTermRequest true "术语"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {object}  errors.AppError
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /glossary/terms [post]
func (h *GlossaryHandler) CreateTerm(c *gin.Context) {
	ctx := c.Request.Context()
	var req createTermRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	term := &types.GlossaryTerm{
		TenantID:        caller.TenantID,
		StandardName:    req.StandardName,
		Aliases:         glossaryAliasPayload(req.Aliases),
		Definition:      req.Definition,
		CounterExamples: req.CounterExamples,
		Domain:          req.Domain,
		OwnerID:         req.OwnerID,
		CreatedBy:       caller.UserID,
	}
	saved, err := h.glossaryService.CreateTerm(ctx, term)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": saved})
}

// UpdateTerm godoc
// @Summary      更新术语
// @Description  按字段更新术语；nil 字段保持不变，改名会重新校验租户内唯一。
// @Tags         术语表
// @Accept       json
// @Produce      json
// @Param        id      path  string            true  "术语ID"
// @Param        request body  updateTermRequest true  "更新"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {object}  errors.AppError
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /glossary/terms/{id} [put]
func (h *GlossaryHandler) UpdateTerm(c *gin.Context) {
	ctx := c.Request.Context()
	var req updateTermRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	input := &types.GlossaryTermUpdate{
		StandardName:    req.StandardName,
		Aliases:         req.Aliases,
		Definition:      req.Definition,
		CounterExamples: req.CounterExamples,
		Domain:          req.Domain,
		OwnerID:         req.OwnerID,
	}
	saved, err := h.glossaryService.UpdateTerm(ctx, caller.TenantID, c.Param("id"), input, caller.UserID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": saved})
}

// DeleteTerm godoc
// @Summary      删除术语（软删）
// @Description  删除=置 deprecated 并保留映射，不物理删；body 可带 superseded_by_id 指向接替术语供历史回答重定向。
// @Tags         术语表
// @Accept       json
// @Produce      json
// @Param        id      path  string            true   "术语ID"
// @Param        request body  deleteTermRequest false "重定向（可选）"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {object}  errors.AppError
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /glossary/terms/{id} [delete]
func (h *GlossaryHandler) DeleteTerm(c *gin.Context) {
	ctx := c.Request.Context()
	var req deleteTermRequest
	// body 可选：无 body（io.EOF）视为不带重定向的纯废弃。
	if err := c.ShouldBindJSON(&req); err != nil && !goerrors.Is(err, io.EOF) {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	if err := h.glossaryService.DeleteTerm(ctx, caller.TenantID, c.Param("id"), req.SupersededByID, caller.UserID); err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true})
}

// MergeTerms godoc
// @Summary      合并术语
// @Description  将 source 术语并入 target：source 置 deprecated 且 superseded_by_id=target，保留映射。
// @Tags         术语表
// @Accept       json
// @Produce      json
// @Param        request body mergeTermsRequest true "合并"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {object}  errors.AppError
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /glossary/terms/merge [post]
func (h *GlossaryHandler) MergeTerms(c *gin.Context) {
	ctx := c.Request.Context()
	var req mergeTermsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	dst, err := h.glossaryService.MergeTerms(ctx, caller.TenantID, req.SourceID, req.TargetID, caller.UserID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": dst})
}

// ListTerms godoc
// @Summary      搜索/列出术语
// @Description  keyword 对 standard_name/aliases/definition 模糊匹配；domain 过滤领域；两者均可省略（全量列出）。
// @Tags         术语表
// @Produce      json
// @Param        keyword  query  string  false  "关键词"
// @Param        domain   query  string  false  "领域"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /glossary/terms [get]
func (h *GlossaryHandler) ListTerms(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	terms, err := h.glossaryService.SearchTerms(ctx, caller.TenantID, c.Query("keyword"), c.Query("domain"))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": terms})
}

// GetTerm godoc
// @Summary      查询单个术语
// @Description  按 ID 返回术语（含 deprecated 状态与 superseded_by_id 重定向）。
// @Tags         术语表
// @Produce      json
// @Param        id  path  string  true  "术语ID"  extensions(x-agent=term_id)
// @Success      200  {object}  handler.DataResponse{data=types.GlossaryTerm}
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Security     ApiKeyAuth
// @x-agent      {"id":"glossary_term.get","risk":"read"}
// @x-agent-hint "Reads the workspace's controlled vocabulary entry for one term: its standard name, definition and domain. Use it when a question hinges on what a business term means, so the answer uses the organisation's definition rather than a generic one."
// @Router       /glossary/terms/{id} [get]
func (h *GlossaryHandler) GetTerm(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	term, err := h.glossaryService.GetTerm(ctx, caller.TenantID, c.Param("id"))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": term})
}

// ExpandSynonyms godoc
// @Summary      查询同义词扩展
// @Description  对查询文本分词命中术语后返回其全部别名（供检索同义词扩展；deprecated 术语不参与）。
// @Tags         术语表
// @Produce      json
// @Param        q  query  string  true  "查询文本"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {object}  errors.AppError
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /glossary/expand [get]
func (h *GlossaryHandler) ExpandSynonyms(c *gin.Context) {
	ctx := c.Request.Context()
	q := c.Query("q")
	if q == "" {
		c.Error(errors.NewBadRequestError("q is required"))
		return
	}
	caller := types.CallerFromContext(ctx)
	synonyms, err := h.glossaryService.ExpandSynonyms(ctx, caller.TenantID, q)
	if err != nil {
		c.Error(err)
		return
	}
	if synonyms == nil {
		synonyms = []string{}
	}
	c.JSON(200, gin.H{"success": true, "data": gin.H{"query": q, "synonyms": synonyms}})
}

// glossaryAliasPayload converts the request's string slice into the JSONB
// payload, falling back to an empty array so the NOT NULL column always holds
// a JSON array. Kept local to this vertical.
func glossaryAliasPayload(aliases []string) types.JSON {
	data, err := json.Marshal(aliases)
	if err != nil {
		return types.JSON([]byte("[]"))
	}
	return types.JSON(data)
}
