package handler

import (
	"strconv"

	"github.com/gin-gonic/gin"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// ReadingHandler exposes §15.2 reading-page outline and progress.
type ReadingHandler struct {
	reading interfaces.ReadingService
}

// NewReadingHandler constructs the handler.
func NewReadingHandler(reading interfaces.ReadingService) *ReadingHandler {
	return &ReadingHandler{reading: reading}
}

// readingProgressRequest is the save body.
type readingProgressRequest struct {
	KnowledgeID string  `json:"knowledge_id"`
	ChunkID     string  `json:"chunk_id"`
	Percent     float64 `json:"percent"`
	ChunkCount  int     `json:"chunk_count"`
}

// GetKnowledgeTOC godoc
// @Summary      阅读页目录
// @Description  从分块结果推导文档目录（KnowAxis V3.0 §15.2）。识别 Markdown 标题与中文编号标题；与正文同级受权限约束。
// @Tags         阅读
// @Produce      json
// @Param        id  path  string  true  "知识ID"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge/{id}/reading-toc [get]
func (h *ReadingHandler) GetKnowledgeTOC(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	toc, err := h.reading.TOC(ctx, tenantID, c.Param("id"))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": toc})
}

// SaveReadingProgress godoc
// @Summary      保存阅读进度
// @Description  记录本人在一篇文档中的阅读位置（KnowAxis V3.0 §15.2）。读到 95% 以上视为读完。
// @Tags         阅读
// @Accept       json
// @Produce      json
// @Param        request  body  readingProgressRequest  true  "进度"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /reading/progress [put]
func (h *ReadingHandler) SaveReadingProgress(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req readingProgressRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(apperrors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	progress, err := h.reading.SaveProgress(ctx, tenantID, caller.UserID,
		types.ReadingProgressInput{
			KnowledgeID: req.KnowledgeID,
			ChunkID:     req.ChunkID,
			Percent:     req.Percent,
			ChunkCount:  req.ChunkCount,
		})
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": progress})
}

// GetReadingProgress godoc
// @Summary      读取阅读进度
// @Description  读取本人在某一篇文档中的阅读位置；没有记录时返回 null（前端从顶部开始）。
// @Tags         阅读
// @Produce      json
// @Param        knowledge_id  query  string  true  "知识ID"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /reading/progress [get]
func (h *ReadingHandler) GetReadingProgress(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	caller := types.CallerFromContext(ctx)
	progress, err := h.reading.GetProgress(ctx, tenantID, caller.UserID, c.Query("knowledge_id"))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": progress})
}

// ContinueReading godoc
// @Summary      继续阅读
// @Description  最近的阅读位置（KnowAxis V3.0 §15.2）。无权访问的文档保留占位但不显示标题。
// @Tags         阅读
// @Produce      json
// @Param        limit  query  int  false  "条数（默认 20）"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /reading/continue [get]
func (h *ReadingHandler) ContinueReading(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if err != nil || limit <= 0 || limit > 100 {
		limit = 20
	}
	caller := types.CallerFromContext(ctx)
	list, listErr := h.reading.ContinueReading(ctx, tenantID, caller.UserID, limit)
	if listErr != nil {
		c.Error(listErr)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": list})
}
