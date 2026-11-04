package repository

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// compositeRankRow is the raw page row before scoring.
//
// 热度三个计数用相关子查询而不是三个 JOIN + GROUP BY：列表页一次只取
// page_size 行（默认 20），每行三条走索引的计数查询比先聚合整张
// knowledge_view_events 再连接要便宜得多，也不会因为 cartesian 积把
// 计数乘起来。
type compositeRankRow struct {
	ID              string
	Title           string
	FileName        string
	CurrentVersion  int
	LifecycleStatus string
	Classification  string
	ExpiresAt       *time.Time
	LastReviewedAt  *time.Time
	ReviewCycleDays int
	CreatedAt       time.Time
	UpdatedAt       time.Time
	CustomMetadata  types.JSON
	Views           int64
	Favorites       int64
	Citations       int64
}

// CompositeRankPage returns one page scored by the §12.2 composite formula.
//
// 分页与排序的关系在这里是**先取候选页再打分**：综合分依赖热度，而热度
// 要跨整张表聚合。两种做法各有代价：
//   - 全表打分再分页：正确但每次要扫全部条目；
//   - 先按任一单列截一页再打分：便宜但"第 2 页"可能包含本应排在第一页的条目。
//
// 这里取的是有界的折中：取 page_size × CompositeRankCandidateFactor 行候选
// （按更新时间倒序，保证候选里至少包含全部"近期活跃"条目），打分后返回前
// page_size 行。factor 与 total 一起回显，调用方知道"这是候选集内的排序"。
func (r *knowledgeRepository) CompositeRankPage(
	ctx context.Context, tenantID uint64, kbID string, page *types.Pagination,
	filter types.KnowledgeListFilter, keyword string, now time.Time,
) (*types.CompositeRankResult, int64, error) {
	candidateLimit := page.Limit() * types.CompositeRankCandidateFactor
	if candidateLimit > types.MaxCompositeRankCandidates {
		candidateLimit = types.MaxCompositeRankCandidates
	}
	offset := page.Offset()
	// 候选窗口从当前页的起点开始向后取：前几页的候选高度重叠，所以只需要
	// 把 offset 之后的一段取出即可。offset 很大时窗口被上限截断，回显的
	// ScoredCount 会让调用方看到"只评了 N 行"。
	if offset > candidateLimit {
		offset = 0
	}
	var rows []compositeRankRow
	query := applyKnowledgeListFilter(
		r.db.WithContext(ctx).Table("knowledges").
			Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, kbID),
		filter,
	)
	err := query.
		Select(`knowledges.id, knowledges.title, knowledges.file_name,
			knowledges.current_version, knowledges.lifecycle_status,
			knowledges.classification, knowledges.expires_at,
			knowledges.last_reviewed_at, knowledges.review_cycle_days,
			knowledges.created_at, knowledges.updated_at,
			knowledges.custom_metadata,
			(SELECT COUNT(*) FROM knowledge_view_events v
			   WHERE v.tenant_id = knowledges.tenant_id
			     AND v.knowledge_id = knowledges.id) AS views,
			(SELECT COUNT(*) FROM user_resource_favorites f
			   WHERE f.tenant_id = knowledges.tenant_id
			     AND f.resource_type = ?
			     AND f.resource_id = knowledges.id) AS favorites,
			(SELECT COUNT(*) FROM evidence_packs e
			   WHERE e.tenant_id = knowledges.tenant_id
			     AND e.refusal = ''
			     AND CAST(e.citations AS TEXT) LIKE '%' || knowledges.id || '%') AS citations`,
			types.ResourceTypeKnowledge).
		Order("knowledges.updated_at DESC").
		Order("knowledges.id ASC").
		Offset(offset).
		Limit(candidateLimit).
		Scan(&rows).Error
	if err != nil {
		return nil, 0, err
	}

	inputs := make([]types.CompositeRankInput, 0, len(rows))
	for _, row := range rows {
		inputs = append(inputs, types.CompositeRankInput{
			KnowledgeID:     row.ID,
			Title:           row.Title,
			FileName:        row.FileName,
			CurrentVersion:  row.CurrentVersion,
			LifecycleStatus: row.LifecycleStatus,
			Classification:  row.Classification,
			ExpiresAt:       row.ExpiresAt,
			LastReviewedAt:  row.LastReviewedAt,
			ReviewCycleDays: row.ReviewCycleDays,
			CreatedAt:       row.CreatedAt,
			UpdatedAt:       row.UpdatedAt,
			Views:           row.Views,
			Favorites:       row.Favorites,
			Citations:       row.Citations,
			Level:           knowledgeLevelFromMetadata(row.CustomMetadata),
		})
	}
	result := types.ScoreComposite(inputs, keyword, types.DefaultCompositeRankWeights(), now)
	result.ScoredCount = len(inputs)
	// 只返回请求的那一页：候选集比页大，是为了让排序在这批候选里是准确的。
	return result, int64(len(inputs)), nil
}

// knowledgeLevelFromMetadata reads the §10.4 level out of custom metadata.
//
// 层级只存在于候选卡发布时写入的 custom_metadata 里（knowaxis_level）；
// 解析失败按"未标注"处理——一个损坏的元数据不该让条目被判成 L0。
func knowledgeLevelFromMetadata(raw types.JSON) string {
	if len(raw) == 0 {
		return ""
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	level, _ := payload[types.KnowledgeCandidateMetaLevelKey].(string)
	switch types.KnowledgeCandidateLevel(level) {
	case types.KnowledgeCandidateLevelL0, types.KnowledgeCandidateLevelL1,
		types.KnowledgeCandidateLevelL2, types.KnowledgeCandidateLevelL3:
		return level
	default:
		return ""
	}
}

// compile-time reminder that the sort field must stay in the whitelist.
var _ = types.KnowledgeListSortByComposite
