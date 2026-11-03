package handler

import (
	// Referenced only by the @Failure annotations (errors.AppError); swag
	// resolves annotation types through the file's imports.
	_ "github.com/Tencent/WeKnora/internal/errors"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

// RecycleBinHandler serves the tenant recycle-bin endpoints
// (KnowAxis V3.0 §5.5). Admin-only: restore/purge decisions reshape what
// every member sees, and purge hard-deletes rows — a tenant-wide governance
// surface (对照 rbac.go 的选守卫指引：租户级治理面 → Admin).
type RecycleBinHandler struct {
	recycleBinService interfaces.RecycleBinService
}

// NewRecycleBinHandler constructs the handler.
func NewRecycleBinHandler(recycleBinService interfaces.RecycleBinService) *RecycleBinHandler {
	return &RecycleBinHandler{recycleBinService: recycleBinService}
}

// ListRecycleBin godoc
// @Summary      查看回收站
// @Description  列出本工作区的回收站条目（新→旧）；entity_type 可选过滤（v1 仅 knowledge）；restored_at 非空表示已恢复。
// @Tags         回收站
// @Produce      json
// @Param        entity_type  query  string  false  "实体类型过滤（knowledge）"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {object}  errors.AppError
// @Security     Bearer
// @Router       /recycle-bin [get]
func (h *RecycleBinHandler) ListRecycleBin(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	entries, err := h.recycleBinService.List(ctx, caller.TenantID, c.Query("entity_type"))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": entries})
}

// RestoreRecycleEntry godoc
// @Summary      从回收站恢复
// @Description  恢复一条软删的知识条目：清除 deleted_at 并盖 restored_at（该条目退出 30 天清空倒计时）。v1 范围：恢复元数据行；分块/向量/文件在删除时已释放，恢复后需重新 ingest。
// @Tags         回收站
// @Produce      json
// @Param        recycle_id  path  string  true  "回收站条目ID"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {object}  errors.AppError  "已恢复"
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Router       /recycle-bin/{recycle_id}/restore [post]
func (h *RecycleBinHandler) RestoreRecycleEntry(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	entry, err := h.recycleBinService.Restore(ctx, caller.TenantID, c.Param("recycle_id"), caller.UserID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": entry})
}
