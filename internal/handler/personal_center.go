package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// PersonalCenterHandler exposes the "以我为中心" view (KnowAxis V3.0 §15.3).
type PersonalCenterHandler struct {
	center interfaces.PersonalCenterService
}

// NewPersonalCenterHandler constructs the handler.
func NewPersonalCenterHandler(center interfaces.PersonalCenterService) *PersonalCenterHandler {
	return &PersonalCenterHandler{center: center}
}

// GetPersonalCenter godoc
// @Summary      个人中心
// @Description  一次返回"我提交的审批 / 我的收藏 / 我的知识"与概览计数（KnowAxis V3.0 §15.3）。收藏在权限失效或对象已删除时标记为不可见，而不是渲染成死链接。
// @Tags         个人中心
// @Produce      json
// @Success      200  {object}  map[string]interface{}  "{success, data: PersonalCenter}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /me/center [get]
func (h *PersonalCenterHandler) GetPersonalCenter(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	// 个人中心永远只读"当前调用者"，不接受 user_id 参数：允许传别人会让
	// 它变成一个越权查询入口。
	caller := types.CallerFromContext(ctx)
	if caller.UserID == "" {
		c.Error(errors.NewUnauthorizedError("caller identity is required"))
		return
	}
	center, err := h.center.Get(ctx, tenantID, caller.UserID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": center})
}
