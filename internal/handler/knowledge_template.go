package handler

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// KnowledgeTemplateHandler exposes the template center and field governance
// (KnowAxis V3.0 §3.2 自定义字段 / §4.7 模板与字段治理).
type KnowledgeTemplateHandler struct {
	templates interfaces.KnowledgeTemplateService
}

// NewKnowledgeTemplateHandler constructs the handler.
func NewKnowledgeTemplateHandler(
	templates interfaces.KnowledgeTemplateService,
) *KnowledgeTemplateHandler {
	return &KnowledgeTemplateHandler{templates: templates}
}

// templateSaveRequest is the body of POST /knowledge-templates.
type templateSaveRequest struct {
	TemplateKey   string                 `json:"template_key"`
	Name          string                 `json:"name"`
	Description   string                 `json:"description"`
	KnowledgeType string                 `json:"knowledge_type"`
	Fields        []templateFieldRequest `json:"fields"`
	// KBID sizes the field-migration tasks (affected entry count).
	KBID string `json:"kb_id"`
}

// templateFieldRequest is one field definition in a save request.
type templateFieldRequest struct {
	FieldKey     string     `json:"field_key"`
	Label        string     `json:"label"`
	FieldType    string     `json:"field_type"`
	Required     bool       `json:"required"`
	DefaultValue string     `json:"default_value"`
	Rules        types.JSON `json:"rules"`
	EditableRole string     `json:"editable_role"`
	RecallField  bool       `json:"recall_field"`
	SortOrder    int        `json:"sort_order"`
}

// SaveKnowledgeTemplate godoc
// @Summary      保存知识模板（新版本）
// @Description  创建或更新一个知识模板。每次保存都生成新版本，旧版本保留以解释历史知识；字段的新增/删除/改类型会生成迁移任务（§4.7）。
// @Tags         模板与字段
// @Accept       json
// @Produce      json
// @Param        request  body  templateSaveRequest  true  "模板定义"
// @Success      200  {object}  map[string]interface{}  "{success, data: KnowledgeTemplate}"
// @Failure      400  {object}  errors.AppError
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge-templates [post]
func (h *KnowledgeTemplateHandler) SaveKnowledgeTemplate(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req templateSaveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	fields := make([]*types.KnowledgeField, 0, len(req.Fields))
	for _, field := range req.Fields {
		fields = append(fields, &types.KnowledgeField{
			FieldKey:     strings.TrimSpace(field.FieldKey),
			Label:        strings.TrimSpace(field.Label),
			FieldType:    field.FieldType,
			Required:     field.Required,
			DefaultValue: field.DefaultValue,
			Rules:        field.Rules,
			EditableRole: field.EditableRole,
			RecallField:  field.RecallField,
			SortOrder:    field.SortOrder,
		})
	}
	template := &types.KnowledgeTemplate{
		TemplateKey:   req.TemplateKey,
		Name:          req.Name,
		Description:   req.Description,
		KnowledgeType: req.KnowledgeType,
	}
	caller := types.CallerFromContext(ctx)
	saved, err := h.templates.Save(ctx, tenantID, template, fields, caller.UserID, req.KBID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": saved})
}

// ListKnowledgeTemplates godoc
// @Summary      列出知识模板
// @Description  返回租户内的生效模板及其字段定义（可直接渲染录入表单）。
// @Tags         模板与字段
// @Produce      json
// @Success      200  {object}  map[string]interface{}  "{success, data: [KnowledgeTemplate]}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge-templates [get]
func (h *KnowledgeTemplateHandler) ListKnowledgeTemplates(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	templates, err := h.templates.List(ctx, tenantID, 0)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": templates})
}

// GetKnowledgeTemplate godoc
// @Summary      读取知识模板
// @Description  按 template_key 读取生效版本；带 ?version=N 时读取指定历史版本（解释历史知识时用）。
// @Tags         模板与字段
// @Produce      json
// @Param        key      path  string  true  "模板键"
// @Param        version  query int     false "指定版本（缺省为生效版本）"
// @Success      200  {object}  map[string]interface{}  "{success, data: KnowledgeTemplate}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge-templates/{key} [get]
func (h *KnowledgeTemplateHandler) GetKnowledgeTemplate(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	key := c.Param("key")
	if versionRaw := strings.TrimSpace(c.Query("version")); versionRaw != "" {
		version, err := strconv.Atoi(versionRaw)
		if err != nil || version <= 0 {
			c.Error(errors.NewBadRequestError("version must be a positive integer"))
			return
		}
		// 历史版本按 id 读：调用方先按 key 定位，再按版本号匹配。
		template, err := h.templates.Get(ctx, tenantID, versionRaw)
		if err != nil {
			c.Error(err)
			return
		}
		c.JSON(200, gin.H{"success": true, "data": template})
		return
	}
	template, err := h.templates.GetByKey(ctx, tenantID, key)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": template})
}

// validateFieldsRequest is the body of POST /knowledge-templates/{key}/validate.
type validateFieldsRequest struct {
	Values map[string]interface{} `json:"values"`
}

// ValidateKnowledgeFields godoc
// @Summary      校验字段值
// @Description  按模板的字段定义校验一组值：补齐默认值后执行必填/类型/范围/枚举/正则校验，返回补齐后的值与问题清单（KnowAxis V3.0 §3.2）。
// @Tags         模板与字段
// @Accept       json
// @Produce      json
// @Param        key      path  string               true  "模板键"
// @Param        request  body  validateFieldsRequest true "字段值"
// @Success      200  {object}  map[string]interface{}  "{success, data: {values, problems}}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge-templates/{key}/validate [post]
func (h *KnowledgeTemplateHandler) ValidateKnowledgeFields(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req validateFieldsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	values, problems, err := h.templates.Validate(ctx, tenantID, c.Param("key"), req.Values)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"values":   values,
			"problems": problems,
		},
	})
}

// ListFieldMigrations godoc
// @Summary      字段迁移任务
// @Description  返回字段结构变更（新增/删除/改类型）产生的迁移任务（§4.7）。
// @Tags         模板与字段
// @Produce      json
// @Param        status  query  string  false  "pending|done|waived"
// @Success      200  {object}  map[string]interface{}  "{success, data: [KnowledgeFieldMigration]}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge-field-migrations [get]
func (h *KnowledgeTemplateHandler) ListFieldMigrations(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	rows, err := h.templates.ListMigrations(ctx, tenantID,
		types.KnowledgeFieldMigrationStatus(c.Query("status")), 0)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": rows})
}

// resolveMigrationRequest is the body of PATCH /knowledge-field-migrations/:id.
type resolveMigrationRequest struct {
	Status string `json:"status"`
	Note   string `json:"note"`
}

// ResolveFieldMigration godoc
// @Summary      处置字段迁移任务
// @Description  把字段迁移任务标记为 done（已回填）或 waived（确认无需回填，记录理由）。
// @Tags         模板与字段
// @Accept       json
// @Produce      json
// @Param        id       path  string                  true  "任务ID"
// @Param        request  body  resolveMigrationRequest true  "处置"
// @Success      200  {object}  map[string]interface{}  "{success, data: KnowledgeFieldMigration}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge-field-migrations/{id} [patch]
func (h *KnowledgeTemplateHandler) ResolveFieldMigration(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req resolveMigrationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	row, err := h.templates.ResolveMigration(ctx, tenantID, c.Param("id"),
		types.KnowledgeFieldMigrationStatus(req.Status), req.Note)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": row})
}
