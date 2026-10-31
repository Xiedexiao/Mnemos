package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterExportDLPRoutes 装配导出管控与 DLP 巡检路由（KnowAxis V3.0 §2.2
// export 动作 / §13 生成内容校验 v1）。
//
// 装配方式（internal/router/router.go 的 NewRouter 内，与其它 Register*
// 并列；RouterParams 增加两个字段，container.go 增加四条 Provide）：
//
//	RegisterExportDLPRoutes(v1, params.ExportControlHandler, params.DLPHandler, rbacGuards)
//
// 权限矩阵（对照 rbac.go 的选守卫指引）：
//   - POST /export-requests、GET /export-requests —— 申请/查看是成员级
//     面：Viewer+（导出申请人人可提，审批痕迹人人可查自己的租户内列表）。
//   - POST /export-requests/:id/decide —— 审批是租户治理面：Admin+（服务
//     层另做四眼校验：审批人 ≠ 申请人）。
//   - POST /export-requests/:id/fulfill —— 批准后的执行动作：Viewer+
//     （申请人拿到 approved 后自己执行导出；服务层强制只有 approved 可
//     fulfill，状态机不靠路由把守）。
//   - GET /dlp/findings、POST /dlp/patterns —— 安全巡检/规则治理面：
//     Admin+（findings 含敏感样本元数据，规则影响全租户扫描行为）。
//
// 全部路由 JWT-only：注册在裸 group 上、不经 apiKeyGroup 声明，scoped
// API key 默认拒绝（导出与 DLP 治理都是"人"的责任，与 /review、/glossary
// 治理面同一取舍，见 rbac.go 的 default-deny 说明）。
//
// 扫描不暴露 HTTP：DLPService.ScanText 是内部服务，问答输出挂钩在
// internal/handler/session（saveEvidencePack 附近），见
// internal/types/interfaces/export_dlp.go 的装配说明。
func RegisterExportDLPRoutes(
	r *gin.RouterGroup,
	e *handler.ExportControlHandler,
	d *handler.DLPHandler,
	g *rbacGuards,
) {
	if e != nil {
		exports := r.Group("/export-requests")
		{
			// 申请/列表 = Viewer+；裁决 = Admin+（四眼在服务层强制）。
			exports.POST("", g.Viewer(), e.CreateRequest)
			exports.GET("", g.Viewer(), e.ListRequests)
			exports.GET("/:id", g.Viewer(), e.GetRequest)
			exports.POST("/:id/decide", g.Admin(), e.Decide)
			exports.POST("/:id/fulfill", g.Viewer(), e.Fulfill)
		}
	}
	if d != nil {
		dlp := r.Group("/dlp")
		{
			// DLP 命中巡检与自定义规则 = Admin+。
			dlp.GET("/findings", g.Admin(), d.ListFindings)
			dlp.POST("/patterns", g.Admin(), d.AddPattern)
		}
	}
}
