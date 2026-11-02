package handler

import (
	"strconv"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

// ModelGatewayHandler serves the model gateway endpoints (KnowAxis V3.0
// §16.5): routing rules, prompt template versions and usage summary.
//
// 权限面：路由规则与用量是租户治理/成本面（Admin）；Prompt 版本读取从宽
// （Viewer+，模板内容对构建答案的人有用），写新版本是 Admin。平台级规则
// (tenant 0) 的写操作在 service 层再收一道系统管理员门槛。
type ModelGatewayHandler struct {
	modelGateway interfaces.ModelGatewayService
}

// NewModelGatewayHandler constructs the handler.
func NewModelGatewayHandler(modelGateway interfaces.ModelGatewayService) *ModelGatewayHandler {
	return &ModelGatewayHandler{modelGateway: modelGateway}
}

// createRuleRequest is the body of POST /model-gateway/routing-rules.
type createRuleRequest struct {
	// Scope: task | classification.
	Scope string `json:"scope" binding:"required"`
	// ScopeValue: task key (chat/embedding/rerank/...) or classification
	// value (public/internal/secret/confidential).
	ScopeValue string `json:"scope_value" binding:"required"`
	// ModelID is the models-table ID calls are routed to.
	ModelID string `json:"model_id" binding:"required"`
	// Enabled defaults to true when omitted.
	Enabled *bool `json:"enabled"`
	// Platform marks the rule as platform-level (tenant 0, inherited by
	// every tenant). Only system administrators may set it.
	Platform bool `json:"platform"`
}

// updateRuleRequest is the body of PUT /model-gateway/routing-rules/:id.
// scope/scope_value are immutable (改路由 = 新建规则 + 删旧规则).
type updateRuleRequest struct {
	ModelID *string `json:"model_id"`
	Enabled *bool   `json:"enabled"`
}

// savePromptVersionRequest is the body of POST
// /model-gateway/prompts/:key/versions.
type savePromptVersionRequest struct {
	Content    string `json:"content" binding:"required"`
	ChangeNote string `json:"change_note"`
}

// CreateRule godoc
// @Summary      新建模型路由规则
// @Description  将任务（chat/embedding/rerank/...）或数据密级（public/internal/secret/confidential）路由到指定模型；同租户同 scope+scope_value 唯一。platform=true 表示平台级规则（仅系统管理员）。
// @Tags         模型网关
// @Accept       json
// @Produce      json
// @Param        request body createRuleRequest true "路由规则"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {object}  errors.AppError
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /model-gateway/routing-rules [post]
func (h *ModelGatewayHandler) CreateRule(c *gin.Context) {
	ctx := c.Request.Context()
	var req createRuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	tenantID := caller.TenantID
	if req.Platform {
		if !types.IsSystemAdminFromContext(ctx) {
			c.Error(errors.NewForbiddenError("only system administrators can create platform-level routing rules"))
			return
		}
		tenantID = types.PlatformLevelTenantID
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	rule, err := h.modelGateway.CreateRule(ctx, &types.ModelRoutingRule{
		TenantID:   tenantID,
		Scope:      req.Scope,
		ScopeValue: req.ScopeValue,
		ModelID:    req.ModelID,
		Enabled:    enabled,
		CreatedBy:  caller.UserID,
	})
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": rule})
}

// UpdateRule godoc
// @Summary      更新模型路由规则
// @Description  按 ID 更新规则（model_id / enabled；scope 与 scope_value 不可改，改路由=新建+删除）。平台级规则仅系统管理员可改。
// @Tags         模型网关
// @Accept       json
// @Produce      json
// @Param        id      path  string             true  "规则ID"
// @Param        request body  updateRuleRequest  true  "更新"
// @Success      200  {object}  map[string]interface{}
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /model-gateway/routing-rules/{id} [put]
func (h *ModelGatewayHandler) UpdateRule(c *gin.Context) {
	ctx := c.Request.Context()
	var req updateRuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	rule, err := h.modelGateway.UpdateRule(ctx, caller.TenantID, c.Param("id"), &types.ModelRoutingRuleUpdate{
		ModelID: req.ModelID,
		Enabled: req.Enabled,
	}, caller.UserID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": rule})
}

// DeleteRule godoc
// @Summary      删除模型路由规则
// @Description  物理删除规则（路由是配置不是生命周期数据）。平台级规则仅系统管理员可删。
// @Tags         模型网关
// @Produce      json
// @Param        id  path  string  true  "规则ID"
// @Success      200  {object}  map[string]interface{}
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /model-gateway/routing-rules/{id} [delete]
func (h *ModelGatewayHandler) DeleteRule(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	if err := h.modelGateway.DeleteRule(ctx, caller.TenantID, c.Param("id"), caller.UserID); err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true})
}

// GetRule godoc
// @Summary      查询单条路由规则
// @Description  按 ID 返回规则（含平台级规则；tenant_id=0 表示平台级）。
// @Tags         模型网关
// @Produce      json
// @Param        id  path  string  true  "规则ID"
// @Success      200  {object}  map[string]interface{}
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /model-gateway/routing-rules/{id} [get]
func (h *ModelGatewayHandler) GetRule(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	rule, err := h.modelGateway.GetRule(ctx, caller.TenantID, c.Param("id"))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": rule})
}

// ListRules godoc
// @Summary      列出路由规则
// @Description  返回本租户规则 + 平台级规则（tenant_id=0），按租户优先排序，即完整的生效路由图景。
// @Tags         模型网关
// @Produce      json
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /model-gateway/routing-rules [get]
func (h *ModelGatewayHandler) ListRules(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	rules, err := h.modelGateway.ListRules(ctx, caller.TenantID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": rules})
}

// ListPromptVersions godoc
// @Summary      列出提示词模板版本
// @Description  按 template_key（prompt_templates 文件名去后缀，如 system_prompt/rewrite）列出全部版本，最新在前。
// @Tags         模型网关
// @Produce      json
// @Param        key  path  string  true  "模板键"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /model-gateway/prompts/{key}/versions [get]
func (h *ModelGatewayHandler) ListPromptVersions(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	versions, err := h.modelGateway.ListVersions(ctx, caller.TenantID, c.Param("key"))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": versions})
}

// GetPromptVersion godoc
// @Summary      查询提示词模板版本详情
// @Description  按 template_key + version 返回版本内容（404 表示该版本不存在）。
// @Tags         模型网关
// @Produce      json
// @Param        key      path  string  true  "模板键"
// @Param        version  path  int     true  "版本号"
// @Success      200  {object}  map[string]interface{}
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /model-gateway/prompts/{key}/versions/{version} [get]
func (h *ModelGatewayHandler) GetPromptVersion(c *gin.Context) {
	ctx := c.Request.Context()
	version, err := strconv.Atoi(c.Param("version"))
	if err != nil || version < 1 {
		c.Error(errors.NewBadRequestError("version must be a positive integer"))
		return
	}
	caller := types.CallerFromContext(ctx)
	row, err := h.modelGateway.GetVersion(ctx, caller.TenantID, c.Param("key"), version)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": row})
}

// LatestPromptVersion godoc
// @Summary      查询提示词模板最新版本
// @Description  返回该模板键的最新版本；尚无版本时 data 为 null（调用方回退 YAML 内置模板）。
// @Tags         模型网关
// @Produce      json
// @Param        key  path  string  true  "模板键"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /model-gateway/prompts/{key}/latest [get]
func (h *ModelGatewayHandler) LatestPromptVersion(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	row, err := h.modelGateway.LatestVersion(ctx, caller.TenantID, c.Param("key"))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": row})
}

// SavePromptVersion godoc
// @Summary      保存提示词模板新版本
// @Description  追加一个新版本（version=当前最大+1；内容与最新版相同则去重返回既有版本，幂等）。版本只增不改。
// @Tags         模型网关
// @Accept       json
// @Produce      json
// @Param        key      path  string                    true  "模板键"
// @Param        request body  savePromptVersionRequest  true  "版本内容"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {object}  errors.AppError
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /model-gateway/prompts/{key}/versions [post]
// ListPromptTemplates godoc
// @Summary      可覆盖的 Prompt 模板清单
// @Description  返回平台内置、可由租户在 prompt_versions 中覆盖的模板键（§16.5）。配合 GET /model-gateway/prompts/:key/latest 使用：先看哪些可覆盖，再读当前生效版本。
// @Tags         模型网关
// @Produce      json
// @Success      200  {object}  map[string]interface{}  "{success, data: [string]}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /model-gateway/prompts [get]
func (h *ModelGatewayHandler) ListPromptTemplates(c *gin.Context) {
	c.JSON(200, gin.H{"success": true, "data": types.PromptTemplateKeys()})
}

func (h *ModelGatewayHandler) SavePromptVersion(c *gin.Context) {
	ctx := c.Request.Context()
	var req savePromptVersionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	saved, err := h.modelGateway.SaveVersion(ctx, &types.PromptVersion{
		TenantID:    caller.TenantID,
		TemplateKey: c.Param("key"),
		Content:     req.Content,
		ChangeNote:  req.ChangeNote,
		CreatedBy:   caller.UserID,
	})
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": saved})
}

// UsageSummary godoc
// @Summary      查询模型用量汇总
// @Description  按天/模型返回最近 N 天（默认 30，上限 366）的 token 与调用次数累计（UTC 日）。
// @Tags         模型网关
// @Produce      json
// @Param        days  query  int  false  "天数（默认 30，上限 366）"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /model-gateway/usage [get]
func (h *ModelGatewayHandler) UsageSummary(c *gin.Context) {
	ctx := c.Request.Context()
	days := 0
	if raw := c.Query("days"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			c.Error(errors.NewBadRequestError("days must be a non-negative integer"))
			return
		}
		days = parsed
	}
	caller := types.CallerFromContext(ctx)
	summary, err := h.modelGateway.UsageSummary(ctx, caller.TenantID, days)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": gin.H{"days": summaryDays(days), "rows": summary}})
}

// summaryDays echoes the effective window length (30 default / 366 clamp) so
// the UI can label the chart without re-implementing the clamping.
func summaryDays(requested int) int {
	switch {
	case requested <= 0:
		return 30
	case requested > 366:
		return 366
	default:
		return requested
	}
}
