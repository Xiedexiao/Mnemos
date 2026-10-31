package router

import (
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/gin-gonic/gin"
)

// RegisterReviewRoutes 装配知识审核流路由（KnowAxis V3.0 §6 的 v1 子集）。
//
// 装配方式（cmd/server 的 NewRouter 内，与其它 Register*Routes 并列，一行示例）：
//
//	reviewSvc := service.NewReviewService(
//	    repository.NewApprovalTaskRepository(db),
//	    repository.NewApprovalReceiptRepository(db),
//	    repository.NewApprovalFlowRepository(db),
//	    knowledgeRepo,   // interfaces.KnowledgeRepository（NewKnowledgeRepository(db)）
//	    chunkRepo,       // interfaces.ChunkRepository（NewChunkRepository(db)）
//	    lifecycleSvc,    // interfaces.KnowledgeLifecycleService（service.NewKnowledgeLifecycleService(...)）
//	    auditSvc,        // interfaces.AuditLogService（service.NewAuditLogService(...)）
//	)
//	router.RegisterReviewRoutes(apiV1, guards, handler.NewReviewHandler(reviewSvc))
//
// 权限矩阵（对照 rbac.go 的选守卫指引）：
//   - POST /knowledge/:id/review/submit —— 内容写边界：JWT Contributor+，
//     且对父 KB 有 write 权限（KBAccessWriteFromKnowledgeIDParam）；scoped
//     API key 需 ingest 能力（沿用 /knowledge 写路由的 apiKeyGroup 声明）。
//   - GET  /review/tasks、POST /review/tasks/:task_id/decide、
//     GET /review/receipts —— 租户治理/审计面：Admin+，且保持 JWT-only
//     （不进 API key 能力表 → 对 API key 默认拒绝；审批是"人"的责任，
//     与 activity 路由同一取舍）。
//   - GET /review/receipts/:receipt_id —— 低敏感回执查看：Viewer+，JWT-only。
//
// 注意：gin 的路由树在同一位置只允许一个通配符名，/knowledge 既有路由统一
// 用 :id（routes_knowledge.go），因此提交路由复用 :id（Handler 内双读兼容
// :knowledge_id）。审核流新增表/常量见 internal/types/review.go 与迁移
// migrations/versioned/000136_review_approval.up.sql。
func RegisterReviewRoutes(r *gin.RouterGroup, g *rbacGuards, h *handler.ReviewHandler) {
	if h == nil {
		return
	}

	// 提交审核：与文档写路由（k.POST("/:id/reparse") 等）同档的
	// Contributor + KB write 组合，scoped ingest key 可达。
	kgrp := r.Group("/knowledge")
	review := g.apiKeyGroup(kgrp, apiKeyIngest(apiKeyFullAccess()))
	review.POST("/:id/review/submit",
		g.Contributor(),
		g.KBAccessWriteFromKnowledgeIDParam("id"),
		h.SubmitReview,
	)

	// 待办 / 裁决 / 回执链：JWT-only 的治理面（注册在裸 group 上、不经
	// apiKeyGroup 声明 → API key 默认拒绝，见 rbac.go 的 default-deny 说明）。
	reviewOps := r.Group("/review")
	{
		reviewOps.GET("/tasks", g.Admin(), h.ListTasks)
		reviewOps.POST("/tasks/:task_id/decide", g.Admin(), h.Decide)
		// 链回放属于审计面：Admin+；单条回执查看是低敏感读取：Viewer+。
		reviewOps.GET("/receipts", g.Admin(), h.GetReceiptChain)
		reviewOps.GET("/receipts/:receipt_id", g.Viewer(), h.GetReceipt)
	}
}
