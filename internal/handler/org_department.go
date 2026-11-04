package handler

import (
	"github.com/gin-gonic/gin"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// OrgDepartmentHandler exposes the tenant organization tree (§2.1 L1).
type OrgDepartmentHandler struct {
	departments interfaces.OrgDepartmentService
}

// NewOrgDepartmentHandler constructs the handler.
func NewOrgDepartmentHandler(
	departments interfaces.OrgDepartmentService,
) *OrgDepartmentHandler {
	return &OrgDepartmentHandler{departments: departments}
}

// departmentRequest is the create/update body.
type departmentRequest struct {
	ParentID       string `json:"parent_id"`
	Name           string `json:"name"`
	ManagerUserID  string `json:"manager_user_id"`
	SubjectGroupID string `json:"subject_group_id"`
	ExternalID     string `json:"external_id"`
}

// moveDepartmentRequest is the move body.
type moveDepartmentRequest struct {
	ParentID string `json:"parent_id"`
}

// departmentMembersRequest is the add-members body.
type departmentMembersRequest struct {
	UserIDs []string `json:"user_ids"`
}

// toInput converts the request into the service input.
func (r departmentRequest) toInput() types.OrgDepartmentInput {
	return types.OrgDepartmentInput{
		ParentID:       r.ParentID,
		Name:           r.Name,
		ManagerUserID:  r.ManagerUserID,
		SubjectGroupID: r.SubjectGroupID,
		ExternalID:     r.ExternalID,
	}
}

// ListDepartments godoc
// @Summary      组织架构树
// @Description  返回本工作区的部门树（含成员数）。部门是权限模型的 L1 层：部门经理可管理本部门子树的授权，Admin+ 管理全租户。
// @Tags         组织架构
// @Produce      json
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /organization/departments [get]
func (h *OrgDepartmentHandler) ListDepartments(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	tree, err := h.departments.ListTree(ctx, tenantID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": tree})
}

// CreateDepartment godoc
// @Summary      新增部门
// @Description  在指定父部门下新建部门；不传 parent_id 则为根部门。会自动创建一个同名 ACL 主体组，便于"给整个部门授权"。
// @Tags         组织架构
// @Accept       json
// @Produce      json
// @Param        request  body  departmentRequest  true  "部门"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /organization/departments [post]
func (h *OrgDepartmentHandler) CreateDepartment(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req departmentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(apperrors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	department, err := h.departments.CreateDepartment(ctx, tenantID, caller.UserID, req.toInput())
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": department})
}

// GetDepartment godoc
// @Summary      部门详情
// @Description  返回一个部门及其成员。
// @Tags         组织架构
// @Produce      json
// @Param        id  path  string  true  "部门 ID"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /organization/departments/{id} [get]
func (h *OrgDepartmentHandler) GetDepartment(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	department, err := h.departments.GetDepartment(ctx, tenantID, c.Param("id"))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": department})
}

// UpdateDepartment godoc
// @Summary      修改部门
// @Description  修改部门名称、负责人或绑定主体组。空字段表示不修改。
// @Tags         组织架构
// @Accept       json
// @Produce      json
// @Param        id       path  string             true  "部门 ID"
// @Param        request  body  departmentRequest  true  "部门"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /organization/departments/{id} [put]
func (h *OrgDepartmentHandler) UpdateDepartment(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req departmentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(apperrors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	department, err := h.departments.UpdateDepartment(ctx, tenantID, c.Param("id"),
		caller.UserID, req.toInput())
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": department})
}

// MoveDepartment godoc
// @Summary      移动部门
// @Description  把部门挂到另一个父部门下（传空则为根）。拒绝挂到自己的子树下，也拒绝超过深度上限。
// @Tags         组织架构
// @Accept       json
// @Produce      json
// @Param        id       path  string                 true  "部门 ID"
// @Param        request  body  moveDepartmentRequest  true  "新父部门"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /organization/departments/{id}/move [post]
func (h *OrgDepartmentHandler) MoveDepartment(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req moveDepartmentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(apperrors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	department, err := h.departments.MoveDepartment(ctx, tenantID, c.Param("id"),
		caller.UserID, req.ParentID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": department})
}

// DeleteDepartment godoc
// @Summary      删除部门
// @Description  删除部门。仍有子部门时拒绝——一次误删会让整棵子树连同成员关系一起消失。
// @Tags         组织架构
// @Produce      json
// @Param        id  path  string  true  "部门 ID"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /organization/departments/{id} [delete]
func (h *OrgDepartmentHandler) DeleteDepartment(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	caller := types.CallerFromContext(ctx)
	if err := h.departments.DeleteDepartment(ctx, tenantID, c.Param("id"), caller.UserID); err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true})
}

// AddDepartmentMembers godoc
// @Summary      添加部门成员
// @Description  把用户加入部门。成员同时进入该部门的 ACL 主体组，因此组上的授权立即生效。
// @Tags         组织架构
// @Accept       json
// @Produce      json
// @Param        id       path  string                   true  "部门 ID"
// @Param        request  body  departmentMembersRequest  true  "成员"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /organization/departments/{id}/members [post]
func (h *OrgDepartmentHandler) AddDepartmentMembers(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req departmentMembersRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(apperrors.NewBadRequestError(err.Error()))
		return
	}
	if len(req.UserIDs) == 0 {
		c.Error(apperrors.NewBadRequestError("user_ids is required"))
		return
	}
	caller := types.CallerFromContext(ctx)
	added, err := h.departments.AddMembers(ctx, tenantID, c.Param("id"), caller.UserID, req.UserIDs)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": gin.H{"added": added}})
}

// RemoveDepartmentMember godoc
// @Summary      移除部门成员
// @Description  把用户移出部门，该部门主体组上的授权对其立即失效。
// @Tags         组织架构
// @Produce      json
// @Param        id       path  string  true  "部门 ID"
// @Param        user_id  path  string  true  "用户 ID"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /organization/departments/{id}/members/{user_id} [delete]
func (h *OrgDepartmentHandler) RemoveDepartmentMember(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	caller := types.CallerFromContext(ctx)
	if err := h.departments.RemoveMember(ctx, tenantID, c.Param("id"),
		c.Param("user_id"), caller.UserID); err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true})
}

// GetDepartmentScope godoc
// @Summary      我的部门管理范围
// @Description  返回当前用户可管理的部门子树。Admin+ 为全租户；部门经理为本部门子树。
// @Tags         组织架构
// @Produce      json
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /organization/departments/scope [get]
func (h *OrgDepartmentHandler) GetDepartmentScope(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	caller := types.CallerFromContext(ctx)
	scope, err := h.departments.ScopeOf(ctx, tenantID, caller.UserID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": scope})
}
