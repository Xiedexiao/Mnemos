package handler

import (
	"github.com/gin-gonic/gin"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// PromptRolloutHandler exposes the §16.5 canary / rollback surface.
type PromptRolloutHandler struct {
	rollout interfaces.PromptRolloutService
}

// NewPromptRolloutHandler constructs the handler.
func NewPromptRolloutHandler(rollout interfaces.PromptRolloutService) *PromptRolloutHandler {
	return &PromptRolloutHandler{rollout: rollout}
}

// GetPromptRollout godoc
// @Summary      提示词灰度配置
// @Description  返回某个提示词模板当前的活跃版本指针（稳定版、灰度版与比例）（KnowAxis V3.0 §16.5）。未配置时返回零值——此时行为与改动前一致：取最新版本。
// @Tags         模型与提示词
// @Produce      json
// @Param        template_key  path  string  true  "模板键"
// @Success      200  {object}  map[string]interface{}  "{success, data: PromptVersionPointer}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /prompt-rollout/{template_key} [get]
func (h *PromptRolloutHandler) GetPromptRollout(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	pointer, err := h.rollout.Get(ctx, tenantID, c.Param("template_key"))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": pointer})
}

// ConfigurePromptRollout godoc
// @Summary      配置提示词灰度
// @Description  设置稳定版本与可选的灰度版本及比例（KnowAxis V3.0 §16.5）。两个版本都必须真实存在——指向不存在的版本会让取数静默回退到最新，而运营以为配置了灰度。灰度按 (模板键, 主体键) 哈希抽样，同一会话在灰度期内恒看到同一版。
// @Tags         模型与提示词
// @Accept       json
// @Produce      json
// @Param        template_key  path  string                       true  "模板键"
// @Param        request       body  types.PromptVersionPointer   true  "{stable_version, canary_version, canary_percent}"
// @Success      200  {object}  map[string]interface{}  "{success, data: PromptVersionPointer}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /prompt-rollout/{template_key} [put]
func (h *PromptRolloutHandler) ConfigurePromptRollout(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var request types.PromptVersionPointer
	if err := c.ShouldBindJSON(&request); err != nil {
		c.Error(apperrors.NewBadRequestError(err.Error()))
		return
	}
	// 模板键以路径为准：body 里再带一个会让"改的是哪个模板"出现两个来源。
	request.TemplateKey = c.Param("template_key")
	caller := types.CallerFromContext(ctx)
	pointer, err := h.rollout.Configure(ctx, tenantID, caller.UserID, request)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": pointer})
}

// RollbackPrompt godoc
// @Summary      回滚提示词版本
// @Description  把稳定版本指回一个已存在的历史版本（KnowAxis V3.0 §16.5）。回滚是改指针，不删版本、也不复制旧内容成新版本——后者会掩盖"曾经上线过一个坏版本"这个事实。回滚同时清空灰度：把流量继续分给刚被判定有问题的版本是自相矛盾的。note 必填。
// @Tags         模型与提示词
// @Accept       json
// @Produce      json
// @Param        template_key  path  string                        true  "模板键"
// @Param        request       body  types.PromptRollbackRequest   true  "{to_version, note}"
// @Success      200  {object}  map[string]interface{}  "{success, data: PromptVersionPointer}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /prompt-rollout/{template_key}/rollback [post]
func (h *PromptRolloutHandler) RollbackPrompt(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var request types.PromptRollbackRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.Error(apperrors.NewBadRequestError(err.Error()))
		return
	}
	request.TemplateKey = c.Param("template_key")
	caller := types.CallerFromContext(ctx)
	pointer, err := h.rollout.Rollback(ctx, tenantID, caller.UserID, request)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": pointer})
}

// SelectPromptVersion godoc
// @Summary      解析某主体应看到的提示词版本
// @Description  按 (模板键, 主体键) 解析灰度结果（KnowAxis V3.0 §16.5）。同一主体在灰度期内恒得到同一版本——哈希是纯函数，不依赖进程状态。
// @Tags         模型与提示词
// @Produce      json
// @Param        template_key  path   string  true   "模板键"
// @Param        subject_key   query  string  false  "主体键（会话或用户 ID）"
// @Success      200  {object}  map[string]interface{}  "{success, data: PromptVersionSelection}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /prompt-rollout/{template_key}/select [get]
func (h *PromptRolloutHandler) SelectPromptVersion(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	selection, err := h.rollout.Select(
		ctx, tenantID, c.Param("template_key"), c.Query("subject_key"))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": selection})
}
