package handler

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// LearningCertificateHandler exposes learning-path certification
// (KnowAxis V3.0 §15.5).
type LearningCertificateHandler struct {
	certificates interfaces.LearningCertificateService
}

// NewLearningCertificateHandler constructs the handler.
func NewLearningCertificateHandler(
	certificates interfaces.LearningCertificateService,
) *LearningCertificateHandler {
	return &LearningCertificateHandler{certificates: certificates}
}

// CertifyLearningPath godoc
// @Summary      申请学习认证
// @Description  完成学习路径的全部步骤后颁发认证（KnowAxis V3.0 §15.5）。未完成时返回 eligible=false 与差多少步，而不是报错——前端据此提示还差什么。已有有效证书直接复用。
// @Tags         学习认证
// @Produce      json
// @Param        path_id       path   string  true   "学习路径ID"
// @Param        user_id       query  string  false  "被认证的用户（缺省为调用者本人）"
// @Param        valid_months  query  int     false  "有效期（月，默认 12；0 表示不过期）"
// @Success      200  {object}  map[string]interface{}  "{success, data: LearningCertification}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /learning-paths/{path_id}/certify [post]
func (h *LearningCertificateHandler) CertifyLearningPath(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	caller := types.CallerFromContext(ctx)
	userID := c.Query("user_id")
	// 为他人颁发认证是管理动作；默认只能给自己认证。
	if userID == "" || userID != caller.UserID {
		if !caller.Role.HasPermission(types.TenantRoleAdmin) {
			userID = caller.UserID
		}
	}
	validMonths := 12
	if raw := c.Query("valid_months"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 0 && parsed <= 120 {
			validMonths = parsed
		}
	}
	result, err := h.certificates.Certify(ctx, tenantID, c.Param("path_id"), userID, validMonths)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": result})
}

// ListLearningCertificates godoc
// @Summary      我的认证
// @Description  返回某位用户的全部学习认证，状态按读取时刻解析（过期自动显示 expired）。
// @Tags         学习认证
// @Produce      json
// @Param        user_id  query  string  false  "用户（缺省为调用者本人）"
// @Success      200  {object}  map[string]interface{}  "{success, data: [LearningCertificate]}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /learning-certificates [get]
func (h *LearningCertificateHandler) ListLearningCertificates(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	caller := types.CallerFromContext(ctx)
	userID := c.Query("user_id")
	if userID == "" || (userID != caller.UserID && !caller.Role.HasPermission(types.TenantRoleAdmin)) {
		userID = caller.UserID
	}
	rows, err := h.certificates.List(ctx, tenantID, userID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": rows})
}

// revokeCertificateRequest is the body of POST /learning-certificates/:id/revoke.
type revokeCertificateRequest struct {
	Reason string `json:"reason"`
}

// RevokeLearningCertificate godoc
// @Summary      撤销学习认证
// @Description  撤销一份认证并记录原因（§15.5 认证可撤销，例如政策变更或违规）。
// @Tags         学习认证
// @Accept       json
// @Produce      json
// @Param        id       path  string                   true  "证书ID"
// @Param        request  body  revokeCertificateRequest true  "撤销原因"
// @Success      200  {object}  map[string]interface{}  "{success, data: LearningCertificate}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /learning-certificates/{id}/revoke [post]
func (h *LearningCertificateHandler) RevokeLearningCertificate(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req revokeCertificateRequest
	_ = c.ShouldBindJSON(&req)
	caller := types.CallerFromContext(ctx)
	certificate, err := h.certificates.Revoke(ctx, tenantID, c.Param("id"), req.Reason, caller.UserID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": certificate})
}
