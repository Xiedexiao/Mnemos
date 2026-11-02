package handler

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// FolderACLHandler exposes directory-level permissions and the space policy
// (KnowAxis V3.0 §2.3 权限继承与上限 / §4.1 空间).
type FolderACLHandler struct {
	folders interfaces.FolderACLService
	kbs     interfaces.KnowledgeBaseService
}

// NewFolderACLHandler constructs the handler.
func NewFolderACLHandler(
	folders interfaces.FolderACLService,
	kbs interfaces.KnowledgeBaseService,
) *FolderACLHandler {
	return &FolderACLHandler{folders: folders, kbs: kbs}
}

// folderGrantRequest is the body of POST /knowledge-bases/:id/folder-acl.
type folderGrantRequest struct {
	FolderPath      string     `json:"folder_path"`
	SubjectType     string     `json:"subject_type"`
	SubjectID       string     `json:"subject_id"`
	Action          string     `json:"action"`
	Effect          string     `json:"effect"`
	InheritChildren *bool      `json:"inherit_children"`
	ExpiresAt       *time.Time `json:"expires_at"`
}

// GrantFolderACL godoc
// @Summary      授予目录级 ACL
// @Description  为一个目录授予或拒绝某个动作（KnowAxis V3.0 §2.3）。子目录默认继承；开放父目录没有的动作会被拒绝，并记入治理清单（动作上限）。
// @Tags         目录权限
// @Accept       json
// @Produce      json
// @Param        id       path  string              true  "知识库ID"
// @Param        request  body  folderGrantRequest  true  "目录授权"
// @Success      200  {object}  map[string]interface{}  "{success, data: FolderACL}"
// @Failure      403  {object}  errors.AppError  "超出父级动作上限"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge-bases/{id}/folder-acl [post]
func (h *FolderACLHandler) GrantFolderACL(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req folderGrantRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	inherit := true
	if req.InheritChildren != nil {
		inherit = *req.InheritChildren
	}
	caller := types.CallerFromContext(ctx)
	grant, err := h.folders.Grant(ctx, &types.FolderACL{
		TenantID:        tenantID,
		KnowledgeBaseID: c.Param("id"),
		FolderPath:      req.FolderPath,
		SubjectType:     req.SubjectType,
		SubjectID:       req.SubjectID,
		Action:          req.Action,
		Effect:          req.Effect,
		InheritChildren: inherit,
		GrantedBy:       caller.UserID,
		ExpiresAt:       req.ExpiresAt,
	})
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": grant})
}

// ListFolderACL godoc
// @Summary      列出目录级 ACL
// @Description  返回某个空间的全部目录级授权。
// @Tags         目录权限
// @Produce      json
// @Param        id  path  string  true  "知识库ID"
// @Success      200  {object}  map[string]interface{}  "{success, data: [FolderACL]}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge-bases/{id}/folder-acl [get]
func (h *FolderACLHandler) ListFolderACL(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	grants, err := h.folders.List(ctx, tenantID, c.Param("id"))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": grants})
}

// RevokeFolderACL godoc
// @Summary      撤销目录级 ACL
// @Description  删除一条目录授权；撤权即时生效（权限源是数据库）。
// @Tags         目录权限
// @Produce      json
// @Param        id        path  string  true  "知识库ID"
// @Param        grant_id  path  string  true  "授权ID"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge-bases/{id}/folder-acl/{grant_id} [delete]
func (h *FolderACLHandler) RevokeFolderACL(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	caller := types.CallerFromContext(ctx)
	if err := h.folders.Revoke(ctx, tenantID, c.Param("grant_id"), caller.UserID); err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true})
}

// ResolveFolderACL godoc
// @Summary      解析某个目录的有效权限
// @Description  折叠该目录及其祖先的授权，返回有效允许/拒绝动作，并说明结论来自哪一层（§2.3 继承）。
// @Tags         目录权限
// @Produce      json
// @Param        id           path   string  true  "知识库ID"
// @Param        folder_path  query  string  false "目录路径（缺省为根）"
// @Success      200  {object}  map[string]interface{}  "{success, data: FolderACLDecision}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge-bases/{id}/folder-acl/resolve [get]
func (h *FolderACLHandler) ResolveFolderACL(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	decision, err := h.folders.ResolveForFolder(ctx, tenantID, c.Param("id"), c.Query("folder_path"))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": decision})
}

// ListACLCeilingViolations godoc
// @Summary      权限上限违规清单
// @Description  返回"子级授权超出父级"的治理清单（§2.3：唯一权限过多/破坏继承进入治理清单）。
// @Tags         目录权限
// @Produce      json
// @Success      200  {object}  map[string]interface{}  "{success, data: [ACLCeilingViolation]}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /acl-ceiling-violations [get]
func (h *FolderACLHandler) ListACLCeilingViolations(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	rows, err := h.folders.ListCeilingViolations(ctx, tenantID, 0)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": rows})
}

// GetSpacePolicy godoc
// @Summary      读取空间策略
// @Description  返回空间类型推导出的有效策略：默认密级、是否强制审核、到期只读（§4.1 五种空间类型的默认策略）。
// @Tags         目录权限
// @Produce      json
// @Param        id  path  string  true  "知识库ID"
// @Success      200  {object}  map[string]interface{}  "{success, data: SpacePolicy}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge-bases/{id}/space-policy [get]
func (h *FolderACLHandler) GetSpacePolicy(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	if h.kbs == nil {
		c.Error(errors.NewInternalServerError("knowledge base service is not configured"))
		return
	}
	kb, err := h.kbs.GetKnowledgeBaseByID(ctx, c.Param("id"))
	if err != nil || kb == nil || kb.TenantID != tenantID {
		c.Error(errors.NewNotFoundError("knowledge base not found"))
		return
	}
	policy := types.ResolveSpacePolicy(
		types.ParseSpaceKind(kb.SpaceKind), kb.DefaultClassification, kb.ExpiresAt)
	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"policy":    policy,
			"read_only": policy.SpaceReadOnly(time.Now()),
		},
	})
}
