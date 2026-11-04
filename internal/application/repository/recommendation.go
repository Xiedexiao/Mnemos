package repository

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// recommendationRepository implements interfaces.RecommendationRepository
// (KnowAxis V3.0 §15.3 推荐 / §16.1 热门).
type recommendationRepository struct {
	db *gorm.DB
}

// NewRecommendationRepository constructs the repository.
func NewRecommendationRepository(db *gorm.DB) interfaces.RecommendationRepository {
	return &recommendationRepository{db: db}
}

// recommendationSelect is the shared projection: every recommendation query
// returns the same shape so the service can rank without per-query DTOs.
const recommendationSelect = "k.id AS knowledge_id, k.knowledge_base_id, k.title, " +
	"k.description AS summary, k.lifecycle_status, k.classification, k.updated_at"

// RecordView appends one view event unless the user already viewed this
// document inside the dedupe window.
func (r *recommendationRepository) RecordView(
	ctx context.Context, tenantID uint64, userID, knowledgeID, knowledgeBaseID string,
	now time.Time,
) (bool, error) {
	since := now.Add(-types.RecommendationViewDedupeWindow)
	var existing int64
	if err := r.db.WithContext(ctx).
		Model(&types.KnowledgeViewEvent{}).
		Where("tenant_id = ? AND user_id = ? AND knowledge_id = ? AND created_at >= ?",
			tenantID, userID, knowledgeID, since).
		Count(&existing).Error; err != nil {
		return false, err
	}
	if existing > 0 {
		return false, nil
	}
	event := &types.KnowledgeViewEvent{
		TenantID: tenantID, UserID: userID, KnowledgeID: knowledgeID,
		KnowledgeBaseID: knowledgeBaseID, CreatedAt: now.UTC(),
	}
	if err := r.db.WithContext(ctx).Create(event).Error; err != nil {
		return false, err
	}
	return true, nil
}

// ListHistory returns the user's recent views, newest first.
func (r *recommendationRepository) ListHistory(
	ctx context.Context, tenantID uint64, userID string, limit int,
) ([]*types.KnowledgeViewEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var events []*types.KnowledgeViewEvent
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND user_id = ?", tenantID, userID).
		Order("created_at DESC").
		Limit(limit).
		Find(&events).Error; err != nil {
		return nil, err
	}
	return events, nil
}

// liveKnowledge restricts a recommendation query to rows that may be shown.
//
// 推荐位与检索结果用同一个"可搜索"集合（§7.1 Searchable）：有效、待复审、
// 降级三种可见；草稿、审批中、过期、已下架、已替换、归档一律不出现在推荐里。
// 密级与文档 ACL 由服务层再过滤，因为那需要主体展开。
func liveKnowledge(query *gorm.DB) *gorm.DB {
	return query.Where("k.deleted_at IS NULL").
		Where("COALESCE(NULLIF(k.lifecycle_status, ''), 'valid') IN ?",
			[]string{
				string(types.KnowledgeLifecycleValid),
				string(types.KnowledgeLifecyclePendingReviewCycle),
				string(types.KnowledgeLifecycleStale),
			})
}

// RelatedKnowledge returns documents linked to one by a typed relation.
func (r *recommendationRepository) RelatedKnowledge(
	ctx context.Context, tenantID uint64, knowledgeID string, limit int,
) ([]types.RecommendationItem, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	var items []types.RecommendationItem
	query := r.db.WithContext(ctx).Table("knowledges AS k").
		Select(recommendationSelect).
		Joins(`JOIN knowledge_relations rel ON
			(rel.from_knowledge_id = k.id AND rel.to_knowledge_id = ?)
			OR (rel.to_knowledge_id = k.id AND rel.from_knowledge_id = ?)`, knowledgeID, knowledgeID).
		Where("rel.tenant_id = ? AND k.tenant_id = ?", tenantID, tenantID).
		Where("k.id <> ?", knowledgeID)
	if err := liveKnowledge(query).Group(recommendationSelect).Limit(limit).
		Find(&items).Error; err != nil {
		return nil, err
	}
	for index := range items {
		items[index].Reason = types.RecommendReasonRelated
	}
	return items, nil
}

// SameTagKnowledge returns documents sharing at least one tag.
func (r *recommendationRepository) SameTagKnowledge(
	ctx context.Context, tenantID uint64, knowledgeID string, limit int,
) ([]types.RecommendationItem, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	var items []types.RecommendationItem
	query := r.db.WithContext(ctx).Table("knowledges AS k").
		Select(recommendationSelect+", MIN(t.name) AS matched_tag").
		Joins(`JOIN knowledge_tag_relations mine ON mine.knowledge_id = ?`, knowledgeID).
		Joins(`JOIN knowledge_tags mt ON mt.id = mine.tag_id`).
		Joins(`JOIN knowledge_tags t ON t.name = mt.name AND t.tenant_id = mt.tenant_id`).
		Joins(`JOIN knowledge_tag_relations r ON r.tag_id = t.id`).
		Where("r.knowledge_id = k.id AND k.id <> ?", knowledgeID).
		Where("k.tenant_id = ? AND t.tenant_id = ?", tenantID, tenantID)
	if err := liveKnowledge(query).Group(recommendationSelect).Limit(limit).
		Find(&items).Error; err != nil {
		return nil, err
	}
	for index := range items {
		items[index].Reason = types.RecommendReasonSameTag
	}
	return items, nil
}

// SameSpaceKnowledge returns other documents in the same space.
func (r *recommendationRepository) SameSpaceKnowledge(
	ctx context.Context, tenantID uint64, knowledgeBaseID, excludeKnowledgeID string,
	limit int,
) ([]types.RecommendationItem, error) {
	if knowledgeBaseID == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	var items []types.RecommendationItem
	query := r.db.WithContext(ctx).Table("knowledges AS k").
		Select(recommendationSelect).
		Where("k.tenant_id = ? AND k.knowledge_base_id = ?", tenantID, knowledgeBaseID)
	if excludeKnowledgeID != "" {
		query = query.Where("k.id <> ?", excludeKnowledgeID)
	}
	if err := liveKnowledge(query).Order("k.updated_at DESC").Limit(limit).
		Find(&items).Error; err != nil {
		return nil, err
	}
	for index := range items {
		items[index].Reason = types.RecommendReasonSameSpace
	}
	return items, nil
}

// InterestTags returns the tags the user read or starred most often.
//
// 两路来源：最近阅读过的知识的标签，以及收藏的知识的标签。阅读权重高于
// 收藏是因为阅读是更强的意图信号（收藏常常是"以后再看"）。
func (r *recommendationRepository) InterestTags(
	ctx context.Context, tenantID uint64, userID string, since time.Time, limit int,
) ([]string, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	type tagRow struct {
		Name  string
		Score int64
	}
	var rows []tagRow
	err := r.db.WithContext(ctx).Raw(`
		SELECT t.name AS name, SUM(weighted.score) AS score FROM (
			SELECT rel.tag_id AS tag_id, 2 AS score
			FROM knowledge_view_events v
			JOIN knowledge_tag_relations rel ON rel.knowledge_id = v.knowledge_id
			WHERE v.tenant_id = ? AND v.user_id = ? AND v.created_at >= ?
			UNION ALL
			SELECT rel.tag_id AS tag_id, 1 AS score
			FROM user_resource_favorites f
			JOIN knowledge_tag_relations rel ON rel.knowledge_id = f.resource_id
			WHERE f.tenant_id = ? AND f.user_id = ? AND f.resource_type = ?
		) AS weighted
		JOIN knowledge_tags t ON t.id = weighted.tag_id
		WHERE t.tenant_id = ?
		GROUP BY t.name
		ORDER BY score DESC, t.name ASC
		LIMIT ?`,
		tenantID, userID, since,
		tenantID, userID, types.ResourceTypeKnowledge,
		tenantID, limit).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	tags := make([]string, 0, len(rows))
	for _, row := range rows {
		tags = append(tags, row.Name)
	}
	return tags, nil
}

// KnowledgeForTags returns documents carrying any of the tags.
func (r *recommendationRepository) KnowledgeForTags(
	ctx context.Context, tenantID uint64, tags []string, excludeKnowledgeID string,
	limit int,
) ([]types.RecommendationItem, error) {
	cleaned := make([]string, 0, len(tags))
	for _, tag := range tags {
		if trimmed := strings.TrimSpace(tag); trimmed != "" {
			cleaned = append(cleaned, trimmed)
		}
	}
	if len(cleaned) == 0 {
		return nil, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	var items []types.RecommendationItem
	query := r.db.WithContext(ctx).Table("knowledges AS k").
		Select(recommendationSelect+", MIN(t.name) AS matched_tag").
		Joins(`JOIN knowledge_tag_relations r ON r.knowledge_id = k.id`).
		Joins(`JOIN knowledge_tags t ON t.id = r.tag_id`).
		Where("k.tenant_id = ? AND t.tenant_id = ?", tenantID, tenantID).
		Where("t.name IN ?", cleaned)
	if excludeKnowledgeID != "" {
		query = query.Where("k.id <> ?", excludeKnowledgeID)
	}
	if err := liveKnowledge(query).Group(recommendationSelect).
		Order("k.updated_at DESC").Limit(limit).
		Find(&items).Error; err != nil {
		return nil, err
	}
	for index := range items {
		items[index].Reason = types.RecommendReasonInterest
	}
	return items, nil
}

// LatestKnowledge returns the newest documents.
func (r *recommendationRepository) LatestKnowledge(
	ctx context.Context, tenantID uint64, knowledgeBaseID string, limit int,
) ([]types.RecommendationItem, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	var items []types.RecommendationItem
	query := r.db.WithContext(ctx).Table("knowledges AS k").
		Select(recommendationSelect).
		Where("k.tenant_id = ?", tenantID)
	if knowledgeBaseID != "" {
		query = query.Where("k.knowledge_base_id = ?", knowledgeBaseID)
	}
	if err := liveKnowledge(query).Order("k.updated_at DESC").Limit(limit).
		Find(&items).Error; err != nil {
		return nil, err
	}
	for index := range items {
		items[index].Reason = types.RecommendReasonLatest
	}
	return items, nil
}

// HotKnowledge returns the most-viewed documents since the given time.
func (r *recommendationRepository) HotKnowledge(
	ctx context.Context, tenantID uint64, knowledgeBaseID string, since time.Time,
	limit int,
) ([]types.RecommendationItem, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	type hotRow struct {
		KnowledgeID string
		Views       int64
	}
	var rows []hotRow
	query := r.db.WithContext(ctx).Table("knowledge_view_events").
		Select("knowledge_id, COUNT(*) AS views").
		Where("tenant_id = ? AND created_at >= ?", tenantID, since)
	if knowledgeBaseID != "" {
		query = query.Where("knowledge_base_id = ?", knowledgeBaseID)
	}
	if err := query.Group("knowledge_id").Order("views DESC").
		Limit(limit).Scan(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(rows))
	views := make(map[string]int64, len(rows))
	for _, row := range rows {
		ids = append(ids, row.KnowledgeID)
		views[row.KnowledgeID] = row.Views
	}
	summaries, err := r.KnowledgeSummary(ctx, tenantID, ids)
	if err != nil {
		return nil, err
	}
	items := make([]types.RecommendationItem, 0, len(ids))
	for _, id := range ids {
		item, ok := summaries[id]
		if !ok {
			// 已被删除或已不可展示：热度榜不能把它带出来。
			continue
		}
		item.Reason = types.RecommendReasonHot
		item.Score = float64(views[id])
		items = append(items, item)
	}
	return items, nil
}

// KnowledgeSummary returns the display projection for a set of documents.
func (r *recommendationRepository) KnowledgeSummary(
	ctx context.Context, tenantID uint64, knowledgeIDs []string,
) (map[string]types.RecommendationItem, error) {
	out := make(map[string]types.RecommendationItem, len(knowledgeIDs))
	cleaned := types.NormalizeKnowledgeIDs(knowledgeIDs)
	if len(cleaned) == 0 {
		return out, nil
	}
	var items []types.RecommendationItem
	query := r.db.WithContext(ctx).Table("knowledges AS k").
		Select(recommendationSelect).
		Where("k.tenant_id = ? AND k.id IN ?", tenantID, cleaned)
	if err := liveKnowledge(query).Find(&items).Error; err != nil {
		return nil, err
	}
	for _, item := range items {
		out[item.KnowledgeID] = item
	}
	return out, nil
}
