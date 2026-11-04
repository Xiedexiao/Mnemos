package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterSearchSuggestionRoutes wires the §12.1 search suggestions.
//
// 权限：Viewer+ 且 JWT-only。
//   - 建议是检索面的一部分，任何能搜索的人都能取自己的历史与热门榜；
//   - 历史恒取调用者身份，DELETE 也不接受 user_id 参数，不存在横向越权；
//   - 标题联想在服务层按 ACL 过滤，ACL 失败时整桶为空；
//   - 热门榜会暴露本租户的搜索行为规模，所以不进 API key 面：给第三方
//     key 的应该是检索能力，不是"这家公司在搜什么"。
func RegisterSearchSuggestionRoutes(
	r *gin.RouterGroup, h *handler.SearchSuggestionHandler, g *rbacGuards,
) {
	if h == nil {
		return
	}
	search := r.Group("/search")
	{
		search.GET("/suggestions", g.Viewer(), h.GetSearchSuggestions)
		search.DELETE("/history", g.Viewer(), h.DeleteSearchHistory)
	}
}
