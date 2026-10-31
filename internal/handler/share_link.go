package handler

import (
	"net/http"
	"time"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

// ShareLinkHandler exposes knowledge external share links (KnowAxis V3.0
// §2.5 v1):
//
//	POST   /knowledge/:id/share-links   issue a link (Contributor + KB write)
//	GET    /knowledge/:id/share-links   list a document's links (Contributor + KB write)
//	DELETE /share-links/:share_link_id            revoke a link (Contributor, tenant-scoped)
//	GET    /public/share/:token                   anonymous resolve (no auth)
//
// The public resolve returns a hand-built read-only projection
// (types.ShareLinkResolution) — types.Knowledge is never serialised to a
// visitor, so tenant-internal columns cannot leak. The visitor watermark
// is rendered by the frontend from the share metadata (token + visited_at).
type ShareLinkHandler struct {
	shareService interfaces.ShareLinkService
}

// NewShareLinkHandler constructs the handler.
func NewShareLinkHandler(shareService interfaces.ShareLinkService) *ShareLinkHandler {
	return &ShareLinkHandler{shareService: shareService}
}

// createShareLinkRequest is the body of POST /knowledge/:id/share-links.
type createShareLinkRequest struct {
	// LinkType is the audience: "org" or "people".
	LinkType string `json:"link_type" binding:"required"`
	// Permission is the capability: "view" or "download".
	Permission string `json:"permission" binding:"required"`
	// ExpiresAt is the optional link expiry.
	ExpiresAt *time.Time `json:"expires_at"`
}

// shareLinkResponse is the response envelope for a single share link.
type shareLinkResponse struct {
	Success bool             `json:"success"`
	Data    *types.ShareLink `json:"data"`
}

// shareLinkListResponse is the response envelope for the link list.
type shareLinkListResponse struct {
	Success bool               `json:"success"`
	Data    []*types.ShareLink `json:"data"`
}

// shareLinkResolutionResponse is the public resolve envelope.
type shareLinkResolutionResponse struct {
	Success bool                       `json:"success"`
	Data    *types.ShareLinkResolution `json:"data"`
}

// CreateShareLink godoc
// @Summary      创建知识外链分享
// @Description  为一个知识签发外链。密级上限校验：机密(confidential)一律拒绝；秘密(secret)不允许 download 权限。token 由服务端生成（crypto/rand 32B → base64url）。
// @Tags         知识外链分享
// @Accept       json
// @Produce      json
// @Param        knowledge_id  path  string                 true  "知识ID"
// @Param        request       body  createShareLinkRequest true  "分享配置"
// @Success      201  {object}  shareLinkResponse
// @Failure      400  {object}  errors.AppError  "参数非法"
// @Failure      403  {object}  errors.AppError  "密级不允许外发"
// @Failure      404  {object}  errors.AppError  "知识不存在"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge/{knowledge_id}/share-links [post]
func (h *ShareLinkHandler) CreateShareLink(c *gin.Context) {
	ctx := c.Request.Context()
	knowledgeID := c.Param("id")
	if knowledgeID == "" {
		c.Error(errors.NewBadRequestError("knowledge_id is required"))
		return
	}
	tenantID := c.GetUint64(types.TenantIDContextKey.String())
	if tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	caller := types.CallerFromContext(ctx)

	var req createShareLinkRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}

	link, err := h.shareService.Create(
		ctx, tenantID, knowledgeID, caller.UserID,
		types.ShareLinkType(req.LinkType),
		types.ShareLinkPermission(req.Permission),
		req.ExpiresAt,
	)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(http.StatusCreated, shareLinkResponse{Success: true, Data: link})
}

// ListShareLinks godoc
// @Summary      列出知识的外链
// @Description  返回某个知识已签发的全部外链（含 token，v1 无“仅显一次”存储），按创建时间倒序。
// @Tags         知识外链分享
// @Produce      json
// @Param        knowledge_id  path  string  true  "知识ID"
// @Success      200  {object}  shareLinkListResponse
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge/{knowledge_id}/share-links [get]
func (h *ShareLinkHandler) ListShareLinks(c *gin.Context) {
	ctx := c.Request.Context()
	knowledgeID := c.Param("id")
	if knowledgeID == "" {
		c.Error(errors.NewBadRequestError("knowledge_id is required"))
		return
	}
	tenantID := c.GetUint64(types.TenantIDContextKey.String())
	if tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}

	links, err := h.shareService.ListByKnowledge(ctx, tenantID, knowledgeID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(http.StatusOK, shareLinkListResponse{Success: true, Data: links})
}

// RevokeShareLink godoc
// @Summary      撤销知识外链
// @Description  置 revoked_at 立即失效该外链（单条 UPDATE，幂等安全）。未知/已撤销返回 404。
// @Tags         知识外链分享
// @Produce      json
// @Param        share_link_id  path  string  true  "外链ID"
// @Success      200  {object}  map[string]interface{}
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /share-links/{share_link_id} [delete]
func (h *ShareLinkHandler) RevokeShareLink(c *gin.Context) {
	ctx := c.Request.Context()
	shareLinkID := c.Param("share_link_id")
	if shareLinkID == "" {
		c.Error(errors.NewBadRequestError("share_link_id is required"))
		return
	}
	tenantID := c.GetUint64(types.TenantIDContextKey.String())
	if tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	caller := types.CallerFromContext(ctx)

	if err := h.shareService.Revoke(ctx, tenantID, shareLinkID, caller.UserID); err != nil {
		c.Error(err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    gin.H{"id": shareLinkID, "revoked": true},
	})
}

// ResolveShareLink godoc
// @Summary      解析知识外链（公开）
// @Description  无鉴权：凭 token 访问。撤销/过期/未知 token 统一返回 404，避免状态泄露。成功时记录访问（visit_log 最近 100 条 + 计数 + 审计）并返回只读投影；仅 download 权限附带文件下载地址。
// @Tags         知识外链分享
// @Produce      json
// @Param        token  path  string  true  "外链token"
// @Success      200  {object}  shareLinkResolutionResponse
// @Failure      404  {object}  errors.AppError  "链接不可用（未知/已撤销/已过期）"
// @Router       /public/share/{token} [get]
func (h *ShareLinkHandler) ResolveShareLink(c *gin.Context) {
	ctx := c.Request.Context()
	token := c.Param("token")

	// Visitor telemetry: gin honours trusted proxies (see NewRouter), so
	// ClientIP is the real client behind nginx; the UA rides along for the
	// audit trail. Neither value is ever echoed back as tenant data.
	visitor := types.ShareLinkVisitorMeta{
		IP:        c.ClientIP(),
		UserAgent: c.Request.UserAgent(),
	}

	resolution, err := h.shareService.Resolve(ctx, token, visitor)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(http.StatusOK, shareLinkResolutionResponse{Success: true, Data: resolution})
}
