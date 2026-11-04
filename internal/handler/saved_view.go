package handler

import (
	"github.com/gin-gonic/gin"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// SavedViewHandler exposes the §12.2 saved views.
type SavedViewHandler struct {
	views interfaces.SavedViewService
}

// NewSavedViewHandler constructs the handler.
func NewSavedViewHandler(views interfaces.SavedViewService) *SavedViewHandler {
	return &SavedViewHandler{views: views}
}

// ListSavedViews godoc
// @Summary      我的视图列表
// @Description  返回调用者本人的保存视图（KnowAxis V3.0 §12.2 "筛选条件可保存为我的视图"），并标出该空间下要自动套用的默认视图。视图恒为本人所有：接口不接受 user_id，也不存在"列出同事视图"的路径。
// @Tags         保存的视图
// @Produce      json
// @Success      200  {object}  map[string]interface{}  "{success, data: SavedViewList}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /saved-views [get]
func (h *SavedViewHandler) ListSavedViews(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, caller, ok := savedViewCaller(c)
	if !ok {
		return
	}
	list, err := h.views.List(ctx, tenantID, caller.UserID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": list})
}

// GetSavedView godoc
// @Summary      我的视图详情
// @Description  返回一条保存视图（KnowAxis V3.0 §12.2）。不存在与无权访问返回同一个 404。
// @Tags         保存的视图
// @Produce      json
// @Param        view_id  path  string  true  "视图ID"  extensions(x-agent=view_id)
// @Success      200  {object}  handler.DataResponse{data=types.SavedKnowledgeView}  "{success, data: SavedKnowledgeView}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @x-agent      {"id":"saved_view.get","risk":"read"}
// @x-agent-hint "Reads one of the caller's saved filter views: a named set of list filters for a knowledge space. Use it to reuse the user's own filter rather than guessing one."
// @Router       /saved-views/{view_id} [get]
func (h *SavedViewHandler) GetSavedView(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, caller, ok := savedViewCaller(c)
	if !ok {
		return
	}
	view, err := h.views.Get(ctx, tenantID, caller.UserID, c.Param("view_id"))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": view})
}

// CreateSavedView godoc
// @Summary      保存为我的视图
// @Description  把一组筛选与排序条件保存成个人视图（KnowAxis V3.0 §12.2）。名称在同一用户内唯一（重名返回 409）；每个筛选维度按白名单校验，写错的枚举值直接报错而不是静默返回空列表；视图不保存权限结论（拒绝集与密级上限在每次读取时按当前权限重新计算）。
// @Tags         保存的视图
// @Accept       json
// @Produce      json
// @Param        request  body  types.SavedViewInput  true  "{name, knowledge_base_id, filter, is_default}"
// @Success      200  {object}  map[string]interface{}  "{success, data: SavedKnowledgeView}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /saved-views [post]
func (h *SavedViewHandler) CreateSavedView(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, caller, ok := savedViewCaller(c)
	if !ok {
		return
	}
	var input types.SavedViewInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.Error(apperrors.NewBadRequestError(err.Error()))
		return
	}
	view, err := h.views.Create(ctx, tenantID, caller.UserID, input)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": view})
}

// UpdateSavedView godoc
// @Summary      更新我的视图
// @Description  替换一条保存视图的名称与筛选条件（KnowAxis V3.0 §12.2）。
// @Tags         保存的视图
// @Accept       json
// @Produce      json
// @Param        view_id  path  string               true  "视图ID"
// @Param        request  body  types.SavedViewInput true  "{name, knowledge_base_id, filter, is_default}"
// @Success      200  {object}  map[string]interface{}  "{success, data: SavedKnowledgeView}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /saved-views/{view_id} [put]
func (h *SavedViewHandler) UpdateSavedView(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, caller, ok := savedViewCaller(c)
	if !ok {
		return
	}
	var input types.SavedViewInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.Error(apperrors.NewBadRequestError(err.Error()))
		return
	}
	view, err := h.views.Update(ctx, tenantID, caller.UserID, c.Param("view_id"), input)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": view})
}

// DeleteSavedView godoc
// @Summary      删除我的视图
// @Description  删除一条保存视图（KnowAxis V3.0 §12.2）。
// @Tags         保存的视图
// @Produce      json
// @Param        view_id  path  string  true  "视图ID"
// @Success      200  {object}  map[string]interface{}  "{success}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /saved-views/{view_id} [delete]
func (h *SavedViewHandler) DeleteSavedView(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, caller, ok := savedViewCaller(c)
	if !ok {
		return
	}
	if err := h.views.Delete(ctx, tenantID, caller.UserID, c.Param("view_id")); err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true})
}

// SetDefaultSavedView godoc
// @Summary      设为我在此空间的默认视图
// @Description  把一条保存视图设为其所属空间的默认视图，并取消该空间原有的默认（KnowAxis V3.0 §12.2）。同一 (用户, 空间) 恒只有一个默认。
// @Tags         保存的视图
// @Produce      json
// @Param        view_id  path  string  true  "视图ID"
// @Success      200  {object}  map[string]interface{}  "{success, data: SavedKnowledgeView}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /saved-views/{view_id}/default [post]
func (h *SavedViewHandler) SetDefaultSavedView(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, caller, ok := savedViewCaller(c)
	if !ok {
		return
	}
	view, err := h.views.SetDefault(ctx, tenantID, caller.UserID, c.Param("view_id"))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": view})
}

// savedViewCaller resolves the tenant and caller, writing the error itself.
//
// 身份恒取自上下文，不接受 user_id 参数：视图是个人资产，任何"帮别人查"
// 的入口都会把筛选条件（"我只看机密"）变成可读信息。
func savedViewCaller(c *gin.Context) (uint64, types.Caller, bool) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return 0, types.Caller{}, false
	}
	caller := types.CallerFromContext(ctx)
	if caller.UserID == "" {
		c.Error(apperrors.NewUnauthorizedError("caller identity is required"))
		return 0, types.Caller{}, false
	}
	return tenantID, caller, true
}
