package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterWatermarkRoutes wires the §16.3 watermark ledger read.
//
// 权限 = Admin+：台账记的是"谁下载了什么"，本身包含用户行为与文档密级，
// 只能给治理角色看。
func RegisterWatermarkRoutes(
	r *gin.RouterGroup, h *handler.WatermarkHandler, g *rbacGuards,
) {
	if h == nil {
		return
	}
	r.GET("/watermark-records", g.Admin(), h.ListWatermarkRecords)
}
