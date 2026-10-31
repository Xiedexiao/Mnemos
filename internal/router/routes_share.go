package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// routes_share.go wires the knowledge external share-link endpoints
// (KnowAxis V3.0 §2.5 v1).
//
// RegisterShareLinkRoutes — authenticated management surface, mounted on
// the /api/v1 group next to RegisterKnowledgeRoutes:
//
//	POST   /api/v1/knowledge/:knowledge_id/share-links   创建外链（Contributor + KB write）
//	GET    /api/v1/knowledge/:knowledge_id/share-links   列出外链（Contributor + KB write）
//	DELETE /api/v1/share-links/:share_link_id            撤销外链（Contributor；服务层按租户兜底）
//
// JWT-only by design: no API-key capability is declared for share
// management, so the /api/v1 APIKeyGate fails closed for X-API-Key
// principals here (they keep 403 until a capability is added deliberately).
//
// RegisterShareLinkPublicRoutes — anonymous resolve surface. It must be
// mounted on a group that sits BEFORE the global Auth middleware in
// NewRouter (the same slot RegisterEmbedPublicRoutes uses), because a
// share visitor holds no JWT and no API key:
//
//	GET /public/share/:token   免鉴权解析外链
//
// Rate limiting on the public endpoint is intentionally left as a TODO:
// v1 leans on the uniform 404 and the per-visit audit trail; add a
// per-IP/per-token limiter middleware here when wiring it for real.
//
// Assembly examples (DI container + NewRouter, for when this vertical is
// wired up — no existing file needs to change to compile this package):
//
//	// internal/container/container.go — provide the repo, service, handler:
//	must(container.Provide(repository.NewShareLinkRepository))
//	must(container.Provide(service.NewShareLinkService))
//	must(container.Provide(handler.NewShareLinkHandler))
//
//	// internal/router/router.go — RouterParams gains:
//	//   ShareLinkHandler *handler.ShareLinkHandler
//	//
//	// BEFORE r.Use(middleware.Auth(...)) (public, unauthenticated):
//	RegisterShareLinkPublicRoutes(&r.RouterGroup, params.ShareLinkHandler)
//	// or equivalently: RegisterShareLinkPublicRoutes(r.Group(""), params.ShareLinkHandler)
//	//
//	// Inside the authenticated v1 group, after rbacGuards is built:
//	RegisterShareLinkRoutes(v1, rbacGuards, params.ShareLinkHandler)
func RegisterShareLinkRoutes(r *gin.RouterGroup, g *rbacGuards, h *handler.ShareLinkHandler) {
	if h == nil {
		return
	}
	// 创建/列表走 :knowledge_id 所属 KB 的写权限链（own / org-shared editor），
	// 与 lifecycle/chunk 的 per-KB 矩阵一致；KBAccessWrite 同时会把请求租户
	// 上下文改写到 KB 归属租户，服务层再做一次租户兜底校验。
	share := r.Group("/knowledge/:knowledge_id/share-links")
	{
		share.POST("", g.Contributor(), g.KBAccessWriteFromKnowledgeIDParam("knowledge_id"), h.CreateShareLink)
		share.GET("", g.Contributor(), g.KBAccessWriteFromKnowledgeIDParam("knowledge_id"), h.ListShareLinks)
	}
	// 撤销：路由层只卡 Contributor（撤销是低风险动作，且不需要反查 KB）；
	// 租户隔离由服务层的 (tenant_id, id) 条件 UPDATE 兜底——跨租户/未知 id
	// 一律 404。
	r.DELETE("/share-links/:share_link_id", g.Contributor(), h.RevokeShareLink)
}

// RegisterShareLinkPublicRoutes mounts the anonymous resolve endpoint on
// the given pre-auth group. The group must be one that does NOT run the
// global Auth middleware (engine root or an equivalent public group) —
// see the file comment above for the exact assembly slot.
func RegisterShareLinkPublicRoutes(publicGroup *gin.RouterGroup, h *handler.ShareLinkHandler) {
	if h == nil || publicGroup == nil {
		return
	}
	// TODO(rate-limit): add a per-IP (and optionally per-token) limiter
	// middleware in front of the resolve handler — e.g. the same Redis
	// token-bucket the embed public endpoints use — before exposing this
	// on an internet-facing deployment. v1 relies on the uniform 404 and
	// per-visit audit rows.
	publicGroup.GET("/public/share/:token", h.ResolveShareLink)
}
