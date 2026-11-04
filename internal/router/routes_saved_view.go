package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterSavedViewRoutes wires the §12.2 saved views ("我的视图").
//
// 权限：Viewer+ 且 JWT-only。视图是个人资产，身份恒取自上下文，路由层不需要
// 任何资源级守卫——没有一条路径能读到别人的视图。之所以不给 API key 开口子：
// 视图内容会暴露"这个人平时怎么筛内容"（例如"只看机密"），这属于个人工作
// 画像，不适合交给第三方集成。
func RegisterSavedViewRoutes(
	r *gin.RouterGroup, h *handler.SavedViewHandler, g *rbacGuards,
) {
	if h == nil {
		return
	}
	views := r.Group("/saved-views")
	{
		views.GET("", g.Viewer(), h.ListSavedViews)
		views.POST("", g.Viewer(), h.CreateSavedView)
		views.GET("/:view_id", g.Viewer(), h.GetSavedView)
		views.PUT("/:view_id", g.Viewer(), h.UpdateSavedView)
		views.DELETE("/:view_id", g.Viewer(), h.DeleteSavedView)
		views.POST("/:view_id/default", g.Viewer(), h.SetDefaultSavedView)
	}
}
