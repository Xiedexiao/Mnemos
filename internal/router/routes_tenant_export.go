package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterTenantExportRoutes wires the §16.3 tenant-level logical export.
//
// 权限 = Owner 且 JWT-only。导出是**整租户**的数据外流，比任何单篇下载都重：
//   - Owner 是唯一对"整个工作区的数据搬走"负责的角色；
//   - 不开口子给 API key：一次调用就能带走全租户数据，不适合机器身份；
//   - 高密级内容另需已批准的 export request（四眼），由服务层强制。
func RegisterTenantExportRoutes(
	r *gin.RouterGroup, h *handler.TenantExportHandler, g *rbacGuards,
) {
	if h == nil {
		return
	}
	r.GET("/tenant-export/plan", g.Owner(), h.PlanTenantExport)
	r.GET("/tenant-export", g.Owner(), h.ExportTenant)
}
