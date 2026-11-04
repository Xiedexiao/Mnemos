package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
)

// RegisterSCIMRoutes wires the SCIM 2.0 provisioning endpoints
// (KnowAxis V3.0 §16.4).
//
// 授权模型与其它路由不同：调用方是外部 IdP，不持有 JWT，也不该拿到租户的
// 普通 API Key。这里用一个**专门的能力** scim_provision：
//   - 它与 manage_members 分开，这样交给 IdP 的凭据不能顺手改租户成员角色；
//   - 它能被单独吊销、单独设置过期时间；
//   - 路径固定为 /scim/v2/*（RFC 7644 惯例），IdP 侧只需填 Base URL + Token。
//
// 路由声明走既有的 apiKeyGroup：未声明能力的 key 对这些路径默认拒绝，
// 声明了 scim_provision 的 key 才能进来。
func RegisterSCIMRoutes(r *gin.RouterGroup, h *handler.SCIMHandler, g *rbacGuards) {
	if h == nil {
		return
	}
	policy := middleware.APIKeyRoutePolicy{}.
		WithCapability(types.APIKeyCapabilitySCIMProvision)
	guard := g.Viewer()

	scim := g.apiKeyGroup(r.Group("/scim/v2"), policy)
	{
		scim.GET("/Users", guard, h.ListSCIMUsers)
		scim.POST("/Users", guard, h.CreateSCIMUser)
		scim.GET("/Users/:id", guard, h.GetSCIMUser)
		scim.PUT("/Users/:id", guard, h.ReplaceSCIMUser)
		scim.PATCH("/Users/:id", guard, h.PatchSCIMUser)
		scim.DELETE("/Users/:id", guard, h.DeleteSCIMUser)

		scim.GET("/Groups", guard, h.ListSCIMGroups)
		scim.POST("/Groups", guard, h.CreateSCIMGroup)
		scim.GET("/Groups/:id", guard, h.GetSCIMGroup)
		scim.PUT("/Groups/:id", guard, h.ReplaceSCIMGroup)
		scim.PATCH("/Groups/:id", guard, h.PatchSCIMGroup)
		scim.DELETE("/Groups/:id", guard, h.DeleteSCIMGroup)
	}
}
