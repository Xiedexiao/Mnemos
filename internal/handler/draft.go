package handler

import (
	"errors"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/application/service"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// DraftHandler exposes §5.4 draft autosave.
type DraftHandler struct {
	drafts interfaces.DraftService
}

// NewDraftHandler constructs the handler.
func NewDraftHandler(drafts interfaces.DraftService) *DraftHandler {
	return &DraftHandler{drafts: drafts}
}

// saveDraftRequest is the autosave body.
type saveDraftRequest struct {
	Kind             string          `json:"kind"`
	ScopeID          string          `json:"scope_id"`
	Title            string          `json:"title"`
	Body             string          `json:"body"`
	Anchor           types.JSON      `json:"anchor"`
	Mentions         []types.Mention `json:"mentions"`
	ExpectedRevision int             `json:"expected_revision"`
}

// SaveDraft godoc
// @Summary      自动保存草稿
// @Description  自动保存编辑中的内容（KnowAxis V3.0 §5.4）。带 expected_revision 时若服务端已前进则返回 409 与双方内容，由前端合并——不做静默覆盖。
// @Tags         草稿
// @Accept       json
// @Produce      json
// @Param        request  body  saveDraftRequest  true  "草稿"
// @Success      200  {object}  map[string]interface{}  "{success, data: Draft}"
// @Failure      409  {object}  map[string]interface{}  "{success:false, data:{current, submitted}}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /drafts [put]
func (h *DraftHandler) SaveDraft(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req saveDraftRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(apperrors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	input := types.DraftSaveInput{
		Kind:             req.Kind,
		ScopeID:          strings.TrimSpace(req.ScopeID),
		Title:            req.Title,
		Body:             req.Body,
		Anchor:           req.Anchor,
		Mentions:         req.Mentions,
		ExpectedRevision: req.ExpectedRevision,
	}
	draft, err := h.drafts.Save(ctx, tenantID, caller.UserID, input)
	if err != nil {
		if errors.Is(err, service.ErrDraftConflict) {
			// 409 带双方内容：前端据此合并，而不是覆盖。
			c.JSON(409, gin.H{
				"success": false,
				"error":   "draft revision conflict",
				"data":    types.DraftConflict{Current: draft, Submitted: input},
			})
			return
		}
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": draft})
}

// GetDraft godoc
// @Summary      读取草稿
// @Description  读取本人某一份草稿；不存在时返回 null 而不是 404（编辑器据此判断是新建还是续写）。
// @Tags         草稿
// @Produce      json
// @Param        kind      query  string  true   "knowledge/comment/answer"
// @Param        scope_id  query  string  false  "范围 ID"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /drafts [get]
func (h *DraftHandler) GetDraft(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	kind := types.DraftKind(strings.TrimSpace(c.Query("kind")))
	if !kind.Valid() {
		c.Error(apperrors.NewBadRequestError("unknown draft kind"))
		return
	}
	caller := types.CallerFromContext(ctx)
	draft, err := h.drafts.Get(ctx, tenantID, caller.UserID, kind, strings.TrimSpace(c.Query("scope_id")))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": draft})
}

// ListDrafts godoc
// @Summary      我的草稿
// @Description  列出本人的草稿（按更新时间倒序）。
// @Tags         草稿
// @Produce      json
// @Param        kind   query  string  false  "类型过滤"
// @Param        limit  query  int     false  "条数（默认 50）"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /drafts/list [get]
func (h *DraftHandler) ListDrafts(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if err != nil || limit <= 0 || limit > 200 {
		limit = 50
	}
	caller := types.CallerFromContext(ctx)
	drafts, listErr := h.drafts.List(ctx, tenantID, caller.UserID, strings.TrimSpace(c.Query("kind")), limit)
	if listErr != nil {
		c.Error(listErr)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": drafts})
}

// DeleteDraft godoc
// @Summary      删除草稿
// @Description  删除一份草稿（提交后清理，或用户主动丢弃）。
// @Tags         草稿
// @Produce      json
// @Param        id  path  string  true  "草稿 ID"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /drafts/{id} [delete]
func (h *DraftHandler) DeleteDraft(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	caller := types.CallerFromContext(ctx)
	if err := h.drafts.Delete(ctx, tenantID, caller.UserID, c.Param("id")); err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true})
}
