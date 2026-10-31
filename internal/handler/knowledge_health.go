package handler

import (
	"context"
	goerrors "errors"
	"strings"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// KnowledgeHealthHandler serves the knowledge-health dashboard endpoints
// (KnowAxis V3.0 §14.6 / §16.1 v1). The dashboard is read-only: GET returns
// the latest snapshot and computes one on miss; POST /compute is the explicit
// force-recompute.
type KnowledgeHealthHandler struct {
	healthService interfaces.KnowledgeHealthService
	// config honours the RBAC rollout switch for the query-param-dependent
	// tenant-scope gate (same semantics as middleware.RequireRole).
	config *config.Config
}

// NewKnowledgeHealthHandler constructs the handler.
func NewKnowledgeHealthHandler(
	healthService interfaces.KnowledgeHealthService, config *config.Config,
) *KnowledgeHealthHandler {
	return &KnowledgeHealthHandler{healthService: healthService, config: config}
}

// GetHealth godoc
// @Summary      查询知识健康度看板
// @Description  返回最新健康度快照（§14.6 六维度 + 0-100 健康分）；无快照时现算现返回。带 kb_id 为单库维度（Viewer+）；kb_id 缺省或 scope=tenant 为租户维度（需 Admin）。
// @Tags         知识健康度
// @Produce      json
// @Param        kb_id  query  string  false  "知识库ID（缺省为租户维度）"
// @Param        scope  query  string  false  "统计口径：tenant | knowledge_base（缺省按 kb_id 推断）"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {object}  errors.AppError
// @Failure      403  {object}  errors.AppError
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge-health [get]
func (h *KnowledgeHealthHandler) GetHealth(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	scope, scopeID, err := resolveKnowledgeHealthScope(c.Query("kb_id"), c.Query("scope"))
	if err != nil {
		c.Error(err)
		return
	}
	// scope=tenant 是租户级治理面：路由层只能先放行 Viewer（角色门槛依赖
	// query 参数），Admin 门槛在这里按口径判定。
	if scope == types.KnowledgeHealthScopeTenant && !h.mayViewTenantHealth(ctx) {
		c.Error(errors.NewForbiddenError("tenant-scope knowledge health requires workspace admin"))
		return
	}
	snapshot, err := h.healthService.Latest(ctx, caller.TenantID, scope, scopeID)
	if goerrors.Is(err, gorm.ErrRecordNotFound) {
		// 看板首次访问：现算现返回（追加一行快照）。
		snapshot, err = h.healthService.Compute(ctx, caller.TenantID, scopeID)
	}
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": snapshot})
}

// ComputeHealth godoc
// @Summary      强制重算知识健康度快照
// @Description  立即重算并追加一行快照。kb_id 缺省为租户维度；kb_id 不存在时 404。
// @Tags         知识健康度
// @Produce      json
// @Param        kb_id  query  string  false  "知识库ID（缺省为租户维度）"
// @Success      200  {object}  map[string]interface{}
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge-health/compute [post]
func (h *KnowledgeHealthHandler) ComputeHealth(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	snapshot, err := h.healthService.Compute(ctx, caller.TenantID, strings.TrimSpace(c.Query("kb_id")))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": snapshot})
}

// resolveKnowledgeHealthScope maps the query params onto a statistics scope.
// kb_id wins: present → knowledge_base scope (an explicit scope must agree).
// Without kb_id: empty or "tenant" → tenant scope; "knowledge_base" without
// kb_id → 400; anything else → 400.
func resolveKnowledgeHealthScope(kbID, scopeRaw string) (types.KnowledgeHealthScope, string, error) {
	kbID = strings.TrimSpace(kbID)
	switch types.KnowledgeHealthScope(strings.TrimSpace(scopeRaw)) {
	case types.KnowledgeHealthScopeKnowledgeBase:
		if kbID == "" {
			return "", "", errors.NewBadRequestError("kb_id is required when scope=knowledge_base")
		}
		return types.KnowledgeHealthScopeKnowledgeBase, kbID, nil
	case types.KnowledgeHealthScopeTenant:
		if kbID != "" {
			return "", "", errors.NewBadRequestError("scope=tenant cannot be combined with kb_id")
		}
		return types.KnowledgeHealthScopeTenant, "", nil
	case "":
		if kbID != "" {
			// 缺省口径跟随 kb_id：单库看板。
			return types.KnowledgeHealthScopeKnowledgeBase, kbID, nil
		}
		return types.KnowledgeHealthScopeTenant, "", nil
	default:
		return "", "", errors.NewBadRequestError("invalid scope, must be tenant or knowledge_base")
	}
}

// mayViewTenantHealth is the in-handler gate for the tenant-scope dashboard.
// Same authority ladder as InitializationHandler.canUpdateTenantModels:
// API-key scopes (the health routes are JWT-only, so defensive), then the
// JWT role ladder, then the shared RBAC rollout switch — role checks only
// block while RBAC enforcement is on.
func (h *KnowledgeHealthHandler) mayViewTenantHealth(ctx context.Context) bool {
	if scope, ok := types.TenantAPIKeyScopeFromContext(ctx); ok {
		return scope.FullAccess
	}
	if types.CallerFromContext(ctx).Role.HasPermission(types.TenantRoleAdmin) || types.IsSystemAdminFromContext(ctx) {
		return true
	}
	return h.config == nil || !h.config.Tenant.IsRBACEnforced()
}
