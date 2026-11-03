package handler

import (
	"strconv"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

// DLPHandler exposes the DLP findings inspection surface（KnowAxis V3.0 §13
// 生成内容校验 v1）。命中记录是安全巡检数据：只读、Admin-only（routes 层
// 守卫）。扫描本身不暴露 HTTP —— ScanText 是内部服务，调用方是问答输出
// 挂钩与导出管线；自定义规则的注册经 POST /dlp/patterns（Admin）。
type DLPHandler struct {
	dlpService interfaces.DLPService
}

// NewDLPHandler constructs the DLP handler.
func NewDLPHandler(dlpService interfaces.DLPService) *DLPHandler {
	return &DLPHandler{dlpService: dlpService}
}

// dlpFindingsHardLimit bounds one list call at the handler edge too (the
// repository clamps again; two layers keep the posture obvious).
const dlpFindingsHardLimit = 200

// addDLPPatternRequest is the body of POST /dlp/patterns.
type addDLPPatternRequest struct {
	// Name 规则名（可省略，服务层自动命名 custom-N）。
	Name string `json:"name"`
	// Pattern Go 正则。
	Pattern string `json:"pattern" binding:"required"`
	// FindingType 命中类别（可省略，默认 custom_pattern）。
	FindingType string `json:"finding_type"`
}

// ListFindings godoc
// @Summary      列出 DLP 命中记录
// @Description  返回本租户最近的 DLP 扫描命中记录（最新在前，limit 默认 50、硬上限 200）。matched_sample 是脱敏后的样本（只留前后 2 位），不落原文。
// @Tags         DLP
// @Produce      json
// @Param        limit  query  int  false  "条数上限（默认 50，最大 200）"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {object}  apperrors.AppError
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /dlp/findings [get]
func (h *DLPHandler) ListFindings(c *gin.Context) {
	ctx := c.Request.Context()
	limit := 0
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			c.Error(apperrors.NewBadRequestError("limit must be a non-negative integer"))
			return
		}
		limit = parsed
	}
	if limit > dlpFindingsHardLimit {
		limit = dlpFindingsHardLimit
	}
	caller := types.CallerFromContext(ctx)
	findings, err := h.dlpService.ListFindings(ctx, caller.TenantID, limit)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": findings})
}

// AddPattern godoc
// @Summary      注册自定义 DLP 规则
// @Description  为本租户注册一条自定义正则规则（Admin+）。v1 规则只存服务进程内存：不建表、重启丢失、多副本不同步；命中统一按 masked 处置并落 dlp_findings。
// @Tags         DLP
// @Accept       json
// @Produce      json
// @Param        request body addDLPPatternRequest true "自定义规则"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {object}  apperrors.AppError  "非法正则或类别"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /dlp/patterns [post]
func (h *DLPHandler) AddPattern(c *gin.Context) {
	ctx := c.Request.Context()
	var req addDLPPatternRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(apperrors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	saved, err := h.dlpService.AddCustomPattern(ctx, caller.TenantID, caller.UserID, &types.DLPCustomPattern{
		Name:        req.Name,
		Pattern:     req.Pattern,
		FindingType: types.DLPFindingType(req.FindingType),
	})
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": saved})
}
