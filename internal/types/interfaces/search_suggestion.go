package interfaces

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// SearchSuggestionRepository supplies the raw rows behind §12.1 suggestions.
//
// 三个方法各自不知道权限：历史天然按 user_id 取；热门与标题必须由服务层
// 按调用者过滤后才能返回（§12.6 建议不泄露）。因此这里刻意不提供
// "全局热门榜"这种签名——一旦有，调用方迟早会直接把全局结果返回出去。
type SearchSuggestionRepository interface {
	// RecentQueries returns one user's distinct recent queries, newest first.
	RecentQueries(
		ctx context.Context, tenantID uint64, userID string, limit int,
	) ([]types.SearchSuggestion, error)
	// HotQueries returns the tenant's most frequent queries. It is
	// unfiltered by ACL: the caller must re-weight it against what the reader
	// may see (§12.6 排序不泄露), which is why it is not named
	// "VisibleHotQueries".
	HotQueries(
		ctx context.Context, tenantID uint64, since time.Time, limit int,
	) ([]types.SearchSuggestion, error)
	// DeleteHistory removes one user's own search-log rows.
	//
	// 历史是 search_logs 的投影，所以"清空历史"就是删掉自己的行。已聚合
	// 成知识缺口的记录不受影响：缺口是按归一化问题独立计数的，删日志不会
	// 让团队的缺口清单凭空少一条。
	DeleteHistory(ctx context.Context, tenantID uint64, userID string) error
	// TitleMatches returns visible documents whose title contains the prefix.
	// knowledgeBaseIDs narrows the search; an empty list means the whole
	// tenant. The caller passes only KBs it has already authorized.
	TitleMatches(
		ctx context.Context, tenantID uint64, knowledgeBaseIDs []string,
		prefix string, limit int,
	) ([]types.SearchSuggestion, error)
}

// SearchSuggestionService is the §12.1 suggestion surface.
type SearchSuggestionService interface {
	// Suggest builds the bundle for one caller. prefix may be empty: that is
	// the "search box just opened" case, which returns history and hot only.
	Suggest(
		ctx context.Context, tenantID uint64, prefix string, limit, hotDays int,
	) (*types.SuggestionBundle, error)
	// RecordQuery pushes one query into the caller's history. Called by the
	// search path after a query actually ran, so the history is what the user
	// really searched, not what they typed and abandoned.
	RecordQuery(ctx context.Context, tenantID uint64, userID, query string) error
	// ClearHistory removes the caller's own history.
	ClearHistory(ctx context.Context, tenantID uint64, userID string) error
}
