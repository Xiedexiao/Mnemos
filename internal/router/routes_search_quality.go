package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterSearchQualityRoutes wires search telemetry and the knowledge-gap list
// (KnowAxis V3.0 §12.5 搜索质量运营 + §14.4 知识缺口).
//
// 权限矩阵：
//   - GET /search-quality、/search-quality/frequent-queries —— 运营指标，
//     含"被权限过滤了多少条"这类聚合：Admin+（治理面，不是检索面）。
//   - POST /search-quality/logs/:log_id/click —— 普通读者产生点击即可，Viewer+；
//     日志行本身已由服务端按 tenant 过滤，不存在越权写入他人租户的可能。
//   - GET /knowledge-gaps、PATCH /knowledge-gaps/:gap_id —— 缺口清单是
//     Owner 的待办池：Admin+。
//
// 全部 JWT-only：查询日志与缺口含未脱敏的用户问题原文，不应通过 API key
// 批量拉取（与 /review、/knowledge-health 同一取舍）。
func RegisterSearchQualityRoutes(
	r *gin.RouterGroup, h *handler.SearchQualityHandler, g *rbacGuards,
) {
	if h == nil {
		return
	}
	quality := r.Group("/search-quality")
	{
		quality.GET("", g.Admin(), h.GetSearchQuality)
		quality.GET("/frequent-queries", g.Admin(), h.GetFrequentQueries)
		// 点击回填：让任何发出搜索的人都能上报自己这一次的点击。
		quality.POST("/logs/:log_id/click", g.Viewer(), h.MarkQueryClicked)
	}
	gaps := r.Group("/knowledge-gaps")
	{
		gaps.GET("", g.Admin(), h.ListKnowledgeGaps)
		gaps.PATCH("/:gap_id", g.Admin(), h.UpdateKnowledgeGap)
	}
}
