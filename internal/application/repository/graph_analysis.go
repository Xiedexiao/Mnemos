package repository

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// graphAnalysisRepository implements interfaces.GraphAnalysisRepository
// (KnowAxis V3.0 §11.1 中心度 / 社区检测 / 专家发现).
type graphAnalysisRepository struct {
	db *gorm.DB
}

// NewGraphAnalysisRepository constructs the repository.
func NewGraphAnalysisRepository(db *gorm.DB) interfaces.GraphAnalysisRepository {
	return &graphAnalysisRepository{db: db}
}

// analysisNodeColumns is the projection of one analysis node.
const analysisNodeColumns = "k.id AS knowledge_id, k.knowledge_base_id, k.title, " +
	"COALESCE(NULLIF(k.lifecycle_status, ''), 'valid') AS lifecycle_status"

// analysisableKnowledge is the lifecycle gate shared by every analysis query.
//
// 与知识地图、推荐、检索同一集合：草稿还没发布，过期/下架不该再被当成
// "核心资产"，审批中的内容在结论出来前不该影响全租户的中心度排行。
func analysisableKnowledge(query *gorm.DB) *gorm.DB {
	return query.Where("k.deleted_at IS NULL").
		Where("COALESCE(NULLIF(k.lifecycle_status, ''), 'valid') IN ?",
			[]string{
				string(types.KnowledgeLifecycleValid),
				string(types.KnowledgeLifecyclePendingReviewCycle),
				string(types.KnowledgeLifecycleStale),
			})
}

// AnalysisNodes returns the documents that participate in graph analysis.
func (r *graphAnalysisRepository) AnalysisNodes(
	ctx context.Context, tenantID uint64, knowledgeBaseID string, limit int,
) ([]types.GraphAnalysisNode, error) {
	if limit <= 0 || limit > types.MaxGraphAnalysisNodes {
		limit = types.MaxGraphAnalysisNodes
	}
	var nodes []types.GraphAnalysisNode
	query := r.db.WithContext(ctx).Table("knowledges AS k").
		Select(analysisNodeColumns).
		Where("k.tenant_id = ?", tenantID)
	if knowledgeBaseID != "" {
		query = query.Where("k.knowledge_base_id = ?", knowledgeBaseID)
	}
	if err := analysisableKnowledge(query).
		Order("k.id ASC").
		Limit(limit).
		Find(&nodes).Error; err != nil {
		return nil, err
	}
	return nodes, nil
}

// analysisEdgeBatch bounds one IN list.
//
// 2000 个节点会让两端各展开一次 IN，SQLite 的变量上限虽然够（3.32+ 是
// 32766），但把一条语句压到 4000 个占位符既难读也难诊断。分批在 Go 侧
// 合并结果，成本可以忽略。
const analysisEdgeBatch = 400

// AnalysisEdges returns the edges whose both ends are inside the node set.
func (r *graphAnalysisRepository) AnalysisEdges(
	ctx context.Context, tenantID uint64, knowledgeIDs []string,
) ([]types.KnowledgeMapEdge, error) {
	cleaned := types.NormalizeKnowledgeIDs(knowledgeIDs)
	if len(cleaned) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(cleaned))
	edges := make([]types.KnowledgeMapEdge, 0, len(cleaned))
	for start := 0; start < len(cleaned); start += analysisEdgeBatch {
		end := start + analysisEdgeBatch
		if end > len(cleaned) {
			end = len(cleaned)
		}
		var batch []types.KnowledgeMapEdge
		err := r.db.WithContext(ctx).Table("knowledge_relations").
			Select("from_knowledge_id AS \"from\", to_knowledge_id AS \"to\", relation_type, origin").
			Where("tenant_id = ?", tenantID).
			Where("direction = ?", "forward").
			Where("from_knowledge_id IN ? AND to_knowledge_id IN ?", cleaned[start:end], cleaned).
			Order("from_knowledge_id ASC, to_knowledge_id ASC").
			Scan(&batch).Error
		if err != nil {
			return nil, err
		}
		for _, edge := range batch {
			// 分批查询时同一条边可能被两个批次各取到一次（两端分处不同批），
			// 按 (from,to,type) 去重后再返回，否则社区检测的内部边数会翻倍。
			key := edge.From + "\x00" + edge.To + "\x00" + edge.RelationType
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			edges = append(edges, edge)
		}
	}
	return edges, nil
}

// ExpertAuthorship resolves who authored each document.
func (r *graphAnalysisRepository) ExpertAuthorship(
	ctx context.Context, tenantID uint64, knowledgeIDs []string,
) (map[string]string, error) {
	cleaned := types.NormalizeKnowledgeIDs(knowledgeIDs)
	authorship := make(map[string]string, len(cleaned))
	if len(cleaned) == 0 {
		return authorship, nil
	}

	// 1. 最先发布的版本是谁发的（上传/正式发布的路径）。
	type versionAuthorRow struct {
		KnowledgeID string
		PublishedBy string
	}
	var versionRows []versionAuthorRow
	err := r.db.WithContext(ctx).Table("knowledge_versions AS v").
		Select("v.knowledge_id, v.published_by").
		Where("v.tenant_id = ? AND v.knowledge_id IN ?", tenantID, cleaned).
		Where("v.published_by <> ''").
		Where("v.version = (SELECT MIN(v2.version) FROM knowledge_versions v2 " +
			"WHERE v2.knowledge_id = v.knowledge_id AND v2.tenant_id = v.tenant_id)").
		Scan(&versionRows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range versionRows {
		authorship[row.KnowledgeID] = row.PublishedBy
	}

	// 2. 从 §10 经验候选卡发布出来的文档，作者是提交人（RequestedBy）。
	//    候选路径优先于版本路径：卡片上明确写了"谁提交的这条经验"，而
	//    发布版本的操作人可能只是走审核流程的管理员。
	type candidateRow struct {
		KnowledgeID string
		RequestedBy string
	}
	var candidateRows []candidateRow
	err = r.db.WithContext(ctx).Table("knowledge_candidates").
		Select("knowledge_id, requested_by").
		Where("tenant_id = ? AND knowledge_id IN ?", tenantID, cleaned).
		Where("requested_by <> ''").
		Scan(&candidateRows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range candidateRows {
		authorship[row.KnowledgeID] = row.RequestedBy
	}
	return authorship, nil
}

// ExpertAnswerCounters aggregates community-answer signals per answerer.
//
// 只统计挂在给定文档下的问题（community_questions.knowledge_id）：专家是
// "对这个领域懂"，全站答案数会把一个只回答人事问题的人推成设备专家。
func (r *graphAnalysisRepository) ExpertAnswerCounters(
	ctx context.Context, tenantID uint64, knowledgeIDs []string, since time.Time,
) ([]types.ExpertAnswerCounters, error) {
	cleaned := types.NormalizeKnowledgeIDs(knowledgeIDs)
	if len(cleaned) == 0 {
		return nil, nil
	}
	var counters []types.ExpertAnswerCounters
	// accepted 在 Postgres 是 boolean、SQLite 是整数，用 CASE WHEN a.accepted
	// 在两边都成立（不是 "= TRUE" 这种方言写法）。
	err := r.db.WithContext(ctx).Table("community_answers AS a").
		Select(`a.answered_by AS user_id,
		        SUM(CASE WHEN a.accepted THEN 1 ELSE 0 END) AS accepted_answers,
		        COALESCE(SUM(a.votes), 0) AS useful_ratings`).
		Joins("JOIN community_questions AS q ON q.id = a.question_id AND q.tenant_id = a.tenant_id").
		Where("a.tenant_id = ? AND q.knowledge_id IN ?", tenantID, cleaned).
		Where("a.answered_by <> '' AND a.created_at >= ?", since.UTC()).
		Group("a.answered_by").
		Scan(&counters).Error
	if err != nil {
		return nil, err
	}
	return counters, nil
}

// ExpertCitationCounts counts how often each document was cited in answers.
//
// 引用清单是 JSONB（SQLite 侧是 TEXT），跨方言解析 JSON 数组没有统一写法，
// 所以在 Go 侧解析：取窗口内最新的 limit 条问答，解码 citations，再按知识
// ID 累加。上限是刻意的——引用统计是专家榜的一个加权项，不值得为它扫全表；
// 取最新而非随机，是因为近期引用更能反映"现在谁最懂"。
func (r *graphAnalysisRepository) ExpertCitationCounts(
	ctx context.Context, tenantID uint64, knowledgeIDs []string, since time.Time, limit int,
) (map[string]int, error) {
	cleaned := types.NormalizeKnowledgeIDs(knowledgeIDs)
	counts := make(map[string]int, len(cleaned))
	if len(cleaned) == 0 {
		return counts, nil
	}
	if limit <= 0 || limit > 20000 {
		limit = 5000
	}
	allowed := make(map[string]struct{}, len(cleaned))
	for _, id := range cleaned {
		allowed[id] = struct{}{}
	}
	type citationRow struct {
		Citations string
	}
	var rows []citationRow
	err := r.db.WithContext(ctx).Table("evidence_packs").
		Select("CAST(citations AS TEXT) AS citations").
		Where("tenant_id = ? AND created_at >= ?", tenantID, since.UTC()).
		Where("refusal = ''").
		Order("created_at DESC").
		Limit(limit).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		raw := strings.TrimSpace(row.Citations)
		if raw == "" || raw == "[]" || raw == "null" {
			continue
		}
		var citations []types.EvidenceCitation
		if err := json.Unmarshal([]byte(raw), &citations); err != nil {
			// 一行坏 JSON 不该让整份报告失败：跳过它，其余照常统计。
			continue
		}
		// 同一条回答里对同一篇文档的多次引用只算一次：否则一段长答案把
		// 同一篇知识标了十次，就把它推成了"最常被引用"。
		seen := make(map[string]struct{}, len(citations))
		for _, citation := range citations {
			if citation.KnowledgeID == "" {
				continue
			}
			if _, ok := allowed[citation.KnowledgeID]; !ok {
				continue
			}
			if _, duplicate := seen[citation.KnowledgeID]; duplicate {
				continue
			}
			seen[citation.KnowledgeID] = struct{}{}
			counts[citation.KnowledgeID]++
		}
	}
	return counts, nil
}
