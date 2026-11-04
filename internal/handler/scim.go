package handler

import (
	"context"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/application/service"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// SCIMHandler exposes the SCIM 2.0 provisioning endpoints
// (KnowAxis V3.0 §16.4 组织与用户组同步).
//
// 这一组端点的调用方是身份提供方（Okta/Azure AD/飞书/钉钉），不是平台前端：
// 路径、错误码与响应体都按 RFC 7644 组织，而不是平台的 {success, data} 包装。
// 混用两种风格会让 IdP 侧的 provisioning 配置无法完成。
type SCIMHandler struct {
	scim interfaces.SCIMService
}

// NewSCIMHandler constructs the handler.
func NewSCIMHandler(scim interfaces.SCIMService) *SCIMHandler {
	return &SCIMHandler{scim: scim}
}

// scimTenant extracts the tenant id from the provisioning token's context.
func scimTenant(ctx context.Context) (uint64, bool) {
	tenantID, ok := types.TenantIDFromContext(ctx)
	return tenantID, ok && tenantID != 0
}

// writeSCIMError renders a SCIM error envelope.
func writeSCIMError(c *gin.Context, err error) {
	code, status := service.ProjectSCIMError(err)
	c.JSON(code, types.SCIMError{
		Schemas: []string{types.SCIMSchemaError},
		Detail:  err.Error(),
		Status:  status,
	})
}

// scimPaging reads startIndex/count.
func scimPaging(c *gin.Context) (int, int) {
	startIndex, _ := strconv.Atoi(c.DefaultQuery("startIndex", "1"))
	count, _ := strconv.Atoi(c.DefaultQuery("count", "100"))
	return startIndex, count
}

// ListSCIMUsers godoc
// @Summary      SCIM 用户列表
// @Description  SCIM 2.0 User 资源列表，供身份提供方做增量对账。支持 startIndex/count 分页与 `userName eq "..."` 过滤。
// @Tags         SCIM
// @Produce      json
// @Param        startIndex  query  int     false  "起始序号（1 起）"
// @Param        count       query  int     false  "每页条数（默认 100，上限 500）"
// @Param        filter      query  string  false  "过滤表达式（支持 userName eq）"
// @Success      200  {object}  types.SCIMListResponse
// @Router       /scim/v2/Users [get]
func (h *SCIMHandler) ListSCIMUsers(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := scimTenant(ctx)
	if !ok {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	startIndex, count := scimPaging(c)
	result, err := h.scim.ListUsers(ctx, tenantID, startIndex, count, c.Query("filter"))
	if err != nil {
		writeSCIMError(c, err)
		return
	}
	c.JSON(200, result)
}

// GetSCIMUser godoc
// @Summary      SCIM 用户详情
// @Description  按平台用户 ID 读取一个 SCIM User 资源。
// @Tags         SCIM
// @Produce      json
// @Param        id  path  string  true  "用户 ID"
// @Success      200  {object}  types.SCIMUser
// @Router       /scim/v2/Users/{id} [get]
func (h *SCIMHandler) GetSCIMUser(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := scimTenant(ctx)
	if !ok {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	user, err := h.scim.GetUser(ctx, tenantID, c.Param("id"))
	if err != nil {
		writeSCIMError(c, err)
		return
	}
	c.JSON(200, user)
}

// CreateSCIMUser godoc
// @Summary      SCIM 创建用户
// @Description  由身份提供方开通用户（KnowAxis V3.0 §16.4）。userName 已存在返回 409 uniqueness；enterprise 扩展里的 department 会映射到部门树（找不到则新建根部门）。
// @Tags         SCIM
// @Accept       json
// @Produce      json
// @Param        user  body  types.SCIMUser  true  "SCIM User"
// @Success      201  {object}  types.SCIMUser
// @Router       /scim/v2/Users [post]
func (h *SCIMHandler) CreateSCIMUser(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := scimTenant(ctx)
	if !ok {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var user types.SCIMUser
	if err := c.ShouldBindJSON(&user); err != nil {
		writeSCIMError(c, apperrors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	created, err := h.scim.CreateUser(ctx, tenantID, caller.UserID, &user)
	if err != nil {
		writeSCIMError(c, err)
		return
	}
	c.JSON(201, created)
}

// ReplaceSCIMUser godoc
// @Summary      SCIM 替换用户
// @Description  PUT 全量替换用户的可变属性（邮箱、active、department）。
// @Tags         SCIM
// @Accept       json
// @Produce      json
// @Param        id    path  string         true  "用户 ID"
// @Param        user  body  types.SCIMUser  true  "SCIM User"
// @Success      200  {object}  types.SCIMUser
// @Router       /scim/v2/Users/{id} [put]
func (h *SCIMHandler) ReplaceSCIMUser(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := scimTenant(ctx)
	if !ok {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var user types.SCIMUser
	if err := c.ShouldBindJSON(&user); err != nil {
		writeSCIMError(c, apperrors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	updated, err := h.scim.ReplaceUser(ctx, tenantID, c.Param("id"), caller.UserID, &user)
	if err != nil {
		writeSCIMError(c, err)
		return
	}
	c.JSON(200, updated)
}

// PatchSCIMUser godoc
// @Summary      SCIM 部分更新用户
// @Description  只支持 replace active（停用/启用）与 displayName（平台侧由用户名合成，显式忽略）。IdP 停用账号走的就是这条。
// @Tags         SCIM
// @Accept       json
// @Produce      json
// @Param        id     path  string                true  "用户 ID"
// @Param        patch  body  types.SCIMPatchRequest true  "SCIM PatchOp"
// @Success      200  {object}  types.SCIMUser
// @Router       /scim/v2/Users/{id} [patch]
func (h *SCIMHandler) PatchSCIMUser(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := scimTenant(ctx)
	if !ok {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var patch types.SCIMPatchRequest
	if err := c.ShouldBindJSON(&patch); err != nil {
		writeSCIMError(c, apperrors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	updated, err := h.scim.PatchUser(ctx, tenantID, c.Param("id"), caller.UserID, &patch)
	if err != nil {
		writeSCIMError(c, err)
		return
	}
	c.JSON(200, updated)
}

// DeleteSCIMUser godoc
// @Summary      SCIM 停用用户
// @Description  不物理删除：审计、审批回执与版本都引用用户 ID，删行会让这些记录失去主体。停用即刻失去访问。
// @Tags         SCIM
// @Produce      json
// @Param        id  path  string  true  "用户 ID"
// @Success      204
// @Router       /scim/v2/Users/{id} [delete]
func (h *SCIMHandler) DeleteSCIMUser(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := scimTenant(ctx)
	if !ok {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	caller := types.CallerFromContext(ctx)
	if err := h.scim.DeleteUser(ctx, tenantID, c.Param("id"), caller.UserID); err != nil {
		writeSCIMError(c, err)
		return
	}
	c.Status(204)
}

// ListSCIMGroups godoc
// @Summary      SCIM 组列表
// @Description  SCIM 2.0 Group 资源列表；组即平台的 ACL 主体组，成员变动立即影响文档授权。
// @Tags         SCIM
// @Produce      json
// @Param        startIndex  query  int     false  "起始序号"
// @Param        count       query  int     false  "每页条数"
// @Param        filter      query  string  false  "过滤表达式（支持 displayName eq）"
// @Success      200  {object}  types.SCIMListResponse
// @Router       /scim/v2/Groups [get]
func (h *SCIMHandler) ListSCIMGroups(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := scimTenant(ctx)
	if !ok {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	startIndex, count := scimPaging(c)
	result, err := h.scim.ListGroups(ctx, tenantID, startIndex, count, c.Query("filter"))
	if err != nil {
		writeSCIMError(c, err)
		return
	}
	c.JSON(200, result)
}

// GetSCIMGroup godoc
// @Summary      SCIM 组详情
// @Description  读取一个组及其成员。
// @Tags         SCIM
// @Produce      json
// @Param        id  path  string  true  "组 ID"
// @Success      200  {object}  types.SCIMGroup
// @Router       /scim/v2/Groups/{id} [get]
func (h *SCIMHandler) GetSCIMGroup(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := scimTenant(ctx)
	if !ok {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	group, err := h.scim.GetGroup(ctx, tenantID, c.Param("id"))
	if err != nil {
		writeSCIMError(c, err)
		return
	}
	c.JSON(200, group)
}

// CreateSCIMGroup godoc
// @Summary      SCIM 创建组
// @Description  由身份提供方创建组（映射为 ACL 主体组）。
// @Tags         SCIM
// @Accept       json
// @Produce      json
// @Param        group  body  types.SCIMGroup  true  "SCIM Group"
// @Success      201  {object}  types.SCIMGroup
// @Router       /scim/v2/Groups [post]
func (h *SCIMHandler) CreateSCIMGroup(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := scimTenant(ctx)
	if !ok {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var group types.SCIMGroup
	if err := c.ShouldBindJSON(&group); err != nil {
		writeSCIMError(c, apperrors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	created, err := h.scim.CreateGroup(ctx, tenantID, caller.UserID, &group)
	if err != nil {
		writeSCIMError(c, err)
		return
	}
	c.JSON(201, created)
}

// ReplaceSCIMGroup godoc
// @Summary      SCIM 替换组
// @Description  PUT 全量替换组名与成员列表。只处理真正变化的成员，避免权限版本反复前进。
// @Tags         SCIM
// @Accept       json
// @Produce      json
// @Param        id     path  string          true  "组 ID"
// @Param        group  body  types.SCIMGroup  true  "SCIM Group"
// @Success      200  {object}  types.SCIMGroup
// @Router       /scim/v2/Groups/{id} [put]
func (h *SCIMHandler) ReplaceSCIMGroup(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := scimTenant(ctx)
	if !ok {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var group types.SCIMGroup
	if err := c.ShouldBindJSON(&group); err != nil {
		writeSCIMError(c, apperrors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	updated, err := h.scim.ReplaceGroup(ctx, tenantID, c.Param("id"), caller.UserID, &group)
	if err != nil {
		writeSCIMError(c, err)
		return
	}
	c.JSON(200, updated)
}

// PatchSCIMGroup godoc
// @Summary      SCIM 部分更新组
// @Description  支持 displayName 与 members 的 add/remove/replace。
// @Tags         SCIM
// @Accept       json
// @Produce      json
// @Param        id     path  string                 true  "组 ID"
// @Param        patch  body  types.SCIMPatchRequest true  "SCIM PatchOp"
// @Success      200  {object}  types.SCIMGroup
// @Router       /scim/v2/Groups/{id} [patch]
func (h *SCIMHandler) PatchSCIMGroup(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := scimTenant(ctx)
	if !ok {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var patch types.SCIMPatchRequest
	if err := c.ShouldBindJSON(&patch); err != nil {
		writeSCIMError(c, apperrors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	updated, err := h.scim.PatchGroup(ctx, tenantID, c.Param("id"), caller.UserID, &patch)
	if err != nil {
		writeSCIMError(c, err)
		return
	}
	c.JSON(200, updated)
}

// DeleteSCIMGroup godoc
// @Summary      SCIM 删除组
// @Description  删除组及其成员关系。按组写的授权行保留，但组 ID 不再被任何人持有。
// @Tags         SCIM
// @Produce      json
// @Param        id  path  string  true  "组 ID"
// @Success      204
// @Router       /scim/v2/Groups/{id} [delete]
func (h *SCIMHandler) DeleteSCIMGroup(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := scimTenant(ctx)
	if !ok {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	caller := types.CallerFromContext(ctx)
	if err := h.scim.DeleteGroup(ctx, tenantID, c.Param("id"), caller.UserID); err != nil {
		writeSCIMError(c, err)
		return
	}
	c.Status(204)
}

// scimBearerToken extracts the provisioning token from the Authorization header.
func scimBearerToken(ctx *gin.Context) string {
	raw := strings.TrimSpace(ctx.GetHeader("Authorization"))
	if len(raw) > 7 && strings.EqualFold(raw[:7], "bearer ") {
		return strings.TrimSpace(raw[7:])
	}
	return ""
}
