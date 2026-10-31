package handler

import (
	"encoding/json"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

// ExportControlHandler serves the export request endpoints (KnowAxis V3.0
// §2.2 export 动作 v1)。创建/列表/执行是 Viewer+（任何成员都能申请导出、
// 在批准后执行导出），裁决是 Admin+（四眼：审批人 ≠ 申请人，由服务层强制
// ——路由守卫只是第一道门）。
type ExportControlHandler struct {
	exportService interfaces.ExportControlService
}

// NewExportControlHandler constructs the export control handler.
func NewExportControlHandler(exportService interfaces.ExportControlService) *ExportControlHandler {
	return &ExportControlHandler{exportService: exportService}
}

// createExportRequestRequest is the body of POST /export-requests.
// watermark_text 可省略：服务层默认生成"用户名-时间-随机4位"。
type createExportRequestRequest struct {
	// Scope: knowledge | evidence_pack | audit.
	Scope string `json:"scope" binding:"required"`
	// ScopeFilter 是导出范围过滤（kb_ids / 条数 / 时间窗），结构按 scope
	// 各异，v1 不做严格 schema 校验。
	ScopeFilter map[string]interface{} `json:"scope_filter"`
	// MaxClassification 本次导出允许的最高密级；省略按 internal。
	MaxClassification string `json:"max_classification"`
	// WatermarkText 自定义水印；省略由服务端生成。
	WatermarkText string `json:"watermark_text"`
}

// decideExportRequestRequest is the body of POST /export-requests/:id/decide.
type decideExportRequestRequest struct {
	// Decision: approve | reject.
	Decision string `json:"decision" binding:"required"`
	// Note 审批备注（reject 时必填，服务层校验）。
	Note string `json:"note"`
}

// CreateRequest godoc
// @Summary      创建导出申请
// @Description  申请一次导出（scope: knowledge|evidence_pack|audit，可带范围过滤与最高密级）。审批策略 v1：secret/confidential 或 audit scope 保持 pending 等人工审批（Admin 且 ≠申请人，四眼）；public/internal 且非审计导出自动 approve。watermark_text 省略时由服务端生成"用户名-时间-随机4位"。
// @Tags         导出管控
// @Accept       json
// @Produce      json
// @Param        request body createExportRequestRequest true "导出申请"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {object}  errors.AppError
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /export-requests [post]
func (h *ExportControlHandler) CreateRequest(c *gin.Context) {
	ctx := c.Request.Context()
	var req createExportRequestRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(apperrors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	saved, err := h.exportService.Create(ctx, &types.ExportRequest{
		TenantID:          caller.TenantID,
		RequestedBy:       caller.UserID,
		Scope:             req.Scope,
		ScopeFilter:       exportScopeFilterPayload(req.ScopeFilter),
		MaxClassification: req.MaxClassification,
		WatermarkText:     req.WatermarkText,
	})
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": saved})
}

// ListRequests godoc
// @Summary      列出导出申请
// @Description  按状态过滤列出本租户的导出申请（最新在前）；status 省略列出全部。
// @Tags         导出管控
// @Produce      json
// @Param        status  query  string  false  "状态过滤（pending|approved|rejected|fulfilled）"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /export-requests [get]
func (h *ExportControlHandler) ListRequests(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	reqs, err := h.exportService.List(ctx, caller.TenantID, c.Query("status"))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": reqs})
}

// Decide godoc
// @Summary      审批导出申请
// @Description  对 pending 申请做 approve/reject 裁决（Admin+；四眼：审批人不得是申请人，服务层强制）。reject 必须带 note。
// @Tags         导出管控
// @Accept       json
// @Produce      json
// @Param        id      path  string                       true  "申请ID"
// @Param        request body  decideExportRequestRequest   true  "裁决"
// @Success      200  {object}  map[string]interface{}
// @Failure      403  {object}  errors.AppError  "非 Admin 或四眼违规"
// @Failure      409  {object}  errors.AppError  "申请不在 pending 状态"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /export-requests/{id}/decide [post]
func (h *ExportControlHandler) Decide(c *gin.Context) {
	ctx := c.Request.Context()
	var req decideExportRequestRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(apperrors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	saved, err := h.exportService.Decide(ctx, caller.TenantID, c.Param("id"), &types.ExportDecisionInput{
		Decision:     types.ExportDecision(req.Decision),
		ApproverID:   caller.UserID,
		ApproverRole: caller.Role,
		Note:         req.Note,
	})
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": saved})
}

// Fulfill godoc
// @Summary      执行导出
// @Description  将 approved 申请置为 fulfilled 并返回水印文本（前端渲染在导出物上）；pending 须先审批，rejected/fulfilled 是终态。
// @Tags         导出管控
// @Produce      json
// @Param        id  path  string  true  "申请ID"
// @Success      200  {object}  map[string]interface{}
// @Failure      409  {object}  errors.AppError  "申请未批准"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /export-requests/{id}/fulfill [post]
func (h *ExportControlHandler) Fulfill(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	saved, err := h.exportService.Fulfill(ctx, caller.TenantID, c.Param("id"), caller.UserID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": saved})
}

// GetRequest godoc
// @Summary      查询导出申请
// @Description  按 ID 返回本租户的一条导出申请（含审批痕迹与水印文本）。
// @Tags         导出管控
// @Produce      json
// @Param        id  path  string  true  "申请ID"
// @Success      200  {object}  map[string]interface{}
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /export-requests/{id} [get]
func (h *ExportControlHandler) GetRequest(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	req, err := h.exportService.Get(ctx, caller.TenantID, c.Param("id"))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": req})
}

// exportScopeFilterPayload converts the request's filter map into the JSONB
// payload, falling back to an empty object so the column always holds a JSON
// object. Kept local to this vertical (exportXxxJSON naming convention, same
// keep-verticals-independent rationale as the glossary handler).
func exportScopeFilterPayload(filter map[string]interface{}) types.JSON {
	if len(filter) == 0 {
		return types.JSON([]byte("{}"))
	}
	data, err := json.Marshal(filter)
	if err != nil {
		return types.JSON([]byte("{}"))
	}
	return types.JSON(data)
}
