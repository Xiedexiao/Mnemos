package router

import (
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/gin-gonic/gin"
)

// RegisterKnowledgeCandidateRoutes 装配过程经验候选卡片路由（KnowAxis
// V3.0 §10 的 v1 子集：候选卡片 + 审核发布闭环；业务事件连接器后续接
// datasource 框架）。
//
// 装配方式（cmd/server 的 NewRouter 内，与其它 Register*Routes 并列，一行示例）：
//
//	candidateSvc := service.NewKnowledgeCandidateService(
//	    repository.NewKnowledgeCandidateRepository(db), // interfaces.KnowledgeCandidateRepository
//	    knowledgeRepo, // interfaces.KnowledgeRepository（NewKnowledgeRepository(db)）
//	    kbRepo,        // interfaces.KnowledgeBaseRepository（NewKnowledgeBaseRepository(db)）
//	    auditSvc,      // interfaces.AuditLogService（service.NewAuditLogService(...)）
//	)
//	router.RegisterKnowledgeCandidateRoutes(apiV1, guards, handler.NewKnowledgeCandidateHandler(candidateSvc))
//
// 权限矩阵（对照 rbac.go 的选守卫指引）：
//   - POST /knowledge-candidates —— 创建候选卡片：Contributor+（手工与
//     未来连接器共用入口；业务事件连接器接入 datasource 框架后走其自身
//     鉴权，不经本路由）。
//   - GET  /knowledge-candidates、GET /knowledge-candidates/:id —— 沉淀
//     进度是租户内低敏感信息：Viewer+。
//   - POST /knowledge-candidates/:id/confirm —— 两段确认的第一段
//     （candidate→in_review）由提交人触发，挂 Contributor+；第二段
//     （in_review→confirmed，即审核人裁决）要求 Admin+，在服务层用
//     caller.Role 兜底强制（types.TenantRoleAdmin），路由层无法表达
//     "按状态分角色"，故守卫放一档、服务层收紧。
//   - POST /knowledge-candidates/:id/publish、/:id/reject —— 租户治理
//     动作：Admin+。
//   - POST /knowledge-candidates/:id/withdraw —— 提交人撤回：Contributor+
//     （仅提交人本人可撤，服务层校验）。
//   - 整组注册在裸 group 上、不经 apiKeyGroup 声明 → API key 默认拒绝，
//     与 /review 治理面同一取舍：经验确认与发布是"人"的责任，JWT-only。
//
// 路由参数统一 :id 风格（gin 同一位置只允许一个通配符名；本仓库既有路由
// 亦统一 :id）。状态机与表结构见 internal/types/knowledge_candidate.go 与
// 迁移 migrations/versioned/000144_knowledge_candidates.up.sql。
func RegisterKnowledgeCandidateRoutes(r *gin.RouterGroup, g *rbacGuards, h *handler.KnowledgeCandidateHandler) {
	if h == nil {
		return
	}
	candidates := r.Group("/knowledge-candidates")
	{
		// 创建（手工/未来连接器共用）。
		candidates.POST("", g.Contributor(), h.Create)
		// 查询：列表（status/level 过滤）与详情。
		candidates.GET("", g.Viewer(), h.List)
		candidates.GET("/:id", g.Viewer(), h.Get)
		// 状态机动作：confirm（candidate→in_review 提交人触发；
		// in_review→confirmed 审核人 Admin+，服务层强制）→
		// publish（confirmed→published，Admin+，L3 有 §10.4 门禁）|
		// reject（in_review→rejected，Admin+）| withdraw（提交人本人）。
		candidates.POST("/:id/confirm", g.Contributor(), h.Confirm)
		candidates.POST("/:id/publish", g.Admin(), h.Publish)
		candidates.POST("/:id/reject", g.Admin(), h.Reject)
		candidates.POST("/:id/withdraw", g.Contributor(), h.Withdraw)
	}
}
