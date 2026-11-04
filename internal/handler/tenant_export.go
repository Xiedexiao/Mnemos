package handler

import (
	"github.com/gin-gonic/gin"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// TenantExportHandler exposes the §16.3 logical export.
type TenantExportHandler struct {
	exports interfaces.TenantExportService
}

// NewTenantExportHandler constructs the handler.
func NewTenantExportHandler(exports interfaces.TenantExportService) *TenantExportHandler {
	return &TenantExportHandler{exports: exports}
}

// PlanTenantExport godoc
// @Summary      导出预检
// @Description  返回一次租户级逻辑导出会包含哪些表、各多少行，以及需要的最高密级（KnowAxis V3.0 §16.3）。secret/confidential 内容的导出需先取得已批准的 export request，把其 ID 传给导出接口。**这是逻辑导出，不是数据库备份**：不含索引、序列与 WAL；凭据、会话令牌与邀请链接被显式排除。
// @Tags         数据导出
// @Produce      json
// @Success      200  {object}  map[string]interface{}  "{success, data: TenantExportManifest}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /tenant-export/plan [get]
func (h *TenantExportHandler) PlanTenantExport(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	manifest, err := h.exports.Plan(ctx, tenantID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": manifest})
}

// ExportTenant godoc
// @Summary      执行逻辑导出
// @Description  把当前租户的白名单表导出为 JSON 清单（KnowAxis V3.0 §16.3）。secret/confidential 内容的导出必须带 `export_request_id`（已批准的 export request）；没有凭据直接 403，而不是"先导出再补审批"——数据一旦流出无法召回。单表失败或超限会写进清单说明，不让整次导出静默少一张表。
// @Tags         数据导出
// @Produce      json
// @Param        export_request_id  query  string  false  "已批准的导出申请 ID（高密级导出必填）"
// @Success      200  {object}  map[string]interface{}  "{success, data: TenantExportDocument}"
// @Failure      403  {object}  apperrors.AppError  "高密级导出缺少已批准的申请"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /tenant-export [get]
func (h *TenantExportHandler) ExportTenant(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	caller := types.CallerFromContext(ctx)
	document, err := h.exports.Export(ctx, tenantID, caller.UserID,
		c.Query("export_request_id"))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": document})
}
