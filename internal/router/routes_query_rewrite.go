package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterQueryRewriteRoutes wires the §12.1 fault-tolerance surface
// (拼音搜索 / 错别字纠正).
//
// 权限：Viewer+ 且 JWT-only。纠错候选是按调用者的可见空间算出来的，因此
// 普通读者可以放心调用；但这条接口会回显候选词，走 API key 面等于把词表
// 交给第三方集成，与检索建议同一个取舍，不开口子。
func RegisterQueryRewriteRoutes(
	r *gin.RouterGroup, h *handler.QueryRewriteHandler, g *rbacGuards,
) {
	if h == nil {
		return
	}
	r.GET("/search/rewrite", g.Viewer(), h.GetQueryRewrite)
}
