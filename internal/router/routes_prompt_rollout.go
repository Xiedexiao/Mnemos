package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterPromptRolloutRoutes wires the §16.5 canary and rollback surface.
//
// 权限 = Admin+ 且 JWT-only：改活跃版本指针会立刻改变所有用户看到的提示词，
// 属于租户级模型治理；API key 不开口子——机器身份不该改写提示词。
func RegisterPromptRolloutRoutes(
	r *gin.RouterGroup, h *handler.PromptRolloutHandler, g *rbacGuards,
) {
	if h == nil {
		return
	}
	rollout := r.Group("/prompt-rollout")
	{
		rollout.GET("/:template_key", g.Admin(), h.GetPromptRollout)
		rollout.PUT("/:template_key", g.Admin(), h.ConfigurePromptRollout)
		rollout.POST("/:template_key/rollback", g.Admin(), h.RollbackPrompt)
		rollout.GET("/:template_key/select", g.Admin(), h.SelectPromptVersion)
	}
}
