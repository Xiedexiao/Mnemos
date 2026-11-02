package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterKnowledgeTemplateRoutes wires the template center and field
// governance (KnowAxis V3.0 §3.2 自定义字段 / §4.7 模板与字段治理).
//
// 权限矩阵：
//   - GET /knowledge-templates、/knowledge-templates/:key —— 录入表单要用的
//     结构定义，任何能录入的人都要读：Viewer+。
//   - POST /knowledge-templates —— 新增模板版本会改变所有新知识的录入结构，
//     并按字段差异生成迁移任务：Admin+。
//   - POST /knowledge-templates/:key/validate —— 纯校验，不改数据：Contributor+
//     （录入者在提交前先校验）。
//   - GET/PATCH /knowledge-field-migrations —— 治理待办：Admin+。
//
// 全部 JWT-only：字段可见性（editable_role）与召回字段属于治理配置，不该
// 由 API key 批量改写。
func RegisterKnowledgeTemplateRoutes(
	r *gin.RouterGroup, h *handler.KnowledgeTemplateHandler, g *rbacGuards,
) {
	if h == nil {
		return
	}
	templates := r.Group("/knowledge-templates")
	{
		templates.GET("", g.Viewer(), h.ListKnowledgeTemplates)
		templates.POST("", g.Admin(), h.SaveKnowledgeTemplate)
		templates.GET("/:key", g.Viewer(), h.GetKnowledgeTemplate)
		templates.POST("/:key/validate", g.Contributor(), h.ValidateKnowledgeFields)
	}
	migrations := r.Group("/knowledge-field-migrations")
	{
		migrations.GET("", g.Admin(), h.ListFieldMigrations)
		migrations.PATCH("/:id", g.Admin(), h.ResolveFieldMigration)
	}
}
