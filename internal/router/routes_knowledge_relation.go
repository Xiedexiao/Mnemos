package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterKnowledgeRelationRoutes wires knowledge-to-knowledge relations
// (KnowAxis V3.0 §4.5 知识关联).
//
// 权限矩阵：
//   - GET 列表 / delete-impact —— 阅读页要展示关联：Viewer+ 加 KB 读权限。
//   - POST 建立关联、DELETE 解除 —— 改内容结构：Contributor+ 加 KB 写权限。
//
// JWT-only：关联会暴露两篇文档的存在性，机器身份不该批量探测。
func RegisterKnowledgeRelationRoutes(
	r *gin.RouterGroup, h *handler.KnowledgeRelationHandler, g *rbacGuards,
) {
	if h == nil {
		return
	}
	relations := r.Group("/knowledge/:id/relations")
	{
		relations.GET("", g.Viewer(), g.KBAccessReadFromKnowledgeIDParam("id"), h.ListKnowledgeRelations)
		// delete-impact 是静态子段，注册在根之前避免与方法组合混淆。
		relations.GET("/delete-impact", g.Viewer(),
			g.KBAccessReadFromKnowledgeIDParam("id"), h.GetRelationDeleteImpact)
		relations.POST("", g.Contributor(),
			g.KBAccessWriteFromKnowledgeIDParam("id"), h.LinkKnowledgeRelation)
		relations.DELETE("", g.Contributor(),
			g.KBAccessWriteFromKnowledgeIDParam("id"), h.UnlinkKnowledgeRelation)
	}
}
