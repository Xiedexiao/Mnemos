package router

import (
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/gin-gonic/gin"
)

// RegisterSchemaProposalRoutes 装配本体建模提案路由（KnowAxis V3.0 §8.2/
// §8.4：文档驱动 AI 自动建模 → SchemaProposal → 兼容性检查 → 审批 → 落地）。
//
// 装配方式（internal/container/container.go 注册 provider，router.go 的
// RegisterRoutes 内与其它 Register*Routes 并列）：
//
//	// container.go（存储层 + 服务层 + handler）
//	must(container.Provide(repository.NewSchemaProposalRepository))
//	must(container.Provide(func(
//	    repo interfaces.SchemaProposalRepository,
//	    ontology interfaces.OntologyService,
//	    knowledges interfaces.KnowledgeRepository,
//	    chunks interfaces.ChunkRepository,
//	    models interfaces.ModelService,
//	    audit interfaces.AuditLogService,
//	) interfaces.SchemaProposalService {
//	    return service.NewSchemaProposalService(repo, ontology, knowledges, chunks, models, audit)
//	}))
//	must(container.Provide(handler.NewSchemaProposalHandler))
//
//	// router.go（与 RegisterOntologyRoutes 并列一行）
//	RegisterSchemaProposalRoutes(v1, params.SchemaProposalHandler, rbacGuards)
//
// 权限矩阵（对照 rbac.go 的选守卫指引，与 /ontology/schemas 同风格）：
//   - POST /ontology/proposals —— 建模提案创建（AI 自动建模 / 手工提交）：
//     Contributor+。AI 建模会消耗一次模型调用，但不改任何生效结构，故不
//     要求 Admin；审批才是治理动作。
//   - POST /ontology/proposals/:id/compatibility-check —— 只写提案自己的
//     compatibility_report：Contributor+。
//   - GET  /ontology/proposals、/ontology/proposals/:id —— 租户内低敏感读取：
//     Viewer+。
//   - POST /ontology/proposals/:id/approve|reject|apply —— 本体结构的实际
//     治理动作，直接决定生效 Schema 的内容：Admin+（服务层另有四眼校验：
//     审批人必须 != 提案创建人）。
//   - 整组注册在裸 group 上、不经 apiKeyGroup 声明 → API key 默认拒绝，与
//     /ontology/schemas 治理面同一取舍（本体建模是"人"的责任，JWT-only）。
//
// 表结构见 migrations/versioned/000155_schema_proposals.up.sql，领域模型见
// internal/types/schema_proposal.go。
func RegisterSchemaProposalRoutes(
	r *gin.RouterGroup, h *handler.SchemaProposalHandler, g *rbacGuards,
) {
	if h == nil {
		return
	}
	proposals := r.Group("/ontology/proposals")
	{
		// 创建：文档驱动自动建模（knowledge_ids）或手工提交 payload。
		proposals.POST("", g.Contributor(), h.CreateSchemaProposal)
		// 查询（status 过滤）与详情。
		proposals.GET("", g.Viewer(), h.ListSchemaProposals)
		proposals.GET("/:id", g.Viewer(), h.GetSchemaProposal)
		// 兼容性检查：只写提案自身的报告，不改生效本体。
		proposals.POST("/:id/compatibility-check", g.Contributor(), h.CheckSchemaProposalCompatibility)
		// 治理动作：批准/驳回（四眼）与落地（写 ontology_schemas）。
		proposals.POST("/:id/approve", g.Admin(), h.ApproveSchemaProposal)
		proposals.POST("/:id/reject", g.Admin(), h.RejectSchemaProposal)
		proposals.POST("/:id/apply", g.Admin(), h.ApplySchemaProposal)
	}
}
