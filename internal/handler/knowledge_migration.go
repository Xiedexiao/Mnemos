package handler

import (
	"github.com/gin-gonic/gin"

	// Referenced only by the @Failure annotations (apperrors.AppError); swag
	// resolves annotation types through the file's imports.
	_ "github.com/Tencent/WeKnora/internal/errors"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// KnowledgeMigrationHandler exposes the §16.3 bulk import migration tool.
type KnowledgeMigrationHandler struct {
	migration interfaces.KnowledgeMigrationService
}

// NewKnowledgeMigrationHandler constructs the handler.
func NewKnowledgeMigrationHandler(
	migration interfaces.KnowledgeMigrationService,
) *KnowledgeMigrationHandler {
	return &KnowledgeMigrationHandler{migration: migration}
}

// ImportKnowledgeManifest godoc
// @Summary      批量迁移导入
// @Description  按清单批量导入知识（KnowAxis V3.0 §16.3 迁移工具）。支持目录映射、密级、标签与源版本号；逐条独立成败并如实返回每条结果；相同 external_id 重跑时更新而非重复创建；dry_run 只校验不写入。
// @Tags         知识管理
// @Accept       json
// @Produce      json
// @Param        request  body  types.MigrationManifest  true  "迁移清单"
// @Success      200  {object}  map[string]interface{}  "{success, data: MigrationReport}"
// @Failure      400  {object}  apperrors.AppError
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge/migrate [post]
func (h *KnowledgeMigrationHandler) ImportKnowledgeManifest(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var manifest types.MigrationManifest
	if err := c.ShouldBindJSON(&manifest); err != nil {
		c.Error(apperrors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	report, err := h.migration.Import(ctx, tenantID, caller.UserID, manifest)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": report})
}
