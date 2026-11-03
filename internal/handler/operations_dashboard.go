package handler

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// OperationsDashboardHandler exposes the §16.1 operations dashboard.
type OperationsDashboardHandler struct {
	dashboard interfaces.OperationsDashboardService
}

// NewOperationsDashboardHandler constructs the handler.
func NewOperationsDashboardHandler(
	dashboard interfaces.OperationsDashboardService,
) *OperationsDashboardHandler {
	return &OperationsDashboardHandler{dashboard: dashboard}
}

// GetOperationsDashboard godoc
// @Summary      运营看板
// @Description  一次返回知识资产概况、治理待办（待审/待复审/过期/冲突/质量任务/影响任务/缺口/迁移/权限违规）、审核效率（通过率、平均决策时长、逾期）与搜索质量（KnowAxis V3.0 §16.1）。所有数字直接来自对应垂直领域的表，不重新定义口径。
// @Tags         运营看板
// @Produce      json
// @Param        window_days  query  int  false  "统计窗口（天，默认 30，上限 365）"
// @Success      200  {object}  map[string]interface{}  "{success, data: OperationsDashboard}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /operations/dashboard [get]
func (h *OperationsDashboardHandler) GetOperationsDashboard(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	windowDays, err := strconv.Atoi(c.DefaultQuery("window_days", "30"))
	if err != nil || windowDays <= 0 || windowDays > 365 {
		windowDays = 30
	}
	dashboard, getErr := h.dashboard.Get(ctx, tenantID, windowDays)
	if getErr != nil {
		c.Error(getErr)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": dashboard})
}
