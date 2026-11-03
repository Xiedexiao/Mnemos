package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterPersonalCenterRoutes wires the personal center (KnowAxis V3.0 §15.3).
//
// 权限：任何登录用户都能看自己的个人中心（Viewer+）。接口内部固定使用
// 调用者身份，不接受 user_id 参数，因此不存在横向越权的可能。
func RegisterPersonalCenterRoutes(
	r *gin.RouterGroup, h *handler.PersonalCenterHandler, g *rbacGuards,
) {
	if h == nil {
		return
	}
	me := r.Group("/me")
	{
		me.GET("/center", g.Viewer(), h.GetPersonalCenter)
	}
}
