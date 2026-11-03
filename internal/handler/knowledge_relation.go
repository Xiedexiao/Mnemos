package handler

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// KnowledgeRelationHandler exposes knowledge-to-knowledge relations
// (KnowAxis V3.0 §4.5).
type KnowledgeRelationHandler struct {
	relations interfaces.KnowledgeRelationService
}

// NewKnowledgeRelationHandler constructs the handler.
func NewKnowledgeRelationHandler(
	relations interfaces.KnowledgeRelationService,
) *KnowledgeRelationHandler {
	return &KnowledgeRelationHandler{relations: relations}
}

// relationLinkRequest is the body of POST /knowledge/:id/relations.
type relationLinkRequest struct {
	ToKnowledgeID string `json:"to_knowledge_id"`
	// RelationType is one of based_on/replaces/supplements/conflicts/
	// prerequisite/responsible/related.
	RelationType string `json:"relation_type"`
	Note         string `json:"note"`
}

// LinkKnowledgeRelation godoc
// @Summary      建立知识关联
// @Description  在两篇知识之间建立有类型的关系（KnowAxis V3.0 §4.5：依据/替代/补充/冲突/前置/责任/相关）。关联双向存储，反向页面自动出现"谁引用了当前页面"；两端必须同租户。
// @Tags         知识关联
// @Accept       json
// @Produce      json
// @Param        id       path  string               true  "知识ID"
// @Param        request  body  relationLinkRequest  true  "关联"
// @Success      200  {object}  map[string]interface{}  "{success, data: KnowledgeRelation}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge/{id}/relations [post]
func (h *KnowledgeRelationHandler) LinkKnowledgeRelation(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req relationLinkRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	relation, err := h.relations.Link(ctx, tenantID, c.Param("id"),
		strings.TrimSpace(req.ToKnowledgeID),
		types.KnowledgeRelationType(strings.TrimSpace(req.RelationType)),
		req.Note, caller.UserID, types.RelationOriginManual)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": relation})
}

// ListKnowledgeRelations godoc
// @Summary      列出知识关联
// @Description  返回当前文档的关联（含反向"被引用"），按关系类型与对端排序。AI 推荐的关联标注 origin=ai_suggested。
// @Tags         知识关联
// @Produce      json
// @Param        id  path  string  true  "知识ID"
// @Success      200  {object}  map[string]interface{}  "{success, data: [KnowledgeRelationView]}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge/{id}/relations [get]
func (h *KnowledgeRelationHandler) ListKnowledgeRelations(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	views, err := h.relations.List(ctx, tenantID, c.Param("id"))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": views})
}

// UnlinkKnowledgeRelation godoc
// @Summary      删除知识关联
// @Description  删除一条有类型的关联（双向同时移除）。
// @Tags         知识关联
// @Produce      json
// @Param        id              path   string  true  "知识ID"
// @Param        to_knowledge_id query  string  true  "对端知识ID"
// @Param        relation_type   query  string  false "关系类型（缺省 related）"
// @Success      200  {object}  map[string]interface{}  "{success, removed}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge/{id}/relations [delete]
func (h *KnowledgeRelationHandler) UnlinkKnowledgeRelation(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	removed, err := h.relations.Unlink(ctx, tenantID, c.Param("id"),
		strings.TrimSpace(c.Query("to_knowledge_id")),
		types.KnowledgeRelationType(strings.TrimSpace(c.Query("relation_type"))))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "removed": removed})
}

// GetRelationDeleteImpact godoc
// @Summary      删除前的影响检查
// @Description  返回删除该文档会切断多少条关联（§4.5 删除/下架知识时检查引用关系）。供删除确认弹窗提示。
// @Tags         知识关联
// @Produce      json
// @Param        id  path  string  true  "知识ID"
// @Success      200  {object}  map[string]interface{}  "{success, data: {link_count}}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge/{id}/relations/delete-impact [get]
func (h *KnowledgeRelationHandler) GetRelationDeleteImpact(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	count, err := h.relations.DeleteImpact(ctx, tenantID, c.Param("id"))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": gin.H{"link_count": count}})
}
