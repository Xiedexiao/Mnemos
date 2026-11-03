package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterLearningCertificateRoutes wires learning-path certification
// (KnowAxis V3.0 §15.5).
//
// 权限矩阵：
//   - POST /learning-paths/:path_id/certify —— 完成即认证：Viewer+（服务层
//     校验进度，并只允许给自己认证；为他人认证需要 Admin）。
//   - GET /learning-certificates —— 读自己的证书：Viewer+；读他人需要 Admin。
//   - POST /learning-certificates/:id/revoke —— 撤销资格：Admin+。
func RegisterLearningCertificateRoutes(
	r *gin.RouterGroup, h *handler.LearningCertificateHandler, g *rbacGuards,
) {
	if h == nil {
		return
	}
	r.POST("/learning-paths/:path_id/certify", g.Viewer(), h.CertifyLearningPath)
	certificates := r.Group("/learning-certificates")
	{
		certificates.GET("", g.Viewer(), h.ListLearningCertificates)
		certificates.POST("/:id/revoke", g.Admin(), h.RevokeLearningCertificate)
	}
}
