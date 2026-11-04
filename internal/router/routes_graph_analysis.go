package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterGraphAnalysisRoutes wires the §11.1 centrality / community / expert
// analyses.
//
// 权限分两档，依据是"这个结果会不会被用来对人做判断"：
//   - 中心度与社区检测是**内容侧**结论（哪些文档是核心资产、领域怎么划分），
//     任何能读这个空间的读者都可以看，因此 Viewer+ 且经 KBAccessRead 门禁，
//     与知识地图一致。
//   - 专家发现是**人员侧**结论（谁是专家），会直接影响任务分派与考核，
//     且本身带反推风险（§11.3）。因此 Admin+，并在服务层写审计日志。
//
// 三个接口都是 JWT-only：注册在裸 group 上、不经 apiKeyGroup 声明，API key
// 默认拒绝。图分析会一次性吐出整个空间的结构，比逐篇读取更容易被用来做
// 批量枚举，不适合放进给第三方集成的 key 面。
func RegisterGraphAnalysisRoutes(
	r *gin.RouterGroup, h *handler.GraphAnalysisHandler, g *rbacGuards,
) {
	if h == nil {
		return
	}
	analysis := r.Group("/knowledge-bases/:id/graph-analysis")
	{
		analysis.GET("/centrality", g.Viewer(), g.KBAccessRead("id"), h.GetCentrality)
		analysis.GET("/communities", g.Viewer(), g.KBAccessRead("id"), h.GetCommunities)
		analysis.GET("/experts", g.Admin(), g.KBAccessRead("id"), h.GetExperts)
	}
}
