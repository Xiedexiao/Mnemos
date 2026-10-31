package handler

import (
	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

// OntologyHandler exposes the ontology schema governance endpoints
// (KnowAxis V3.0 §8): schema CRUD + publish, and the type/property/relation
// sub-resources of draft schemas. Tenant scoping comes from the caller; the
// service enforces draft-only mutation and writes the ontology.* audits.
type OntologyHandler struct {
	service interfaces.OntologyService
}

// NewOntologyHandler constructs the handler.
func NewOntologyHandler(service interfaces.OntologyService) *OntologyHandler {
	return &OntologyHandler{service: service}
}

// ontologySchemaRequest is the body of POST /ontology/schemas.
type ontologySchemaRequest struct {
	Name        string `json:"name" binding:"required"`
	Description string `json:"description"`
}

// ontologySchemaUpdateRequest is the body of PUT /ontology/schemas/:id.
type ontologySchemaUpdateRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

// ontologyTypeRequest is the body of POST /ontology/schemas/:id/types.
type ontologyTypeRequest struct {
	Name        string     `json:"name" binding:"required"`
	DisplayName string     `json:"display_name"`
	Description string     `json:"description"`
	Aliases     types.JSON `json:"aliases"`
	Config      types.JSON `json:"config"`
}

// ontologyTypeUpdateRequest is the body of PUT /ontology/types/:type_id.
type ontologyTypeUpdateRequest struct {
	DisplayName string     `json:"display_name"`
	Description string     `json:"description"`
	Aliases     types.JSON `json:"aliases"`
	Config      types.JSON `json:"config"`
}

// ontologyPropertyRequest is the body of POST /ontology/types/:type_id/properties.
type ontologyPropertyRequest struct {
	Name     string     `json:"name" binding:"required"`
	DataType string     `json:"data_type" binding:"required"`
	Required bool       `json:"required"`
	Config   types.JSON `json:"config"`
}

// ontologyPropertyUpdateRequest is the body of PUT /ontology/properties/:property_id.
type ontologyPropertyUpdateRequest struct {
	DataType string     `json:"data_type" binding:"required"`
	Required bool       `json:"required"`
	Config   types.JSON `json:"config"`
}

// ontologyRelationRequest is the body of POST /ontology/schemas/:id/relations.
type ontologyRelationRequest struct {
	Name         string `json:"name" binding:"required"`
	SourceTypeID string `json:"source_type_id" binding:"required"`
	TargetTypeID string `json:"target_type_id" binding:"required"`
	Cardinality  string `json:"cardinality"`
	Description  string `json:"description"`
}

// ontologyRelationUpdateRequest is the body of PUT /ontology/relations/:relation_id.
type ontologyRelationUpdateRequest struct {
	SourceTypeID string `json:"source_type_id" binding:"required"`
	TargetTypeID string `json:"target_type_id" binding:"required"`
	Cardinality  string `json:"cardinality"`
	Description  string `json:"description"`
}

// CreateOntologySchema godoc
// @Summary      创建本体 Schema（草稿）
// @Description  新建一个 draft 状态的本体 Schema，version=1；名称在租户内唯一。
// @Tags         本体Schema
// @Accept       json
// @Produce      json
// @Param        request body ontologySchemaRequest true "Schema"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {object}  errors.AppError
// @Security     Bearer
// @Router       /ontology/schemas [post]
func (h *OntologyHandler) CreateOntologySchema(c *gin.Context) {
	ctx := c.Request.Context()
	var req ontologySchemaRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	schema, err := h.service.CreateSchema(ctx, &types.OntologySchema{
		TenantID:    caller.TenantID,
		Name:        req.Name,
		Description: req.Description,
		CreatedBy:   caller.UserID,
	}, caller.UserID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": schema})
}

// ListOntologySchemas godoc
// @Summary      本体 Schema 列表
// @Description  返回当前租户的全部本体 Schema（不含聚合结构）。
// @Tags         本体Schema
// @Produce      json
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Router       /ontology/schemas [get]
func (h *OntologyHandler) ListOntologySchemas(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	schemas, err := h.service.ListSchemas(ctx, caller.TenantID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": schemas})
}

// GetOntologySchema godoc
// @Summary      本体 Schema 详情（聚合）
// @Description  返回 Schema 及其类型（含各自属性）与关系词汇表。
// @Tags         本体Schema
// @Produce      json
// @Param        id  path  string  true  "SchemaID"
// @Success      200  {object}  map[string]interface{}
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Router       /ontology/schemas/{id} [get]
func (h *OntologyHandler) GetOntologySchema(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	detail, err := h.service.GetSchema(ctx, caller.TenantID, c.Param("id"))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": detail})
}

// UpdateOntologySchema godoc
// @Summary      更新本体 Schema（仅草稿）
// @Description  修改 draft Schema 的名称/描述；已发布历史不可变。
// @Tags         本体Schema
// @Accept       json
// @Produce      json
// @Param        id      path  string                       true  "SchemaID"
// @Param        request body  ontologySchemaUpdateRequest  true  "更新"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Router       /ontology/schemas/{id} [put]
func (h *OntologyHandler) UpdateOntologySchema(c *gin.Context) {
	ctx := c.Request.Context()
	var req ontologySchemaUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	schema, err := h.service.UpdateSchema(ctx, caller.TenantID, c.Param("id"),
		&types.OntologySchemaUpdate{Name: req.Name, Description: req.Description}, caller.UserID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": schema})
}

// DeleteOntologySchema godoc
// @Summary      删除本体 Schema（仅草稿且无类型）
// @Description  删除空草稿 Schema 及其残留子资源；已发布/已归档不可删。
// @Tags         本体Schema
// @Produce      json
// @Param        id  path  string  true  "SchemaID"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Router       /ontology/schemas/{id} [delete]
func (h *OntologyHandler) DeleteOntologySchema(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	if err := h.service.DeleteSchema(ctx, caller.TenantID, c.Param("id"), caller.UserID); err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true})
}

// PublishOntologySchema godoc
// @Summary      发布本体 Schema
// @Description  校验（≥1 类型、关系两端类型存在）后将 draft 流转为 published，版本号 +1。
// @Tags         本体Schema
// @Produce      json
// @Param        id  path  string  true  "SchemaID"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Router       /ontology/schemas/{id}/publish [post]
func (h *OntologyHandler) PublishOntologySchema(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	schema, err := h.service.PublishSchema(ctx, caller.TenantID, c.Param("id"), caller.UserID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": schema})
}

// AddOntologyType godoc
// @Summary      新增本体类型
// @Description  向 draft Schema 添加一个类型（Device/SOP/ErrorCode 等）。
// @Tags         本体Schema
// @Accept       json
// @Produce      json
// @Param        id      path  string                true  "SchemaID"
// @Param        request body  ontologyTypeRequest   true  "类型"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Router       /ontology/schemas/{id}/types [post]
func (h *OntologyHandler) AddOntologyType(c *gin.Context) {
	ctx := c.Request.Context()
	var req ontologyTypeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	t, err := h.service.AddType(ctx, caller.TenantID, &types.OntologyType{
		SchemaID:    c.Param("id"),
		Name:        req.Name,
		DisplayName: req.DisplayName,
		Description: req.Description,
		Aliases:     req.Aliases,
		Config:      req.Config,
	}, caller.UserID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": t})
}

// UpdateOntologyType godoc
// @Summary      更新本体类型
// @Description  更新类型显示名/定义/别名/治理配置；仅 draft Schema 可改。
// @Tags         本体Schema
// @Accept       json
// @Produce      json
// @Param        type_id  path  string                      true  "类型ID"
// @Param        request  body  ontologyTypeUpdateRequest   true  "更新"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Router       /ontology/types/{type_id} [put]
func (h *OntologyHandler) UpdateOntologyType(c *gin.Context) {
	ctx := c.Request.Context()
	var req ontologyTypeUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	t, err := h.service.UpdateType(ctx, caller.TenantID, c.Param("type_id"),
		&types.OntologyTypeUpdate{
			DisplayName: req.DisplayName,
			Description: req.Description,
			Aliases:     req.Aliases,
			Config:      req.Config,
		}, caller.UserID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": t})
}

// DeleteOntologyType godoc
// @Summary      删除本体类型
// @Description  从 draft Schema 删除类型并连同其属性。
// @Tags         本体Schema
// @Produce      json
// @Param        type_id  path  string  true  "类型ID"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Router       /ontology/types/{type_id} [delete]
func (h *OntologyHandler) DeleteOntologyType(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	if err := h.service.DeleteType(ctx, caller.TenantID, c.Param("type_id"), caller.UserID); err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true})
}

// AddOntologyProperty godoc
// @Summary      新增类型属性
// @Description  向 draft Schema 的类型添加属性（string/number/boolean/date/enum/ref）。
// @Tags         本体Schema
// @Accept       json
// @Produce      json
// @Param        type_id  path  string                   true  "类型ID"
// @Param        request  body  ontologyPropertyRequest  true  "属性"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Router       /ontology/types/{type_id}/properties [post]
func (h *OntologyHandler) AddOntologyProperty(c *gin.Context) {
	ctx := c.Request.Context()
	var req ontologyPropertyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	p, err := h.service.AddProperty(ctx, caller.TenantID, &types.OntologyProperty{
		TypeID:   c.Param("type_id"),
		Name:     req.Name,
		DataType: types.OntologyPropertyDataType(req.DataType),
		Required: req.Required,
		Config:   req.Config,
	}, caller.UserID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": p})
}

// UpdateOntologyProperty godoc
// @Summary      更新类型属性
// @Description  更新属性的数据类型/必填/约束配置；仅 draft Schema 可改。
// @Tags         本体Schema
// @Accept       json
// @Produce      json
// @Param        property_id  path  string                          true  "属性ID"
// @Param        request      body  ontologyPropertyUpdateRequest   true  "更新"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Router       /ontology/properties/{property_id} [put]
func (h *OntologyHandler) UpdateOntologyProperty(c *gin.Context) {
	ctx := c.Request.Context()
	var req ontologyPropertyUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	p, err := h.service.UpdateProperty(ctx, caller.TenantID, c.Param("property_id"),
		&types.OntologyPropertyUpdate{
			DataType: req.DataType,
			Required: req.Required,
			Config:   req.Config,
		}, caller.UserID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": p})
}

// DeleteOntologyProperty godoc
// @Summary      删除类型属性
// @Description  从 draft Schema 的类型删除属性。
// @Tags         本体Schema
// @Produce      json
// @Param        property_id  path  string  true  "属性ID"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Router       /ontology/properties/{property_id} [delete]
func (h *OntologyHandler) DeleteOntologyProperty(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	if err := h.service.DeleteProperty(ctx, caller.TenantID, c.Param("property_id"), caller.UserID); err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true})
}

// AddOntologyRelation godoc
// @Summary      新增本体关系
// @Description  向 draft Schema 添加关系（belongs_to/depends_on 等），两端类型必须已定义。
// @Tags         本体Schema
// @Accept       json
// @Produce      json
// @Param        id      path  string                   true  "SchemaID"
// @Param        request body  ontologyRelationRequest  true  "关系"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Router       /ontology/schemas/{id}/relations [post]
func (h *OntologyHandler) AddOntologyRelation(c *gin.Context) {
	ctx := c.Request.Context()
	var req ontologyRelationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	rel, err := h.service.AddRelation(ctx, caller.TenantID, &types.OntologyRelation{
		SchemaID:     c.Param("id"),
		Name:         req.Name,
		SourceTypeID: req.SourceTypeID,
		TargetTypeID: req.TargetTypeID,
		Cardinality:  types.OntologyRelationCardinality(req.Cardinality),
		Description:  req.Description,
	}, caller.UserID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": rel})
}

// UpdateOntologyRelation godoc
// @Summary      更新本体关系
// @Description  更新关系两端类型/基数/描述；仅 draft Schema 可改。
// @Tags         本体Schema
// @Accept       json
// @Produce      json
// @Param        relation_id  path  string                          true  "关系ID"
// @Param        request      body  ontologyRelationUpdateRequest   true  "更新"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Router       /ontology/relations/{relation_id} [put]
func (h *OntologyHandler) UpdateOntologyRelation(c *gin.Context) {
	ctx := c.Request.Context()
	var req ontologyRelationUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	rel, err := h.service.UpdateRelation(ctx, caller.TenantID, c.Param("relation_id"),
		&types.OntologyRelationUpdate{
			SourceTypeID: req.SourceTypeID,
			TargetTypeID: req.TargetTypeID,
			Cardinality:  req.Cardinality,
			Description:  req.Description,
		}, caller.UserID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": rel})
}

// DeleteOntologyRelation godoc
// @Summary      删除本体关系
// @Description  从 draft Schema 删除关系。
// @Tags         本体Schema
// @Produce      json
// @Param        relation_id  path  string  true  "关系ID"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Router       /ontology/relations/{relation_id} [delete]
func (h *OntologyHandler) DeleteOntologyRelation(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	if err := h.service.DeleteRelation(ctx, caller.TenantID, c.Param("relation_id"), caller.UserID); err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true})
}
