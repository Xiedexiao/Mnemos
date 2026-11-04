package router

import (
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/gin-gonic/gin"
)

// RegisterGraphFactRoutes 装配图谱事实层路由（B 期图谱增强 v1：GraphFact
// 事实层 + 三级置信度 + 证据绑定 + GraphCommit 版本化 + 路径/影响查询）。
// 数据在 Postgres（迁移 000151），Neo4j 既有投影与 graph_search 不受影响。
//
// 装配方式（cmd/server → internal/router/router.go 的 RegisterRoutes 内，
// 与其它 Register*Routes 并列）：
//
//	graphFactRepo := repository.NewGraphFactRepository(db)
//	graphFactSvc := service.NewGraphFactService(graphFactRepo)
//	RegisterGraphFactRoutes(v1, rbacGuards, handler.NewGraphFactHandler(graphFactSvc))
//
// 权限矩阵（对照 rbac.go 的选守卫指引，与 ontology/review 同风格）：
//   - POST /graph/facts/entities、POST /graph/facts/commit —— 图内容写边界：
//     Contributor+（抽取管线/治理动作写事实层）。
//   - POST /graph/commits/:id/rollback —— 破坏性治理动作：Admin+。
//   - GET /graph/entities、/graph/path、/graph/impact —— 租户图读取面：
//     Viewer+。
//   - GET/PUT /graph/extraction-cache/:chunk_id —— 抽取管线内部的增量缓存
//     读写：Contributor+（读也收 Contributor，避免把缓存行内容当租户图
//     数据外泄给纯浏览角色）。
//     全部 JWT-only：注册在裸 group 上、不经 apiKeyGroup 声明 → API key 默认
//     拒绝（事实层写入是治理责任，与 ontology 路由同一取舍）。
func RegisterGraphFactRoutes(r *gin.RouterGroup, g *rbacGuards, h *handler.GraphFactHandler) {
	if h == nil {
		return
	}
	graph := r.Group("/graph")
	{
		// 写入面：实体批量归一 upsert / 变更集提交。
		graph.POST("/facts/entities", g.Contributor(), h.UpsertEntities)
		graph.POST("/facts/commit", g.Contributor(), h.CreateCommit)
		// 治理面：回滚是破坏性动作（反向标记 superseded），Admin+。
		graph.POST("/commits/:id/rollback", g.Admin(), h.RollbackCommit)
		// 变更集内容与时间点回放（§11.4 可回归 / 可重放）。都是只读审计面，
		// 但会暴露全图的变更历史与历史快照，与回滚同档：Admin+。
		graph.GET("/commits/:id", g.Admin(), h.DiffCommit)
		graph.GET("/replay", g.Admin(), h.ReplayGraph)

		// 读取面：实体检索 / 路径 / 影响面。
		graph.GET("/entities", g.Viewer(), h.SearchEntities)
		graph.GET("/path", g.Viewer(), h.PathQuery)
		graph.GET("/impact", g.Viewer(), h.ImpactQuery)

		// 抽取缓存：管线服务内部用（GET 带 content_hash+schema_version 即
		// 命中判断，不带即管理面查看；PUT 写入/覆盖）。
		graph.GET("/extraction-cache/:chunk_id", g.Contributor(), h.GetExtractionCache)
		graph.PUT("/extraction-cache/:chunk_id", g.Contributor(), h.LetExtractionCache)
	}
}
