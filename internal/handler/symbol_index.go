package handler

import (
	"strings"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

// SymbolIndexHandler serves the exact-retrieval channel endpoints
// (KnowAxis V3.0 §12.7 精准索引 v1). Indexing is Contributor+ with KB
// write access; lookups are Viewer+; the role floors live on the routes.
type SymbolIndexHandler struct {
	symbolIndexService interfaces.SymbolIndexService
	knowledgeService   interfaces.KnowledgeService
}

// NewSymbolIndexHandler constructs the handler. knowledgeService resolves
// the :id param's parent KB so the caller never supplies kb_id in the
// body (错配的 kb_id 不能污染索引行).
func NewSymbolIndexHandler(
	symbolIndexService interfaces.SymbolIndexService,
	knowledgeService interfaces.KnowledgeService,
) *SymbolIndexHandler {
	return &SymbolIndexHandler{
		symbolIndexService: symbolIndexService,
		knowledgeService:   knowledgeService,
	}
}

// indexSymbolsRequest is the body of POST /knowledge/:id/symbols.
// chunk_id 空 = 对整篇建文档级索引。
type indexSymbolsRequest struct {
	ChunkID string `json:"chunk_id"`
	Text    string `json:"text" binding:"required"`
}

// IndexKnowledgeSymbols godoc
// @Summary      建符号索引
// @Description  从 text 抽取符号候选（大写字母≥2+数字组合的 token；E 前缀错误码放行）并按 (knowledge, chunk) 批量 upsert。chunk_id 空则对整篇建文档级索引；重复调用幂等。kb_id 由 :id 反查，不接受 body 传入。
// @Tags         符号索引
// @Accept       json
// @Produce      json
// @Param        id      path  string               true  "知识文档ID"
// @Param        request body  indexSymbolsRequest  true  "建索引"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {object}  errors.AppError
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge/{id}/symbols [post]
func (h *SymbolIndexHandler) IndexKnowledgeSymbols(c *gin.Context) {
	ctx := c.Request.Context()
	var req indexSymbolsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	knowledgeID := c.Param("id")
	// kb_id 从 :id 反查。KBAccessWriteFromKnowledgeIDParam 已把请求租户
	// 上下文改写到 KB 归属租户，GetKnowledgeByID 按该租户解析。
	knowledge, err := h.knowledgeService.GetKnowledgeByID(ctx, knowledgeID)
	if err != nil {
		c.Error(err)
		return
	}
	if knowledge == nil {
		c.Error(errors.NewNotFoundError("knowledge not found"))
		return
	}
	chunkID := strings.TrimSpace(req.ChunkID)
	rows, err := h.symbolIndexService.IndexSymbols(
		ctx, caller.TenantID, knowledge.KnowledgeBaseID, knowledgeID, chunkID, req.Text,
	)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": gin.H{
		"knowledge_id": knowledgeID,
		"chunk_id":     chunkID,
		"indexed":      len(rows),
		"symbols":      rows,
	}})
}

// ListKnowledgeSymbols godoc
// @Summary      查看知识的符号索引
// @Description  列出一篇知识文档已索引的全部符号行（含 chunk 定位与 symbol_type）。
// @Tags         符号索引
// @Produce      json
// @Param        id  path  string  true  "知识文档ID"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge/{id}/symbols [get]
func (h *SymbolIndexHandler) ListKnowledgeSymbols(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	rows, err := h.symbolIndexService.ListByKnowledge(ctx, caller.TenantID, c.Param("id"))
	if err != nil {
		c.Error(err)
		return
	}
	if rows == nil {
		rows = []*types.SymbolIndex{}
	}
	c.JSON(200, gin.H{"success": true, "data": rows})
}

// SymbolLookup godoc
// @Summary      符号精确命中
// @Description  从 q 用同一正则抽取符号候选，对 symbol_index 等值命中，按符号长度降序返回 [{knowledge_id, chunk_id, symbol, locator}]。kb_ids 逗号分隔，缺省全租户。
// @Tags         符号索引
// @Produce      json
// @Param        q       query  string  false  "查询文本"
// @Param        kb_ids  query  string  false  "知识库ID列表（逗号分隔）"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {object}  errors.AppError
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /symbol-lookup [get]
func (h *SymbolIndexHandler) SymbolLookup(c *gin.Context) {
	ctx := c.Request.Context()
	q := c.Query("q")
	if strings.TrimSpace(q) == "" {
		c.Error(errors.NewBadRequestError("q is required"))
		return
	}
	var kbIDs []string
	if raw := strings.TrimSpace(c.Query("kb_ids")); raw != "" {
		for _, id := range strings.Split(raw, ",") {
			if id = strings.TrimSpace(id); id != "" {
				kbIDs = append(kbIDs, id)
			}
		}
	}
	caller := types.CallerFromContext(ctx)
	hits, err := h.symbolIndexService.ExactLookup(ctx, caller.TenantID, kbIDs, q)
	if err != nil {
		c.Error(err)
		return
	}
	if hits == nil {
		hits = []*types.SymbolHit{}
	}
	c.JSON(200, gin.H{"success": true, "data": hits})
}
