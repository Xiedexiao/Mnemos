package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// NetworkAccess rejects requests whose source address is outside the tenant's
// allow-list (KnowAxis V3.0 §16.3 IP 限制).
//
// 必须挂在认证之后：判定需要租户上下文，而租户上下文由认证中间件写入。
// 未认证请求在这里看不到租户，直接放行交给下游认证——认证会拒绝它们，
// 而在这里拒绝会掩盖真正的失败原因。
func NetworkAccess(service interfaces.NetworkAccessService) gin.HandlerFunc {
	return func(c *gin.Context) {
		if service == nil {
			c.Next()
			return
		}
		tenantID, ok := types.TenantIDFromContext(c.Request.Context())
		if !ok || tenantID == 0 {
			c.Next()
			return
		}
		if service.Allows(c.Request.Context(), tenantID, c.ClientIP()) {
			c.Next()
			return
		}
		c.Error(apperrors.NewForbiddenError(
			"请求来源地址不在本工作区的允许范围内"))
		c.AbortWithStatus(http.StatusForbidden)
	}
}
