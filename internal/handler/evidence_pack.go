package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/Tencent/WeKnora/internal/application/service"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

// EvidencePackHandler 暴露 Evidence Pack 查询面（KnowAxis V3.0 §13.4 的
// v1 子集）：按 request_id 回放一次问答的整条链路、按会话列证据包时间线。
// 租户/用户身份一律从 types.CallerFromContext 取（路由层的 RBAC 守卫保证
// 其可信）；服务只按租户隔离，"本人或同租户 Admin"的可见性由路由层
// Viewer+ 与调用方的会话归属共同控制。写入发生在聊天管道挂钩（见
// routes_evidence.go 的装配说明），本 handler 只读。
type EvidencePackHandler struct {
	evidenceService interfaces.EvidencePackService
}

// NewEvidencePackHandler constructs the evidence pack handler.
func NewEvidencePackHandler(evidenceService interfaces.EvidencePackService) *EvidencePackHandler {
	return &EvidencePackHandler{evidenceService: evidenceService}
}

// evidenceSessionIDParam 兼容两种挂载：/sessions/:session_id/... 与
// /sessions/:id/...。gin 的路由树在同一位置只允许一个通配符名，本仓库
// /sessions 的 GET 路由统一用 :id（routes_chat.go），因此本路由复用 :id；
// handler 双读保持对两种挂载都可用。
func evidenceSessionIDParam(c *gin.Context) string {
	if v := c.Param("session_id"); v != "" {
		return v
	}
	return c.Param("id")
}

// GetByRequestID godoc
// @Summary      按请求ID回放证据包
// @Description  返回一次问答（request_id）的完整证据包：检索候选快照、上下文编译摘要、模型/提示词版本、答案、引用清单与当时权限基线，附哈希链锚点（hash_chain_prev/row）供链校验。
// @Tags         证据包
// @Produce      json
// @Param        request_id  path  string  true  "请求追踪ID"
// @Success      200  {object}  map[string]interface{}  "{success, data: 证据包}"
// @Failure      401  {object}  apperrors.AppError  "缺少租户上下文"
// @Failure      404  {object}  apperrors.AppError  "证据包不存在"
// @Security     Bearer
// @Router       /evidence-packs/{request_id} [get]
func (h *EvidencePackHandler) GetByRequestID(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	if caller.TenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is missing"))
		return
	}
	requestID := c.Param("request_id")
	if requestID == "" {
		c.Error(apperrors.NewBadRequestError("request id is required"))
		return
	}

	pack, err := h.evidenceService.GetByRequestID(ctx, requestID)
	if err != nil {
		writeEvidencePackError(c, ctx, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": pack})
}

// ListBySession godoc
// @Summary      会话证据包时间线
// @Description  返回该会话最近 limit 条证据包，按创建时间正序（可逐条回放整段问答链路）。limit 默认 50，硬上限 200。
// @Tags         证据包
// @Produce      json
// @Param        session_id  path  string  true  "会话ID"
// @Param        limit  query  int  false  "条数，1-200"  default(50)  extensions(x-agent)
// @Success      200  {object}  map[string]interface{}  "{success, data: [证据包]}"
// @Failure      401  {object}  apperrors.AppError  "缺少租户上下文"
// @Security     Bearer
// @Router       /sessions/{session_id}/evidence-packs [get]
func (h *EvidencePackHandler) ListBySession(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	if caller.TenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is missing"))
		return
	}
	sessionID := evidenceSessionIDParam(c)
	if sessionID == "" {
		c.Error(apperrors.NewBadRequestError("session id is required"))
		return
	}
	// limit 解析容错：非法值交给服务层钳制，与 audit-log 游标同风格。
	limit := 0
	if raw := c.Query("limit"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			limit = v
		}
	}

	packs, err := h.evidenceService.ListBySession(ctx, sessionID, limit)
	if err != nil {
		writeEvidencePackError(c, ctx, err)
		return
	}
	if packs == nil {
		packs = []*types.EvidencePack{}
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": packs})
}

// writeEvidencePackError 把证据包业务哨兵错误映射为 HTTP 语义：
// 参数缺失 → 400，缺租户上下文 → 401，资源缺失 → 404，
// 其余记日志后 500（不外泄内部细节）。
func writeEvidencePackError(c *gin.Context, ctx context.Context, err error) {
	switch {
	case errors.Is(err, service.ErrEvidenceRequestIDRequired),
		errors.Is(err, service.ErrEvidenceSessionIDRequired),
		errors.Is(err, service.ErrEvidenceInvalidRefusal):
		c.Error(apperrors.NewBadRequestError(err.Error()))
	case errors.Is(err, service.ErrEvidenceTenantMissing):
		c.Error(apperrors.NewUnauthorizedError(err.Error()))
	case errors.Is(err, service.ErrEvidencePackNotFound):
		c.Error(apperrors.NewNotFoundError(err.Error()))
	default:
		logger.ErrorWithFields(ctx, err, map[string]interface{}{"handler": "evidence_pack"})
		c.Error(apperrors.NewInternalServerError("evidence pack operation failed").WithDetails(err.Error()))
	}
}
