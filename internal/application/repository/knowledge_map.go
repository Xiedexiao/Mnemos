package repository

import (
	"context"

	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// knowledgeMapRepository implements interfaces.KnowledgeMapRepository
// (KnowAxis V3.0 §4.6 知识地图).
type knowledgeMapRepository struct {
	db *gorm.DB
}

// NewKnowledgeMapRepository constructs the repository.
func NewKnowledgeMapRepository(db *gorm.DB) interfaces.KnowledgeMapRepository {
	return &knowledgeMapRepository{db: db}
}

// mapNodeColumns is the projection of one map node.
const mapNodeColumns = "k.id AS knowledge_id, k.knowledge_base_id, k.title, k.folder_path, " +
	"COALESCE(NULLIF(k.lifecycle_status, ''), 'valid') AS lifecycle_status, " +
	"k.classification, k.updated_at"

// mapNodeGroupBy is the GROUP BY counterpart of mapNodeColumns.
//
// 不能用 mapNodeColumns 直接 GROUP BY：那串投影里带 `AS` 别名，SQLite 会报
// "near AS: syntax error"，Postgres 也不接受把别名当分组表达式。两处必须
// 各自维护同一组列，改动时一起改。
const mapNodeGroupBy = "k.id, k.knowledge_base_id, k.title, k.folder_path, " +
	"k.lifecycle_status, k.classification, k.updated_at"

// mapableKnowledge restricts to rows a map may show: not deleted and in the
// searchable lifecycle set (same rule as recommendations and search).
func mapableKnowledge(query *gorm.DB) *gorm.DB {
	return query.Where("k.deleted_at IS NULL").
		Where("COALESCE(NULLIF(k.lifecycle_status, ''), 'valid') IN ?",
			[]string{
				string(types.KnowledgeLifecycleValid),
				string(types.KnowledgeLifecyclePendingReviewCycle),
				string(types.KnowledgeLifecycleStale),
			})
}

// MapNodes returns the space's documents with their map fields.
func (r *knowledgeMapRepository) MapNodes(
	ctx context.Context, tenantID uint64, knowledgeBaseID string, limit int,
) ([]types.KnowledgeMapNode, error) {
	if limit <= 0 || limit > types.MaxKnowledgeMapNodes {
		limit = types.MaxKnowledgeMapNodes
	}
	var nodes []types.KnowledgeMapNode
	query := r.db.WithContext(ctx).Table("knowledges AS k").
		Select(mapNodeColumns).
		Where("k.tenant_id = ? AND k.knowledge_base_id = ?", tenantID, knowledgeBaseID)
	if err := mapableKnowledge(query).
		Order("k.folder_path ASC, k.title ASC, k.id ASC").
		Limit(limit).
		Find(&nodes).Error; err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nodes, nil
	}
	if err := r.attachTags(ctx, tenantID, nodes); err != nil {
		return nil, err
	}
	return nodes, nil
}

// RoleMapNodes returns documents carrying any of the role tags.
func (r *knowledgeMapRepository) RoleMapNodes(
	ctx context.Context, tenantID uint64, tagNames []string, limit int,
) ([]types.KnowledgeMapNode, error) {
	cleaned := types.NormalizeTagNames(tagNames)
	if len(cleaned) == 0 {
		return nil, nil
	}
	if limit <= 0 || limit > types.MaxKnowledgeMapNodes {
		limit = types.MaxKnowledgeMapNodes
	}
	lowered := make([]string, 0, len(cleaned))
	for _, tag := range cleaned {
		lowered = append(lowered, tag)
	}
	var nodes []types.KnowledgeMapNode
	query := r.db.WithContext(ctx).Table("knowledges AS k").
		Select(mapNodeColumns).
		Joins("JOIN knowledge_tag_relations r ON r.knowledge_id = k.id").
		Joins("JOIN knowledge_tags t ON t.id = r.tag_id").
		Where("k.tenant_id = ? AND t.tenant_id = ?", tenantID, tenantID).
		Where("LOWER(t.name) IN ?", lowered)
	if err := mapableKnowledge(query).Group(mapNodeGroupBy).
		Order("k.folder_path ASC, k.title ASC").
		Limit(limit).
		Find(&nodes).Error; err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nodes, nil
	}
	if err := r.attachTags(ctx, tenantID, nodes); err != nil {
		return nil, err
	}
	return nodes, nil
}

// attachTags fills each node's tag list in one query.
func (r *knowledgeMapRepository) attachTags(
	ctx context.Context, tenantID uint64, nodes []types.KnowledgeMapNode,
) error {
	ids := make([]string, 0, len(nodes))
	for _, node := range nodes {
		ids = append(ids, node.KnowledgeID)
	}
	type tagRow struct {
		KnowledgeID string
		Name        string
	}
	var rows []tagRow
	if err := r.db.WithContext(ctx).Table("knowledge_tag_relations AS r").
		Select("r.knowledge_id, t.name").
		Joins("JOIN knowledge_tags t ON t.id = r.tag_id").
		Where("t.tenant_id = ? AND r.knowledge_id IN ?", tenantID, ids).
		Order("t.name ASC").
		Scan(&rows).Error; err != nil {
		return err
	}
	byKnowledge := make(map[string][]string, len(rows))
	for _, row := range rows {
		byKnowledge[row.KnowledgeID] = append(byKnowledge[row.KnowledgeID], row.Name)
	}
	for index := range nodes {
		nodes[index].Tags = byKnowledge[nodes[index].KnowledgeID]
	}
	return nil
}

// MapEdges returns the relation edges whose both ends are in the given set.
//
// 只保留 forward 行：反向镜像是同一条边的另一份存储，全部返回会让每条边
// 在地图上出现两次。两端都在集合内是硬条件——一条指向不可见文档的边本身
// 就泄露了那份文档存在。
func (r *knowledgeMapRepository) MapEdges(
	ctx context.Context, tenantID uint64, knowledgeIDs []string,
) ([]types.KnowledgeMapEdge, error) {
	cleaned := types.NormalizeKnowledgeIDs(knowledgeIDs)
	if len(cleaned) == 0 {
		return nil, nil
	}
	// 节点上限 1000，SQLite 对 IN 的个数没有硬限制，但把 id 集合直接展开
	// 成两次 IN 比 JOIN 更易读，也能走 (tenant, from) / (tenant, to) 索引。
	var edges []types.KnowledgeMapEdge
	err := r.db.WithContext(ctx).Table("knowledge_relations").
		Select("from_knowledge_id AS \"from\", to_knowledge_id AS \"to\", relation_type, origin").
		Where("tenant_id = ?", tenantID).
		Where("direction = ?", "forward").
		Where("from_knowledge_id IN ? AND to_knowledge_id IN ?", cleaned, cleaned).
		Order("from_knowledge_id ASC, to_knowledge_id ASC").
		Scan(&edges).Error
	if err != nil {
		return nil, err
	}
	return edges, nil
}

// CountSpaceKnowledge counts the space's documents.
func (r *knowledgeMapRepository) CountSpaceKnowledge(
	ctx context.Context, tenantID uint64, knowledgeBaseID string,
) (int64, error) {
	var total int64
	query := r.db.WithContext(ctx).Table("knowledges AS k").
		Where("k.tenant_id = ? AND k.knowledge_base_id = ?", tenantID, knowledgeBaseID)
	if err := mapableKnowledge(query).Count(&total).Error; err != nil {
		return 0, err
	}
	return total, nil
}
