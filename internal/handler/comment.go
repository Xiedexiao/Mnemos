package handler

import (
	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

// CommentHandler serves the collaborative discussion endpoints on knowledge
// entries (KnowAxis V3.0 §5.2). Route floors: writes Contributor+ with KB
// write access (the knowledge's KB decides, own/shared alike), reads
// Viewer+ with KB read access. The author-or-Admin moderation rule lives in
// the service, so resolve/delete stay safe for every future caller.
type CommentHandler struct {
	commentService interfaces.CommentService
}

// NewCommentHandler constructs the handler.
func NewCommentHandler(commentService interfaces.CommentService) *CommentHandler {
	return &CommentHandler{commentService: commentService}
}

// addCommentRequest is the body of POST /knowledge/:id/comments. Anchor is
// optional: absent/nil = global comment (not pinned to a passage).
type addCommentRequest struct {
	ParentID string                        `json:"parent_id"`
	Body     string                        `json:"body" binding:"required"`
	Anchor   *types.KnowledgeCommentAnchor `json:"anchor"`
}

// AddComment godoc
// @Summary      添加知识评论
// @Description  在知识条目下创建评论或回复；parent_id 非空表示回复（一层嵌套，父评论必须同属该知识且为顶层）；anchor 为划词锚点（可省略=全局评论）。
// @Tags         知识评论
// @Accept       json
// @Produce      json
// @Param        id      path  string             true  "知识ID"
// @Param        request body  addCommentRequest  true  "评论"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {object}  errors.AppError
// @Failure      403  {object}  errors.AppError
// @Security     Bearer
// @Router       /knowledge/{id}/comments [post]
func (h *CommentHandler) AddComment(c *gin.Context) {
	ctx := c.Request.Context()
	var req addCommentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	comment := &types.KnowledgeComment{
		// The KB-access guard rewrote the context tenant to the effective
		// (knowledge's) tenant, so shared-KB writes land in the right rows.
		TenantID:    types.MustTenantIDFromContext(ctx),
		KnowledgeID: c.Param("id"),
		ParentID:    req.ParentID,
		AuthorID:    caller.UserID,
		Body:        req.Body,
	}
	if err := comment.SetAnchor(req.Anchor); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	saved, err := h.commentService.AddComment(ctx, comment)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": saved})
}

// ListComments godoc
// @Summary      列出知识评论（树形）
// @Description  返回知识条目下的评论线程：顶层评论各带 replies，均按时间正序；软删评论剔除，父评论被删的回复提升为顶层。
// @Tags         知识评论
// @Produce      json
// @Param        id  path  string  true  "知识ID"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Router       /knowledge/{id}/comments [get]
func (h *CommentHandler) ListComments(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID := types.MustTenantIDFromContext(ctx)
	threads, err := h.commentService.ListByKnowledge(ctx, tenantID, c.Param("id"))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": threads})
}

// ResolveComment godoc
// @Summary      标记评论已解决
// @Description  将评论标记为 resolved（记录 resolved_by）；仅作者本人或 Admin。
// @Tags         知识评论
// @Produce      json
// @Param        comment_id  path  string  true  "评论ID"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {object}  errors.AppError  "已解决"
// @Failure      403  {object}  errors.AppError  "非作者且非 Admin"
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Router       /comments/{comment_id}/resolve [post]
func (h *CommentHandler) ResolveComment(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	tenantID := types.MustTenantIDFromContext(ctx)
	if err := h.commentService.ResolveComment(ctx, tenantID, c.Param("comment_id"), caller.UserID); err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true})
}

// DeleteComment godoc
// @Summary      删除评论（软删）
// @Description  软删一条评论（行保留供审计，列表剔除；被删回复的子回复提升为顶层）；仅作者本人或 Admin。
// @Tags         知识评论
// @Produce      json
// @Param        comment_id  path  string  true  "评论ID"
// @Success      200  {object}  map[string]interface{}
// @Failure      403  {object}  errors.AppError  "非作者且非 Admin"
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Router       /comments/{comment_id} [delete]
func (h *CommentHandler) DeleteComment(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	tenantID := types.MustTenantIDFromContext(ctx)
	if err := h.commentService.DeleteComment(ctx, tenantID, c.Param("comment_id"), caller.UserID); err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true})
}
