package router

import (
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/gin-gonic/gin"
)

// RegisterChangeRequestRoutes 装配变更单 + 影响任务路由（KnowAxis V3.0
// §7.2/§7.6 的 v1 子集）。
//
// 装配方式（cmd/server 的 NewRouter 内，与其它 Register*Routes 并列，一行示例）：
//
//	crSvc := service.NewChangeRequestService(
//	    repository.NewChangeRequestRepository(db), // interfaces.ChangeRequestRepository
//	    knowledgeRepo, // interfaces.KnowledgeRepository（NewKnowledgeRepository(db)）
//	    lifecycleSvc,  // interfaces.KnowledgeLifecycleService（service.NewKnowledgeLifecycleService(...)）
//	    auditSvc,      // interfaces.AuditLogService（service.NewAuditLogService(...)）
//	)
//	router.RegisterChangeRequestRoutes(apiV1, guards, handler.NewChangeRequestHandler(crSvc))
//
// 权限矩阵（对照 rbac.go 的选守卫指引）：
//   - 变更单没有"创建者自管"语义 —— 它是租户治理动作（批量改他人条目的
//     生效状态），所以写路由一律 g.Admin()，读路由 g.Viewer()（任务书口径），
//     不走 OwnedXxxOrAdmin。
//   - 整组注册在裸 group 上、不经 apiKeyGroup 声明 → API key 默认拒绝，
//     与 /review 治理面同一取舍：变更是"人"的责任，JWT-only。
//
// 路由参数统一 :id 风格（gin 同一位置只允许一个通配符名；本仓库既有路由
// 亦统一 :id）。状态机与表结构见 internal/types/change_request.go 与迁移
// migrations/versioned/000142_change_requests.up.sql。
func RegisterChangeRequestRoutes(r *gin.RouterGroup, g *rbacGuards, h *handler.ChangeRequestHandler) {
	if h == nil {
		return
	}
	crOps := r.Group("/change-requests")
	{
		// 单据 CRUD / 查询。
		crOps.POST("", g.Admin(), h.Create)
		crOps.GET("", g.Viewer(), h.List)
		crOps.GET("/:id", g.Viewer(), h.Get)
		// 条目管理：仅 draft/analyzing 可改（服务层校验状态机）。
		crOps.POST("/:id/items", g.Admin(), h.AddItem)
		crOps.DELETE("/:id/items/:knowledge_id", g.Admin(), h.RemoveItem)
		// 状态机动作：analyze(draft→analyzing) → submit(→in_approval) →
		// schedule(→scheduled) → publish(→publishing→verifying) →
		// close(→closed) | rollback(→rolled_back)。
		crOps.POST("/:id/analyze", g.Admin(), h.Analyze)
		crOps.POST("/:id/submit", g.Admin(), h.SubmitForApproval)
		crOps.POST("/:id/schedule", g.Admin(), h.Schedule)
		crOps.POST("/:id/publish", g.Admin(), h.Publish)
		crOps.POST("/:id/close", g.Admin(), h.Close)
		crOps.POST("/:id/rollback", g.Admin(), h.Rollback)
		// 影响任务闭环：done/waived + note。
		crOps.POST("/:id/impact-tasks/:task_id", g.Admin(), h.UpdateImpactTask)
	}
}
