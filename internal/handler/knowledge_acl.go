package handler

import (
	"strconv"
	"strings"
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
	// kgRepo resolves the document's current space for the move preview.
	kgRepo interfaces.KnowledgeRepository
	// versions exposes the revocation propagation ledger (§2.6).
	versions interfaces.PermissionVersionService
}

// NewKnowledgeACLHandler constructs the handler.
func NewKnowledgeACLHandler(
	aclService interfaces.KnowledgeACLService,
	kgRepo interfaces.KnowledgeRepository,
	versions interfaces.PermissionVersionService,
) *KnowledgeACLHandler {
	return &KnowledgeACLHandler{aclService: aclService, kgRepo: kgRepo, versions: versions}
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

// ListRevocationLedger godoc
// @Summary      撤权传播台账
// @Description  返回某篇知识最近的撤权传播记录（KnowAxis V3.0 §2.6）：每个出口一行，含处置结果与当时的权限版本。"撤权完成"的标准是每个出口都验证过看不到，而不是事件已发出。
// @Tags         知识ACL
// @Produce      json
// @Param        id     path   string  true   "知识ID"
// @Param        limit  query  int     false  "返回条数（默认 100，上限 500）"
// @Success      200  {object}  map[string]interface{}  "{success, data: [RevocationPropagation]}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge/{id}/acl/revocations [get]
func (h *KnowledgeACLHandler) ListRevocationLedger(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	limit := 0
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			limit = parsed
		}
	}
	if h.versions == nil {
		c.JSON(200, gin.H{"success": true, "data": []interface{}{}})
		return
	}
	rows, err := h.versions.ListRevocations(ctx, caller.TenantID, c.Param("id"), limit)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": rows})
}

// PreviewKnowledgeMove godoc
// @Summary      预览跨空间移动的访问差异
// @Description  移动前回答"谁将获得/失去访问"（KnowAxis V3.0 §2.3）。只读投影：不写任何数据。移动本身会把文档级授权重置为目标空间继承，因此同时给出将被重置的授权条数。
// @Tags         知识ACL
// @Produce      json
// @Param        id              path   string  true  "知识ID"
// @Param        target_kb_id    query  string  true  "目标知识库ID"
// @Success      200  {object}  map[string]interface{}  "{success, data: KnowledgeMoveAccessPreview}"
// @Failure      400  {object}  errors.AppError
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge/{id}/acl/move-preview [get]
func (h *KnowledgeACLHandler) PreviewKnowledgeMove(c *gin.Context) {
	ctx := c.Request.Context()
	knowledgeID := c.Param("id")
	targetKBID := strings.TrimSpace(c.Query("target_kb_id"))
	sourceKBID := strings.TrimSpace(c.Query("source_kb_id"))
	if targetKBID == "" {
		c.Error(errors.NewBadRequestError("target_kb_id is required"))
		return
	}
	// The document decides its current space; callers may not assert a source
	// space that differs from where the document actually lives.
	if sourceKBID == "" && h.kgRepo != nil {
		knowledge, err := h.kgRepo.GetKnowledgeByIDOnly(ctx, knowledgeID)
		if err != nil {
			c.Error(errors.NewNotFoundError("knowledge not found"))
			return
		}
		if knowledge != nil {
			sourceKBID = knowledge.KnowledgeBaseID
		}
	}
	preview, err := h.aclService.MovePreview(ctx, knowledgeID, sourceKBID, targetKBID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": preview})
}
