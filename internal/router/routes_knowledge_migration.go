package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterKnowledgeMigrationRoutes wires the §16.3 bulk import migration tool.
//
// 权限 = Admin+：一次迁移会批量写入成千条知识，并可能带入密级与目录结构，
// 与单篇上传不是一个量级的动作。
func RegisterKnowledgeMigrationRoutes(
	r *gin.RouterGroup, h *handler.KnowledgeMigrationHandler, g *rbacGuards,
) {
	if h == nil {
		return
	}
	r.POST("/knowledge/migrate", g.Admin(), h.ImportKnowledgeManifest)
}
