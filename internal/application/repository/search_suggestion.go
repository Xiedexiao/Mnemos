package repository

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// searchSuggestionRepository implements interfaces.SearchSuggestionRepository
// (KnowAxis V3.0 §12.1 搜索建议).
type searchSuggestionRepository struct {
	db *gorm.DB
}

// NewSearchSuggestionRepository constructs the repository.
func NewSearchSuggestionRepository(db *gorm.DB) interfaces.SearchSuggestionRepository {
	return &searchSuggestionRepository{db: db}
}

// RecentQueries returns one user's distinct recent queries.
//
// 用 search_logs 而不是另建一张 history 表：日志已经记录了 (tenant, user,
// query, created_at)，再存一份就多了一条会与日志不一致的真相。DISTINCT 的
// 取法是"每个词取它最近一次"，因此同一词重复搜索只会出现一次且排在最前。
func (r *searchSuggestionRepository) RecentQueries(
	ctx context.Context, tenantID uint64, userID string, limit int,
) ([]types.SearchSuggestion, error) {
	if userID == "" {
		return nil, nil
	}
	if limit <= 0 || limit > types.MaxSuggestionLimit {
		limit = types.DefaultSuggestionLimit
	}
	type historyRow struct {
		Query string
	}
	var rows []historyRow
	// 分组取 MAX(created_at) 排序，再取原始 query（不是归一化形式）：
	// 历史列表要回填用户当时输入的大小写与空格，否则点回去会像"被改写"。
	// GROUP BY query（不是 query_normalized）：历史要回填用户当时的原样输入。
	// 选择 query 而不是 MAX(query) 会让 Postgres 报 "must appear in GROUP BY"，
	// 因此分组键与投影列必须是同一列。
	err := r.db.WithContext(ctx).Table("search_logs").
		Select("query AS query, MAX(created_at) AS latest").
		Where("tenant_id = ? AND user_id = ? AND query <> ''", tenantID, userID).
		Group("query").
		Order("latest DESC, query ASC").
		Limit(limit).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]types.SearchSuggestion, 0, len(rows))
	for _, row := range rows {
		query := strings.TrimSpace(row.Query)
		if query == "" {
			continue
		}
		out = append(out, types.SearchSuggestion{
			Text: query, Kind: types.SuggestionKindHistory,
		})
	}
	return out, nil
}

// HotQueries returns the tenant's most frequent queries in the window.
//
// 按归一化形式分组（大小写与空格不能把一个词拆成两个），但回填该组里
// 最新的原始写法——读者点进去应该看到人写的词。零结果的查询**不排除**：
// "大家常搜但搜不到"恰恰是读者最需要知道的，排除它会让热门榜变成"已经有
// 答案的词"，而这正是读者已经知道的部分。
func (r *searchSuggestionRepository) HotQueries(
	ctx context.Context, tenantID uint64, since time.Time, limit int,
) ([]types.SearchSuggestion, error) {
	if limit <= 0 || limit > types.MaxSuggestionLimit {
		limit = types.DefaultSuggestionLimit
	}
	type hotRow struct {
		QueryNormalized string
		Query           string
		Occurrences     int64
	}
	var rows []hotRow
	err := r.db.WithContext(ctx).Table("search_logs").
		Select(`query_normalized, MAX(query) AS query, COUNT(*) AS occurrences`).
		Where("tenant_id = ? AND created_at >= ? AND query_normalized <> ''", tenantID, since.UTC()).
		Group("query_normalized").
		Order("occurrences DESC, query_normalized ASC").
		Limit(limit).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]types.SearchSuggestion, 0, len(rows))
	for _, row := range rows {
		text := strings.TrimSpace(row.Query)
		if text == "" {
			text = row.QueryNormalized
		}
		if text == "" {
			continue
		}
		out = append(out, types.SearchSuggestion{
			Text: text, Kind: types.SuggestionKindHot, Count: row.Occurrences,
		})
	}
	return out, nil
}

// TitleMatches returns visible documents whose title contains the prefix.
//
// 生命周期口径与检索一致：草稿、过期、下架不参与联想——它们本来就搜不到，
// 出现在联想里就是"提示了一条点进去看不到的结果"。ACL 由服务层过滤。
func (r *searchSuggestionRepository) TitleMatches(
	ctx context.Context, tenantID uint64, knowledgeBaseIDs []string,
	prefix string, limit int,
) ([]types.SearchSuggestion, error) {
	if limit <= 0 || limit > types.MaxSuggestionLimit {
		limit = types.DefaultSuggestionLimit
	}
	escaped := strings.ToLower(escapeLikeKeyword(prefix))
	if escaped == "" {
		return nil, nil
	}
	type titleRow struct {
		ID              string
		Title           string
		KnowledgeBaseID string
	}
	var rows []titleRow
	query := r.db.WithContext(ctx).Table("knowledges AS k").
		Select("k.id, k.title, k.knowledge_base_id").
		Where("k.tenant_id = ?", tenantID).
		Where("k.deleted_at IS NULL").
		Where("COALESCE(NULLIF(k.lifecycle_status, ''), 'valid') IN ?",
			[]string{
				string(types.KnowledgeLifecycleValid),
				string(types.KnowledgeLifecyclePendingReviewCycle),
				string(types.KnowledgeLifecycleStale),
			}).
		Where("LOWER(k.title) LIKE ? ESCAPE ?", "%"+escaped+"%", likeEscapeChar)
	if len(knowledgeBaseIDs) > 0 {
		query = query.Where("k.knowledge_base_id IN ?", knowledgeBaseIDs)
	}
	// 前缀匹配的排在包含匹配之前："E45 报" 命中 "E45 报警处理" 比命中
	// "关于 E45 报警的补充说明" 更可能是读者想要的。
	// Order 只接受一个参数，带占位符的 CASE 必须包成 clause.Expr；
	// 直接把参数摊进字符串会丢掉绑定（也就丢掉了 LIKE 的转义）。
	err := query.
		Order(clause.OrderBy{Expression: clause.Expr{
			SQL:  "CASE WHEN LOWER(k.title) LIKE ? ESCAPE ? THEN 0 ELSE 1 END, k.title ASC",
			Vars: []interface{}{escaped + "%", likeEscapeChar},
		}}).
		Limit(limit).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]types.SearchSuggestion, 0, len(rows))
	for _, row := range rows {
		title := strings.TrimSpace(row.Title)
		if title == "" {
			continue
		}
		out = append(out, types.SearchSuggestion{
			Text:            title,
			Kind:            types.SuggestionKindTitle,
			KnowledgeID:     row.ID,
			KnowledgeBaseID: row.KnowledgeBaseID,
		})
	}
	return out, nil
}

// DeleteHistory removes one user's own search-log rows.
//
// 只删 (tenant, user) 这一组，不碰别人的行：条件里带 user_id 是唯一的
// 越权防线，任何"按 query 删"的写法都会波及同租户其他搜过同一个词的同事。
func (r *searchSuggestionRepository) DeleteHistory(
	ctx context.Context, tenantID uint64, userID string,
) error {
	if userID == "" {
		return nil
	}
	return r.db.WithContext(ctx).
		Where("tenant_id = ? AND user_id = ?", tenantID, userID).
		Delete(&types.SearchLog{}).Error
}
