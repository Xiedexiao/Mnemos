package handler

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// WatermarkHandler exposes the §16.3 download trace ledger.
type WatermarkHandler struct {
	watermarks interfaces.WatermarkService
}

// NewWatermarkHandler constructs the handler.
func NewWatermarkHandler(watermarks interfaces.WatermarkService) *WatermarkHandler {
	return &WatermarkHandler{watermarks: watermarks}
}

// ListWatermarkRecords godoc
// @Summary      水印下载台账
// @Description  按密级受控的下载/导出台账（KnowAxis V3.0 §16.3）：谁、何时、哪一篇、什么密级、水印文本。public 级下载不入账。
// @Tags         空间管理
// @Produce      json
// @Param        knowledge_id  query  string  false  "按知识过滤"
// @Param        user_id       query  string  false  "按用户过滤"
// @Param        limit         query  int     false  "条数（默认 100）"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /watermark-records [get]
func (h *WatermarkHandler) ListWatermarkRecords(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "100"))
	if err != nil || limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, listErr := h.watermarks.List(ctx, tenantID,
		strings.TrimSpace(c.Query("knowledge_id")),
		strings.TrimSpace(c.Query("user_id")), limit)
	if listErr != nil {
		c.Error(listErr)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": rows})
}
