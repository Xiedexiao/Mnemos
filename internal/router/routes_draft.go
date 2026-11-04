package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterDraftRoutes wires §5.4 draft autosave.
//
// 权限：草稿是"本人未完成的内容"，只有作者本人可见可写，Viewer+ 即可；
// 仓储层按 (tenant, user) 过滤，别人的草稿 ID 猜不到也读不到。
func RegisterDraftRoutes(
	r *gin.RouterGroup, h *handler.DraftHandler, g *rbacGuards,
) {
	if h == nil {
		return
	}
	drafts := r.Group("/drafts")
	{
		drafts.PUT("", g.Viewer(), h.SaveDraft)
		drafts.GET("", g.Viewer(), h.GetDraft)
		drafts.GET("/list", g.Viewer(), h.ListDrafts)
		drafts.DELETE("/:id", g.Viewer(), h.DeleteDraft)
	}
}
