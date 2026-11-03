package handler

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// ChangeSignalHandler exposes the §7.3 change-signal catalogue.
type ChangeSignalHandler struct {
	signals interfaces.ChangeSignalService
}

// NewChangeSignalHandler constructs the handler.
func NewChangeSignalHandler(signals interfaces.ChangeSignalService) *ChangeSignalHandler {
	return &ChangeSignalHandler{signals: signals}
}

// collectSignalRequest is the body of POST /change-signals.
type collectSignalRequest struct {
	SignalType      string     `json:"signal_type"`
	SourceID        string     `json:"source_id"`
	KnowledgeID     string     `json:"knowledge_id"`
	KnowledgeBaseID string     `json:"knowledge_base_id"`
	Summary         string     `json:"summary"`
	Confidence      string     `json:"confidence"`
	Payload         types.JSON `json:"payload"`
}

// CollectChangeSignal godoc
// @Summary      上报变更信号
// @Description  采集一条更新信号（KnowAxis V3.0 §7.3 七类：内部事件/源系统状态/外部订阅/依赖链/用户反馈/冲突/连接器健康）。同一来源的同类信号在待处理期间自动去重，重复上报不会堆积。
// @Tags         变更信号
// @Accept       json
// @Produce      json
// @Param        request  body  collectSignalRequest  true  "信号"
// @Success      200  {object}  map[string]interface{}  "{success, data: ChangeSignal}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /change-signals [post]
func (h *ChangeSignalHandler) CollectChangeSignal(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req collectSignalRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	signal, err := h.signals.Collect(ctx, &types.ChangeSignal{
		TenantID:        tenantID,
		SignalType:      strings.TrimSpace(req.SignalType),
		SourceID:        strings.TrimSpace(req.SourceID),
		KnowledgeID:     strings.TrimSpace(req.KnowledgeID),
		KnowledgeBaseID: strings.TrimSpace(req.KnowledgeBaseID),
		Summary:         req.Summary,
		Confidence:      req.Confidence,
		Payload:         req.Payload,
	})
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": signal})
}

// ListChangeSignals godoc
// @Summary      变更信号队列
// @Description  按状态与类型列出信号，默认 pending（§7.3）。
// @Tags         变更信号
// @Produce      json
// @Param        status       query  string  false  "pending|converted|dismissed"
// @Param        signal_type  query  string  false  "七类信号之一"
// @Param        limit        query  int     false  "返回条数（默认 100）"
// @Success      200  {object}  map[string]interface{}  "{success, data: [ChangeSignal]}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /change-signals [get]
func (h *ChangeSignalHandler) ListChangeSignals(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "100"))
	if err != nil || limit <= 0 || limit > 200 {
		limit = 100
	}
	status := types.ChangeSignalStatus(c.Query("status"))
	if status == "" {
		status = types.ChangeSignalPending
	}
	rows, listErr := h.signals.List(ctx, tenantID, status,
		types.ChangeSignalType(c.Query("signal_type")), limit)
	if listErr != nil {
		c.Error(listErr)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": rows})
}

// ConvertChangeSignal godoc
// @Summary      信号转变更单
// @Description  把信号转成变更单草稿（§7.3 默认动作）。风险级与变更类型由信号类型决定——外部法规更新默认高风险、源系统作废默认下架，避免把高风险信号误标为低风险。
// @Tags         变更信号
// @Produce      json
// @Param        id  path  string  true  "信号ID"
// @Success      200  {object}  map[string]interface{}  "{success, data: ChangeSignal}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /change-signals/{id}/convert [post]
func (h *ChangeSignalHandler) ConvertChangeSignal(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	caller := types.CallerFromContext(ctx)
	signal, err := h.signals.Convert(ctx, tenantID, c.Param("id"), caller.UserID)
	if err != nil {
		writeChangeSignalError(c, err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": signal})
}

// DismissChangeSignal godoc
// @Summary      驳回信号
// @Description  判定信号为误报并移出队列（§7.3 可驳回，避免噪声长期占用待办）。
// @Tags         变更信号
// @Produce      json
// @Param        id  path  string  true  "信号ID"
// @Success      200  {object}  map[string]interface{}  "{success, data: ChangeSignal}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /change-signals/{id}/dismiss [post]
func (h *ChangeSignalHandler) DismissChangeSignal(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	caller := types.CallerFromContext(ctx)
	signal, err := h.signals.Dismiss(ctx, tenantID, c.Param("id"), caller.UserID)
	if err != nil {
		writeChangeSignalError(c, err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": signal})
}

// writeChangeSignalError maps signal sentinels onto HTTP semantics.
func writeChangeSignalError(c *gin.Context, err error) {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "already handled"):
		c.Error(errors.NewConflictError(msg))
	case strings.Contains(msg, "not found"):
		c.Error(errors.NewNotFoundError(msg))
	default:
		c.Error(err)
	}
}
