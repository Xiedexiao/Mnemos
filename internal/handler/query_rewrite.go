package handler

import (
	"strings"

	"github.com/gin-gonic/gin"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// QueryRewriteHandler exposes the §12.1 fault-tolerance surface.
type QueryRewriteHandler struct {
	rewrite interfaces.QueryRewriteService
}

// NewQueryRewriteHandler constructs the handler.
func NewQueryRewriteHandler(rewrite interfaces.QueryRewriteService) *QueryRewriteHandler {
	return &QueryRewriteHandler{rewrite: rewrite}
}

// GetQueryRewrite godoc
// @Summary      查询容错（拼音与错别字）
// @Description  对一条查询给出容错计划（KnowAxis V3.0 §12.1 容错）：中文查询返回无调拼音与语料中的纠错候选；纯拼音输入返回它可能拼的中文词。**原查询永远保留**，备选词只是额外召回路径——服务端不会替用户改词。候选只来自调用者可见的空间与租户术语表；含数字的编号片段不纠错（§12.1 编号精确通道）。
// @Tags         查询容错
// @Produce      json
// @Param        q              query  string  true   "查询文本"
// @Param        knowledge_base_ids  query  string  false  "限定空间，逗号分隔（默认取调用者可见空间）"
// @Success      200  {object}  map[string]interface{}  "{success, data: QueryRewrite}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /search/rewrite [get]
func (h *QueryRewriteHandler) GetQueryRewrite(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	query := strings.TrimSpace(c.Query("q"))
	if query == "" {
		c.Error(apperrors.NewBadRequestError("q is required"))
		return
	}
	kbIDs := splitCommaList(c.Query("knowledge_base_ids"))
	plan, err := h.rewrite.Rewrite(ctx, tenantID, kbIDs, query)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": plan})
}

// splitCommaList splits a comma-separated query parameter, dropping blanks.
func splitCommaList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
