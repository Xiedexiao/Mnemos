package handler

import (
	"github.com/gin-gonic/gin"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// IndexCheckHandler exposes the §17.1 index-coverage self-check.
type IndexCheckHandler struct {
	checks interfaces.IndexCheckService
}

// NewIndexCheckHandler constructs the handler.
func NewIndexCheckHandler(checks interfaces.IndexCheckService) *IndexCheckHandler {
	return &IndexCheckHandler{checks: checks}
}

// RunIndexCheck godoc
// @Summary      索引覆盖自检
// @Description  对热路径查询逐条取执行计划，报告哪些是全表扫描（KnowAxis V3.0 §17.1）。检查的是**计划**而不是耗时：耗时依赖数据量与机器，而"是否全表扫描"与规模无关，今天就能判定明天会不会出问题。单条检查失败标 unknown 而不当作通过。全表扫描的查询会随表增长线性变慢。
// @Tags         运维
// @Produce      json
// @Success      200  {object}  map[string]interface{}  "{success, data: IndexCheckReport}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /operations/index-check [get]
func (h *IndexCheckHandler) RunIndexCheck(c *gin.Context) {
	ctx := c.Request.Context()
	if _, ok := types.TenantIDFromContext(ctx); !ok {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	report, err := h.checks.Run(ctx)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": report})
}
