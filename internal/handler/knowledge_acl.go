package handler

import (
	"time"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

// KnowledgeACLHandler manages per-document ACL grants (KnowAxis V3.0 §2.1):
// 不能自授 is enforced here; the Admin floor on the routes enforces the
// authorize action ceiling of the default role matrix.
type KnowledgeACLHandler struct {
	aclService interfaces.KnowledgeACLService
}

// NewKnowledgeACLHandler constructs the handler.
func NewKnowledgeACLHandler(aclService interfaces.KnowledgeACLService) *KnowledgeACLHandler {
	return &KnowledgeACLHandler{aclService: aclService}
}

// aclGrantRequest is the body of POST /knowledge/:id/acl.
type aclGrantRequest struct {
	SubjectType string     `json:"subject_type"`
	SubjectID   string     `json:"subject_id" binding:"required"`
	Action      string     `json:"action" binding:"required"`
	Effect      string     `json:"effect"`
	ExpiresAt   *time.Time `json:"expires_at"`
}

// GrantKnowledgeACL godoc
// @Summary      授予/更新文档级 ACL
// @Description  为单个用户授予或拒绝某一动作（deny 优先；密级 secret/confidential 必须显式 allow 才可问/可看）。不能自授。
// @Tags         知识ACL
// @Accept       json
// @Produce      json
// @Param        id      path  string          true  "知识ID"
// @Param        request body  aclGrantRequest true  "授权"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {object}  errors.AppError
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge/{id}/acl [post]
func (h *KnowledgeACLHandler) GrantKnowledgeACL(c *gin.Context) {
	ctx := c.Request.Context()
	knowledgeID := c.Param("id")
	var req aclGrantRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	if req.SubjectID == caller.UserID {
		c.Error(errors.NewBadRequestError("不能自授权限（authorize 不能授予自己）"))
		return
	}
	effect := req.Effect
	if effect == "" {
		effect = string(types.KnowledgeACLEffectAllow)
	}
	grant := &types.KnowledgeACL{
		TenantID:    caller.TenantID,
		KnowledgeID: knowledgeID,
		SubjectType: req.SubjectType,
		SubjectID:   req.SubjectID,
		Action:      req.Action,
		Effect:      effect,
		GrantedBy:   caller.UserID,
		ExpiresAt:   req.ExpiresAt,
	}
	saved, err := h.aclService.Grant(ctx, grant)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": saved})
}

// ListKnowledgeACL godoc
// @Summary      查询文档 ACL
// @Description  返回该知识的全部 ACL 授权行。
// @Tags         知识ACL
// @Produce      json
// @Param        id  path  string  true  "知识ID"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge/{id}/acl [get]
func (h *KnowledgeACLHandler) ListKnowledgeACL(c *gin.Context) {
	grants, err := h.aclService.ListByKnowledge(c.Request.Context(), c.Param("id"))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": grants})
}

// RevokeKnowledgeACL godoc
// @Summary      撤销文档 ACL
// @Description  删除一条授权行；撤权在下次请求即时生效（权限源是数据库，非索引）。
// @Tags         知识ACL
// @Produce      json
// @Param        id      path  string  true  "知识ID"
// @Param        acl_id  path  string  true  "授权ID"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge/{id}/acl/{acl_id} [delete]
func (h *KnowledgeACLHandler) RevokeKnowledgeACL(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	if err := h.aclService.Revoke(ctx, caller.TenantID, c.Param("acl_id"), caller.UserID); err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true})
}
