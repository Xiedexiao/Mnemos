package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// ACLSubjectGroupHandler exposes tenant-scoped ACL subject groups
// (KnowAxis V3.0 §2.1: a grant may target a user, a group or a role).
type ACLSubjectGroupHandler struct {
	groups interfaces.ACLSubjectGroupService
}

// NewACLSubjectGroupHandler constructs the handler.
func NewACLSubjectGroupHandler(groups interfaces.ACLSubjectGroupService) *ACLSubjectGroupHandler {
	return &ACLSubjectGroupHandler{groups: groups}
}

// aclGroupRequest is the create/update body.
type aclGroupRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// aclGroupMembersRequest is the add-members body.
type aclGroupMembersRequest struct {
	UserIDs []string `json:"user_ids"`
}

// CreateACLGroup godoc
// @Summary      新增授权主体组
// @Description  创建一个用户组，供文档 ACL 按组授权（KnowAxis V3.0 §2.1）。组内成员自动继承组上的授权；退组即刻收权。
// @Tags         知识ACL
// @Accept       json
// @Produce      json
// @Param        request  body  aclGroupRequest  true  "用户组"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /acl/groups [post]
func (h *ACLSubjectGroupHandler) CreateACLGroup(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req aclGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	group, err := h.groups.CreateGroup(ctx, tenantID, caller.UserID, req.Name, req.Description)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": group})
}

// ListACLGroups godoc
// @Summary      授权主体组列表
// @Description  列出本租户的用户组及成员数。
// @Tags         知识ACL
// @Produce      json
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /acl/groups [get]
func (h *ACLSubjectGroupHandler) ListACLGroups(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	groups, err := h.groups.ListGroups(ctx, tenantID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": groups})
}

// UpdateACLGroup godoc
// @Summary      修改授权主体组
// @Description  重命名或修改描述。授权存的是组 ID，改名不会改变已有授权的适用范围。
// @Tags         知识ACL
// @Accept       json
// @Produce      json
// @Param        id       path  string          true  "组 ID"
// @Param        request  body  aclGroupRequest true  "用户组"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /acl/groups/{id} [put]
func (h *ACLSubjectGroupHandler) UpdateACLGroup(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req aclGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	group, err := h.groups.UpdateGroup(ctx, tenantID, c.Param("id"), caller.UserID,
		req.Name, req.Description)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": group})
}

// DeleteACLGroup godoc
// @Summary      删除授权主体组
// @Description  删除用户组及其成员关系。按组写的授权行保留，但组 ID 不再被任何用户持有，等同于自然失效。
// @Tags         知识ACL
// @Produce      json
// @Param        id  path  string  true  "组 ID"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /acl/groups/{id} [delete]
func (h *ACLSubjectGroupHandler) DeleteACLGroup(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	caller := types.CallerFromContext(ctx)
	if err := h.groups.DeleteGroup(ctx, tenantID, c.Param("id"), caller.UserID); err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true})
}

// ListACLGroupMembers godoc
// @Summary      组成员列表
// @Description  返回组内用户 ID。
// @Tags         知识ACL
// @Produce      json
// @Param        id  path  string  true  "组 ID"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /acl/groups/{id}/members [get]
func (h *ACLSubjectGroupHandler) ListACLGroupMembers(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	members, err := h.groups.ListMembers(ctx, tenantID, c.Param("id"))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": members})
}

// AddACLGroupMembers godoc
// @Summary      添加组成员
// @Description  把用户加入组，成员立即继承组上的全部授权。
// @Tags         知识ACL
// @Accept       json
// @Produce      json
// @Param        id       path  string                 true  "组 ID"
// @Param        request  body  aclGroupMembersRequest true  "成员"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /acl/groups/{id}/members [post]
func (h *ACLSubjectGroupHandler) AddACLGroupMembers(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req aclGroupMembersRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	if len(req.UserIDs) == 0 {
		c.Error(errors.NewBadRequestError("user_ids is required"))
		return
	}
	caller := types.CallerFromContext(ctx)
	added, err := h.groups.AddMembers(ctx, tenantID, c.Param("id"), caller.UserID, req.UserIDs)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": gin.H{"added": added}})
}

// RemoveACLGroupMember godoc
// @Summary      移除组成员
// @Description  把用户移出组，组上的授权对该用户立即失效。
// @Tags         知识ACL
// @Produce      json
// @Param        id       path  string  true  "组 ID"
// @Param        user_id  path  string  true  "用户 ID"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /acl/groups/{id}/members/{user_id} [delete]
func (h *ACLSubjectGroupHandler) RemoveACLGroupMember(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	caller := types.CallerFromContext(ctx)
	if err := h.groups.RemoveMember(ctx, tenantID, c.Param("id"), c.Param("user_id"),
		caller.UserID); err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true})
}
