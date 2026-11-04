package handler

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// SearchSuggestionHandler exposes the §12.1 suggestion surfaces.
type SearchSuggestionHandler struct {
	suggestions interfaces.SearchSuggestionService
}

// NewSearchSuggestionHandler constructs the handler.
func NewSearchSuggestionHandler(
	suggestions interfaces.SearchSuggestionService,
) *SearchSuggestionHandler {
	return &SearchSuggestionHandler{suggestions: suggestions}
}

// GetSearchSuggestions godoc
// @Summary      搜索建议
// @Description  搜索框联想（KnowAxis V3.0 §12.1）：历史搜索（本人）、热门搜索（本租户窗口内高频词）、实时联想（标题，已按 ACL 过滤）、术语联想。建议只来自调用者可见集合；标题联想在 ACL 查询失败时整体为空而不是放行（§12.6 建议不泄露）。q 为空时只返回历史与热门，即"搜索框刚打开"的状态。
// @Tags         搜索建议
// @Produce      json
// @Param        q          query  string  true   "已输入的前缀（可空）"
// @Param        limit      query  int     false  "每个来源的条数（默认 8，上限 30）"
// @Param        hot_days   query  int     false  "热门榜窗口天数（默认 30，上限 365）"
// @Success      200  {object}  map[string]interface{}  "{success, data: SuggestionBundle}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /search/suggestions [get]
func (h *SearchSuggestionHandler) GetSearchSuggestions(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	limit, err := suggestionIntQuery(c, "limit")
	if err != nil {
		c.Error(err)
		return
	}
	hotDays, err := suggestionIntQuery(c, "hot_days")
	if err != nil {
		c.Error(err)
		return
	}
	bundle, bundleErr := h.suggestions.Suggest(
		ctx, tenantID, c.Query("q"), limit, hotDays)
	if bundleErr != nil {
		c.Error(bundleErr)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": bundle})
}

// DeleteSearchHistory godoc
// @Summary      清空搜索历史
// @Description  删除本人（且仅本人）的搜索历史（KnowAxis V3.0 §12.1 历史搜索）。历史是 search_logs 的投影，删除只影响该用户自己的行；已聚合成知识缺口的记录不受影响。
// @Tags         搜索建议
// @Produce      json
// @Success      200  {object}  map[string]interface{}  "{success}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /search/history [delete]
func (h *SearchSuggestionHandler) DeleteSearchHistory(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	// 身份恒取自上下文，不接受 user_id 参数：否则任何登录用户都能清空
	// 同事的搜索记录（既毁数据，也构成对他人行为的干扰）。
	caller := types.CallerFromContext(ctx)
	if err := h.suggestions.ClearHistory(ctx, tenantID, caller.UserID); err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true})
}

// suggestionIntQuery parses an optional non-negative int query parameter.
func suggestionIntQuery(c *gin.Context, name string) (int, error) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return 0, nil
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return 0, apperrors.NewBadRequestError(name + " must be a number")
	}
	if parsed < 0 {
		return 0, apperrors.NewBadRequestError(name + " must not be negative")
	}
	return parsed, nil
}
