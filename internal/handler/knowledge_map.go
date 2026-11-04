package handler

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// KnowledgeMapHandler exposes §4.6 knowledge maps and §15.5 role maps.
type KnowledgeMapHandler struct {
	maps interfaces.KnowledgeMapService
}

// NewKnowledgeMapHandler constructs the handler.
func NewKnowledgeMapHandler(maps interfaces.KnowledgeMapService) *KnowledgeMapHandler {
	return &KnowledgeMapHandler{maps: maps}
}

// GetSpaceKnowledgeMap godoc
// @Summary      空间知识地图
// @Description  按目录树或知识关联图聚合一个空间的知识结构（KnowAxis V3.0 §4.6）。所有节点与边都已过生命周期与文档 ACL：边只在两端都可见时保留，隐藏数量只返回聚合值。
// @Tags         知识地图
// @Produce      json
// @Param        id     path   string  true   "知识库ID"
// @Param        view   query  string  false  "聚合视角：folder（默认）/relation/topic"
// @Param        limit  query  int     false  "节点上限（默认 300，上限 1000）"
// @Success      200  {object}  map[string]interface{}  "{success, data: KnowledgeMap}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge-bases/{id}/knowledge-map [get]
func (h *KnowledgeMapHandler) GetSpaceKnowledgeMap(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var limit int
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			c.Error(errors.NewBadRequestError("limit must be a number"))
			return
		}
		limit = parsed
	}
	view := types.ParseKnowledgeMapView(c.Query("view"))
	knowledgeMap, err := h.maps.SpaceMap(ctx, tenantID, c.Param("id"), view, limit)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": knowledgeMap})
}

// GetRoleKnowledgeMap godoc
// @Summary      岗位知识地图
// @Description  以标签定义岗位，返回该岗位的知识地图与本人的学习覆盖率（KnowAxis V3.0 §15.5）。覆盖率只统计与本岗位文档相交的学习任务，"无需学习"不计为 100%。
// @Tags         知识地图
// @Produce      json
// @Param        tags   query  string  true   "岗位标签，逗号分隔"
// @Param        limit  query  int     false  "节点上限（默认 300，上限 1000）"
// @Success      200  {object}  map[string]interface{}  "{success, data: RoleKnowledgeMap}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /discovery/role-map [get]
func (h *KnowledgeMapHandler) GetRoleKnowledgeMap(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	tags := normalizeRoleTagQuery(c.Query("tags"))
	if len(tags) == 0 {
		c.Error(errors.NewBadRequestError("tags is required"))
		return
	}
	var limit int
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			c.Error(errors.NewBadRequestError("limit must be a number"))
			return
		}
		limit = parsed
	}
	caller := types.CallerFromContext(ctx)
	roleMap, err := h.maps.RoleMap(ctx, tenantID, caller.UserID, tags, limit)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": roleMap})
}

// normalizeRoleTagQuery splits a comma-separated tag list.
func normalizeRoleTagQuery(raw string) []string {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '，' || r == '\n' || r == '\r'
	})
	return types.NormalizeTagNames(parts)
}
