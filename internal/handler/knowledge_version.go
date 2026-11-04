package handler

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// KnowledgeVersionHandler exposes §7.4 version diff, as-of and rollback.
type KnowledgeVersionHandler struct {
	versions interfaces.KnowledgeVersionQueryService
}

// NewKnowledgeVersionHandler constructs the handler.
func NewKnowledgeVersionHandler(
	versions interfaces.KnowledgeVersionQueryService,
) *KnowledgeVersionHandler {
	return &KnowledgeVersionHandler{versions: versions}
}

// rollbackVersionRequest is the rollback body.
type rollbackVersionRequest struct {
	TargetVersion string `json:"target_version"`
	ChangeNote    string `json:"change_note"`
}

// DiffKnowledgeVersions godoc
// @Summary      版本差异
// @Description  按段落对比两个版本（KnowAxis V3.0 §7.4）。左右可用语义化版本（1.2.0）、递增号（3）或 current；left 缺省为 right 的前一版。
// @Tags         版本
// @Produce      json
// @Param        id     path   string  true   "知识ID"
// @Param        left   query  string  false  "左侧版本（缺省=前一版）"
// @Param        right  query  string  false  "右侧版本（缺省 current）"
// @Success      200  {object}  map[string]interface{}  "{success, data: VersionDiff}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge/{id}/versions/diff [get]
func (h *KnowledgeVersionHandler) DiffKnowledgeVersions(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	diff, err := h.versions.Diff(ctx, tenantID, c.Param("id"),
		strings.TrimSpace(c.Query("left")), strings.TrimSpace(c.Query("right")))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": diff})
}

// AsOfKnowledgeVersion godoc
// @Summary      历史时点版本
// @Description  回答"在某一时点生效的是哪一版"（KnowAxis V3.0 §7.4 as-of 查询）。
// @Tags         版本
// @Produce      json
// @Param        id  path   string  true  "知识ID"
// @Param        at  query  string  true  "时点（RFC3339）"
// @Success      200  {object}  map[string]interface{}  "{success, data: AsOfVersion}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge/{id}/versions/as-of [get]
func (h *KnowledgeVersionHandler) AsOfKnowledgeVersion(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	raw := strings.TrimSpace(c.Query("at"))
	if raw == "" {
		c.Error(errors.NewBadRequestError("at is required (RFC3339)"))
		return
	}
	at, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		c.Error(errors.NewBadRequestError("at must be RFC3339"))
		return
	}
	result, asOfErr := h.versions.AsOf(ctx, tenantID, c.Param("id"), at)
	if asOfErr != nil {
		c.Error(asOfErr)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": result})
}

// RollbackKnowledgeVersion godoc
// @Summary      回滚到历史版本
// @Description  以历史版本的内容**新建**一版（KnowAxis V3.0 §7.4：历史永久可查，不做删除式回退）。必须填写原因，回滚是一次会撤销已批准内容的动作。
// @Tags         版本
// @Accept       json
// @Produce      json
// @Param        id       path  string                 true  "知识ID"
// @Param        request  body  rollbackVersionRequest  true  "回滚"
// @Success      200  {object}  map[string]interface{}  "{success, data: VersionRollbackResult}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge/{id}/versions/rollback [post]
func (h *KnowledgeVersionHandler) RollbackKnowledgeVersion(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req rollbackVersionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	result, err := h.versions.Rollback(ctx, tenantID, types.VersionRollbackInput{
		KnowledgeID:   c.Param("id"),
		TargetVersion: req.TargetVersion,
		ChangeNote:    req.ChangeNote,
	})
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": result})
}
