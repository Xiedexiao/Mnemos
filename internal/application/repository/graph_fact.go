package repository

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ErrExtractionCacheMiss 增量抽取缓存未命中（chunk 无缓存行，或
// content_hash / schema_version 与请求不一致——内容改了或本体换版了，
// 调用方据此决定是否重新抽取）。
var ErrExtractionCacheMiss = errors.New("graph fact: extraction cache miss")

const (
	// graphEntitySearchLimit 是 SearchEntities 的硬上限（v1 无分页，防止
	// 全表拖出）。
	graphEntitySearchLimit = 200
	// graphCommitListDefault / graphCommitListLimit 是 ListCommits 的默认与
	// 硬上限。
	graphCommitListDefault = 50
	graphCommitListLimit   = 500
)

// graphFactRepository implements interfaces.GraphFactRepository on the four
// tables created by migration 000151. 批量写入都在单事务内完成（全成功或
// 全失败），使统计计数与数据行保持一致。
type graphFactRepository struct {
	db *gorm.DB
}

// NewGraphFactRepository constructs the graph fact repository.
func NewGraphFactRepository(db *gorm.DB) interfaces.GraphFactRepository {
	return &graphFactRepository{db: db}
}

// ============================================================================
// 实体
// ============================================================================

// UpsertEntities 批量归一写入实体（单事务）。归一规则：行身份
// (tenant_id, entity_type, normalized_key) 命中 → attributes 键级合并、
// 置信度就高不就低、superseded 行复活；未命中 → 插入。
func (r *graphFactRepository) UpsertEntities(
	ctx context.Context, tenantID uint64, entities []*types.GraphEntity,
) (added int, updated int, err error) {
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		added, updated, err = r.upsertEntitiesTx(tx, tenantID, entities)
		return err
	})
	return added, updated, err
}

// upsertEntitiesTx 在给定事务内逐行 upsert 实体，并把落库结果（含分配的
// ID / 合并后的 attributes）回写到调用方的行上。
func (r *graphFactRepository) upsertEntitiesTx(
	tx *gorm.DB, tenantID uint64, entities []*types.GraphEntity,
) (added int, updated int, err error) {
	now := time.Now()
	for _, entity := range entities {
		if entity == nil {
			continue
		}
		if entity.NormalizedKey == "" {
			entity.NormalizedKey = graphNormalizedKey(entity.Name)
		}
		var existing types.GraphEntity
		err := tx.Where("tenant_id = ? AND entity_type = ? AND normalized_key = ?",
			tenantID, entity.EntityType, entity.NormalizedKey).
			First(&existing).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			if entity.ID == "" {
				entity.ID = uuid.NewString()
			}
			entity.TenantID = tenantID
			if !entity.Confidence.Valid() {
				entity.Confidence = types.GraphConfidenceExtracted
			}
			if entity.Status == "" {
				entity.Status = types.GraphEntityStatusActive
			}
			if len(entity.Attributes) == 0 {
				entity.Attributes = types.JSON("{}")
			}
			entity.CreatedAt = now
			entity.UpdatedAt = now
			if err := tx.Create(entity).Error; err != nil {
				return 0, 0, err
			}
			added++
		case err != nil:
			return 0, 0, err
		default:
			// 归一命中：更新而非新增。canonical name 保留先到者（归一键相同
			// 的大小写变体不互相覆盖）；attributes 键级合并（新值覆盖旧值）。
			existing.Attributes = mergeGraphAttributes(existing.Attributes, entity.Attributes)
			if entity.Confidence.Valid() &&
				entity.Confidence.Rank() > existing.Confidence.Rank() {
				// 就高不就低：AMBIGUOUS 的新数据不得拉低已对齐证据的行。
				existing.Confidence = entity.Confidence
			}
			if existing.Status == types.GraphEntityStatusSuperseded {
				// 复活：被回滚废止的实体被新数据重新主张，随本批 commit 重新生效。
				existing.Status = types.GraphEntityStatusActive
			}
			if existing.SchemaID == "" && entity.SchemaID != "" {
				existing.SchemaID = entity.SchemaID
			}
			if existing.SourceKnowledgeID == "" && entity.SourceKnowledgeID != "" {
				existing.SourceKnowledgeID = entity.SourceKnowledgeID
			}
			if entity.CommitID != "" {
				// commit_id = 最后一次产出/更新该行的 commit（回滚按它反向标记）。
				existing.CommitID = entity.CommitID
			}
			existing.UpdatedAt = now
			if err := tx.Save(&existing).Error; err != nil {
				return 0, 0, err
			}
			*entity = existing
			updated++
		}
	}
	return added, updated, nil
}

// GetEntityByID 按租户 + ID 取实体；不存在返回 gorm.ErrRecordNotFound。
func (r *graphFactRepository) GetEntityByID(
	ctx context.Context, tenantID uint64, entityID string,
) (*types.GraphEntity, error) {
	var entity types.GraphEntity
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, entityID).
		First(&entity).Error; err != nil {
		return nil, err
	}
	return &entity, nil
}

// GetEntitiesByIDs 批量取实体，按调用方给出的顺序返回（缺失 ID 不在结果里）。
func (r *graphFactRepository) GetEntitiesByIDs(
	ctx context.Context, tenantID uint64, entityIDs []string,
) ([]*types.GraphEntity, error) {
	if len(entityIDs) == 0 {
		return nil, nil
	}
	var rows []*types.GraphEntity
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id IN ?", tenantID, entityIDs).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	byID := make(map[string]*types.GraphEntity, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}
	ordered := make([]*types.GraphEntity, 0, len(entityIDs))
	for _, id := range entityIDs {
		if row, ok := byID[id]; ok {
			ordered = append(ordered, row)
		}
	}
	return ordered, nil
}

// SearchEntities 按 name / normalized_key 模糊匹配，只返回 active 行
// （superseded 是回滚废止的历史，不进检索面）。ILIKE 是 Postgres 专属，
// SQLite（Lite 构建）镜像为 LOWER() 两侧比较；ESCAPE ? 与 escapeLikeKeyword
// 配对，保证 \% \_ 按字面匹配。
func (r *graphFactRepository) SearchEntities(
	ctx context.Context, tenantID uint64, keyword, schemaID string, confidenceFilter []types.GraphConfidence,
) ([]*types.GraphEntity, error) {
	query := r.db.WithContext(ctx).
		Model(&types.GraphEntity{}).
		Where("tenant_id = ?", tenantID).
		Where("status = ?", string(types.GraphEntityStatusActive))
	if keyword = strings.TrimSpace(keyword); keyword != "" {
		like := "%" + escapeLikeKeyword(keyword) + "%"
		if r.db.Name() == "postgres" {
			query = query.Where(
				"name ILIKE ? ESCAPE ? OR normalized_key ILIKE ? ESCAPE ?",
				like, likeEscapeChar, like, likeEscapeChar,
			)
		} else {
			query = query.Where(
				"LOWER(name) LIKE LOWER(?) ESCAPE ? OR LOWER(normalized_key) LIKE LOWER(?) ESCAPE ?",
				like, likeEscapeChar, like, likeEscapeChar,
			)
		}
	}
	if schemaID != "" {
		query = query.Where("schema_id = ?", schemaID)
	}
	if len(confidenceFilter) > 0 {
		confidences := make([]string, 0, len(confidenceFilter))
		for _, confidence := range confidenceFilter {
			confidences = append(confidences, string(confidence))
		}
		query = query.Where("confidence IN ?", confidences)
	}
	var entities []*types.GraphEntity
	err := query.Order("updated_at DESC").
		Limit(graphEntitySearchLimit).
		Find(&entities).Error
	return entities, err
}

// ============================================================================
// 事实
// ============================================================================

// UpsertFacts 批量写入事实（单事务）。同一 (tenant, source, relation,
// target) 的 active 事实命中时合并证据、置信度就高、回填 commit_id，
// 不计入 added；否则插入新边。
func (r *graphFactRepository) UpsertFacts(
	ctx context.Context, tenantID uint64, facts []*types.GraphFact,
) (added int, err error) {
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		added, err = r.upsertFactsTx(tx, tenantID, facts)
		return err
	})
	return added, err
}

// upsertFactsTx 在给定事务内逐行 upsert 事实，并把落库结果回写调用方。
func (r *graphFactRepository) upsertFactsTx(
	tx *gorm.DB, tenantID uint64, facts []*types.GraphFact,
) (added int, err error) {
	now := time.Now()
	for _, fact := range facts {
		if fact == nil {
			continue
		}
		var existing types.GraphFact
		err := tx.Where(
			"tenant_id = ? AND source_entity_id = ? AND relation_name = ? AND target_entity_id = ? AND status = ?",
			tenantID, fact.SourceEntityID, fact.RelationName, fact.TargetEntityID,
			string(types.GraphFactStatusActive),
		).Order("created_at ASC").First(&existing).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			if fact.ID == "" {
				fact.ID = uuid.NewString()
			}
			fact.TenantID = tenantID
			if !fact.Confidence.Valid() {
				fact.Confidence = types.GraphConfidenceExtracted
			}
			if fact.Status == "" {
				fact.Status = types.GraphFactStatusActive
			}
			if len(fact.Evidence) == 0 {
				fact.Evidence = types.JSON("[]")
			}
			fact.CreatedAt = now
			fact.UpdatedAt = now
			if err := tx.Create(fact).Error; err != nil {
				return 0, err
			}
			added++
		case err != nil:
			return 0, err
		default:
			// 重复陈述：合并进既有 active 边，证据去重累积（多来源佐证让
			// 同一条事实更可信），不产生重复行。
			existing.Evidence = mergeGraphEvidence(existing.Evidence, fact.Evidence)
			if fact.Confidence.Valid() &&
				fact.Confidence.Rank() > existing.Confidence.Rank() {
				existing.Confidence = fact.Confidence
			}
			if existing.SchemaID == "" && fact.SchemaID != "" {
				existing.SchemaID = fact.SchemaID
			}
			if fact.CommitID != "" {
				existing.CommitID = fact.CommitID
			}
			existing.UpdatedAt = now
			if err := tx.Save(&existing).Error; err != nil {
				return 0, err
			}
			*fact = existing
		}
	}
	return added, nil
}

// GetFactsByIDs 批量取事实（路径装配用；顺序不保证）。
func (r *graphFactRepository) GetFactsByIDs(
	ctx context.Context, tenantID uint64, factIDs []string,
) ([]*types.GraphFact, error) {
	if len(factIDs) == 0 {
		return nil, nil
	}
	var facts []*types.GraphFact
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id IN ?", tenantID, factIDs).
		Find(&facts).Error
	return facts, err
}

// ListActiveFactsFromSources 返回 source 命中给定实体集合的 active 且
// EXTRACTED/INFERRED 的出边。影响面 BFS 与路径查询的逐层展开共用本方法，
// 置信度过滤是"有效图"定义的一部分（AMBIGUOUS 永不参与遍历）。
func (r *graphFactRepository) ListActiveFactsFromSources(
	ctx context.Context, tenantID uint64, sourceEntityIDs []string,
) ([]*types.GraphFact, error) {
	if len(sourceEntityIDs) == 0 {
		return nil, nil
	}
	confidences := []string{
		string(types.GraphConfidenceExtracted), string(types.GraphConfidenceInferred),
	}
	var facts []*types.GraphFact
	err := r.db.WithContext(ctx).
		Where("tenant_id = ?", tenantID).
		Where("status = ?", string(types.GraphFactStatusActive)).
		Where("confidence IN ?", confidences).
		Where("source_entity_id IN ?", sourceEntityIDs).
		Find(&facts).Error
	return facts, err
}

// ============================================================================
// 路径查询
// ============================================================================

// FindFactIDPaths 找 from→to 的简单路径（不走环），按跳数升序返回至多
// limit 条路径的有序 fact-id 链。只走 active 且 EXTRACTED/INFERRED 的边。
func (r *graphFactRepository) FindFactIDPaths(
	ctx context.Context, tenantID uint64, fromEntityID, toEntityID string, maxHops, limit int,
) ([][]string, error) {
	if fromEntityID == toEntityID {
		// 零跳自环不是 v1 语义（路径 = 至少一条边）。
		return nil, nil
	}
	if r.db.Name() == "postgres" {
		return r.findFactIDPathsRecursive(ctx, tenantID, fromEntityID, toEntityID, maxHops, limit)
	}
	// SQLite（Lite 构建）没有数组类型，走等价的逐层 BFS 回退。
	return r.findFactIDPathsBFS(ctx, tenantID, fromEntityID, toEntityID, maxHops, limit)
}

// findFactIDPathsRecursive 用 Postgres WITH RECURSIVE 做有界深度遍历：
// anchor = from 实体的出边；recursive 成员沿 active 边扩展，node_path 防
// 环（v1 maxHops ≤ 3，展开规模可控）。
func (r *graphFactRepository) findFactIDPathsRecursive(
	ctx context.Context, tenantID uint64, fromEntityID, toEntityID string, maxHops, limit int,
) ([][]string, error) {
	var rows []struct {
		FactIDs string `gorm:"column:fact_ids"`
		Depth   int    `gorm:"column:depth"`
	}
	// array_to_string 把 uuid 数组拍平成逗号串（UUID 无逗号，安全拆分），
	// 规避驱动侧数组类型扫描差异。
	const walkSQL = `
WITH RECURSIVE walk AS (
    SELECT
        f.id AS fact_id,
        f.target_entity_id AS node,
        1 AS depth,
        ARRAY[f.id]::varchar[] AS fact_path,
        ARRAY[f.source_entity_id, f.target_entity_id]::varchar[] AS node_path
    FROM graph_facts f
    WHERE f.tenant_id = ?
      AND f.source_entity_id = ?
      AND f.status = 'active'
      AND f.confidence IN ('EXTRACTED', 'INFERRED')
  UNION ALL
    SELECT
        f.id,
        f.target_entity_id,
        w.depth + 1,
        w.fact_path || f.id,
        w.node_path || f.target_entity_id
    FROM walk w
    JOIN graph_facts f
      ON f.tenant_id = ?
     AND f.source_entity_id = w.node
     AND f.status = 'active'
     AND f.confidence IN ('EXTRACTED', 'INFERRED')
    WHERE w.depth < ?
      AND NOT (w.node_path @> ARRAY[f.target_entity_id])
)
SELECT array_to_string(fact_path, ',') AS fact_ids, depth
FROM walk
WHERE node = ?
ORDER BY depth, fact_ids
LIMIT ?`
	if err := r.db.WithContext(ctx).Raw(walkSQL,
		tenantID, fromEntityID, tenantID, maxHops, toEntityID, limit,
	).Scan(&rows).Error; err != nil {
		return nil, err
	}
	paths := make([][]string, 0, len(rows))
	for _, row := range rows {
		if row.FactIDs == "" {
			continue
		}
		paths = append(paths, strings.Split(row.FactIDs, ","))
	}
	return paths, nil
}

// findFactIDPathsBFS 是非 Postgres 方言的等价实现：逐层取
// ListActiveFactsFromSources 出边扩展路径，语义与递归 SQL 一致
// （同跳数优先、不走环、置信度过滤相同）。
func (r *graphFactRepository) findFactIDPathsBFS(
	ctx context.Context, tenantID uint64, fromEntityID, toEntityID string, maxHops, limit int,
) ([][]string, error) {
	type walkState struct {
		factIDs []string
		nodes   []string
	}
	frontier := []walkState{{nodes: []string{fromEntityID}}}
	results := make([][]string, 0, limit)
	for depth := 1; depth <= maxHops && len(frontier) > 0 && len(results) < limit; depth++ {
		sources := make([]string, 0, len(frontier))
		for _, state := range frontier {
			sources = append(sources, state.nodes[len(state.nodes)-1])
		}
		edges, err := r.ListActiveFactsFromSources(ctx, tenantID, sources)
		if err != nil {
			return nil, err
		}
		bySource := make(map[string][]*types.GraphFact, len(edges))
		for _, edge := range edges {
			bySource[edge.SourceEntityID] = append(bySource[edge.SourceEntityID], edge)
		}
		next := make([]walkState, 0, len(frontier))
		for _, state := range frontier {
			tail := state.nodes[len(state.nodes)-1]
			for _, edge := range bySource[tail] {
				if slices.Contains(state.nodes, edge.TargetEntityID) {
					continue // 不走环
				}
				factIDs := append(slices.Clone(state.factIDs), edge.ID)
				if edge.TargetEntityID == toEntityID {
					results = append(results, factIDs)
					if len(results) >= limit {
						return results, nil
					}
					continue
				}
				next = append(next, walkState{
					factIDs: factIDs,
					nodes:   append(slices.Clone(state.nodes), edge.TargetEntityID),
				})
			}
		}
		frontier = next
	}
	return results, nil
}

// ============================================================================
// 变更集（graph_commits，append-only）
// ============================================================================

// ApplyCommit 在单个事务内应用一批变更：upsert 实体 → upsert 事实 → 插入
// commit 行（统计计数与数据行同事务落库）。批次行应预先带上 commit_id
// （正常由服务层回填）；未带的行由本方法认领为本 commit。
func (r *graphFactRepository) ApplyCommit(
	ctx context.Context, commit *types.GraphCommit, entities []*types.GraphEntity, facts []*types.GraphFact,
) (*types.GraphCommit, error) {
	if commit.ID == "" {
		commit.ID = uuid.NewString()
	}
	// 行归属兜底：未带 commit_id 的批次行认领为本 commit（正常流程由服务层
	// 在生成 commit id 后回填，这里保证直接调用仓储也不会出现无主行）。
	for _, entity := range entities {
		if entity != nil && entity.CommitID == "" {
			entity.CommitID = commit.ID
		}
	}
	for _, fact := range facts {
		if fact != nil && fact.CommitID == "" {
			fact.CommitID = commit.ID
		}
	}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		entitiesAdded, entitiesUpdated, err := r.upsertEntitiesTx(tx, commit.TenantID, entities)
		if err != nil {
			return err
		}
		factsAdded, err := r.upsertFactsTx(tx, commit.TenantID, facts)
		if err != nil {
			return err
		}
		commit.EntitiesAdded = entitiesAdded
		commit.EntitiesUpdated = entitiesUpdated
		commit.FactsAdded = factsAdded
		commit.FactsSuperseded = 0
		commit.CreatedAt = time.Now()
		return tx.Create(commit).Error
	})
	if err != nil {
		return nil, err
	}
	return commit, nil
}

// GetCommit 按租户 + ID 取 commit；不存在返回 gorm.ErrRecordNotFound。
func (r *graphFactRepository) GetCommit(
	ctx context.Context, tenantID uint64, commitID string,
) (*types.GraphCommit, error) {
	var commit types.GraphCommit
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, commitID).
		First(&commit).Error; err != nil {
		return nil, err
	}
	return &commit, nil
}

// ListCommits 按创建时间倒序返回 commit（append-only 审计面）。
func (r *graphFactRepository) ListCommits(
	ctx context.Context, tenantID uint64, limit int,
) ([]*types.GraphCommit, error) {
	if limit <= 0 {
		limit = graphCommitListDefault
	}
	if limit > graphCommitListLimit {
		limit = graphCommitListLimit
	}
	var commits []*types.GraphCommit
	err := r.db.WithContext(ctx).
		Where("tenant_id = ?", tenantID).
		Order("created_at DESC").
		Limit(limit).
		Find(&commits).Error
	return commits, err
}

// RollbackCommit 把该 commit 产出的 active 实体/事实批量置 superseded
// （v1 反向标记而非删除），单事务完成，返回实际标记的行数。commit 行本身
// 不可变：回滚动作通过本方法的返回值留痕，不改写历史。
// 注意：行按 commit_id 归属——若该行已被更新的 commit 触达（commit_id 被
// 回填为更新的 commit），回滚旧 commit 不会波及它，语义是"撤销该 commit
// 产出、且此后无人再主张的状态"。
func (r *graphFactRepository) RollbackCommit(
	ctx context.Context, tenantID uint64, commitID string,
) (entitiesSuperseded, factsSuperseded int, err error) {
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		entitiesResult := tx.Model(&types.GraphEntity{}).
			Where("tenant_id = ? AND commit_id = ? AND status = ?",
				tenantID, commitID, string(types.GraphEntityStatusActive)).
			Updates(map[string]any{
				"status":     string(types.GraphEntityStatusSuperseded),
				"updated_at": time.Now(),
			})
		if entitiesResult.Error != nil {
			return entitiesResult.Error
		}
		factsResult := tx.Model(&types.GraphFact{}).
			Where("tenant_id = ? AND commit_id = ? AND status = ?",
				tenantID, commitID, string(types.GraphFactStatusActive)).
			Updates(map[string]any{
				"status":     string(types.GraphFactStatusSuperseded),
				"updated_at": time.Now(),
			})
		if factsResult.Error != nil {
			return factsResult.Error
		}
		entitiesSuperseded = int(entitiesResult.RowsAffected)
		factsSuperseded = int(factsResult.RowsAffected)
		return nil
	})
	return entitiesSuperseded, factsSuperseded, err
}

// ============================================================================
// 增量抽取缓存（§9.4：内容没改不重复提取）
// ============================================================================

// GetExtractionCache 命中判断：缓存行存在且 content_hash 与 schema_version
// 完全相同才返回缓存；否则返回 ErrExtractionCacheMiss。
func (r *graphFactRepository) GetExtractionCache(
	ctx context.Context, tenantID uint64, chunkID, contentHash string, schemaVersion int,
) (*types.ChunkExtractionCache, error) {
	var cache types.ChunkExtractionCache
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND chunk_id = ? AND content_hash = ? AND schema_version = ?",
			tenantID, chunkID, contentHash, schemaVersion).
		First(&cache).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrExtractionCacheMiss
		}
		return nil, err
	}
	return &cache, nil
}

// LetExtractionCache 写入/覆盖缓存（UNIQUE(chunk_id)：重复写入即覆盖，
// 缓存行是 chunk 的"当前缓存"，旧值无需保留）。
func (r *graphFactRepository) LetExtractionCache(
	ctx context.Context, cache *types.ChunkExtractionCache,
) error {
	if cache.ID == "" {
		cache.ID = uuid.NewString()
	}
	cache.CreatedAt = time.Now()
	if len(cache.Extraction) == 0 {
		cache.Extraction = types.JSON("{}")
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing types.ChunkExtractionCache
		err := tx.Where("tenant_id = ? AND chunk_id = ?", cache.TenantID, cache.ChunkID).
			First(&existing).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			return tx.Create(cache).Error
		case err != nil:
			return err
		default:
			existing.ContentHash = cache.ContentHash
			existing.SchemaVersion = cache.SchemaVersion
			existing.Extraction = cache.Extraction
			existing.CreatedAt = cache.CreatedAt
			if err := tx.Save(&existing).Error; err != nil {
				return err
			}
			*cache = existing
			return nil
		}
	})
}

// PeekExtractionCache 按主键读取缓存行（管理面查看，不做命中判断）；
// 不存在返回 gorm.ErrRecordNotFound。
func (r *graphFactRepository) PeekExtractionCache(
	ctx context.Context, tenantID uint64, chunkID string,
) (*types.ChunkExtractionCache, error) {
	var cache types.ChunkExtractionCache
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND chunk_id = ?", tenantID, chunkID).
		First(&cache).Error; err != nil {
		return nil, err
	}
	return &cache, nil
}

// ============================================================================
// 内部助手
// ============================================================================

// graphNormalizedKey 生成实体归一键：去首尾空白 + 小写。v1 只做精确归一
// （LightRAG exact key 模式）；去停用词、别名折叠、LLM 近邻判定为后续版本。
func graphNormalizedKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// mergeGraphAttributes 键级合并两个 attributes JSON 对象：patch 里的键覆盖
// base 同名键，其余保留。patch 非对象或为空时返回 base（补 '{}' 兜底）。
func mergeGraphAttributes(base, patch types.JSON) types.JSON {
	if len(base) == 0 {
		base = types.JSON("{}")
	}
	if len(patch) == 0 {
		return base
	}
	merged := map[string]any{}
	if err := json.Unmarshal(base, &merged); err != nil {
		merged = map[string]any{}
	}
	patchMap := map[string]any{}
	if err := json.Unmarshal(patch, &patchMap); err != nil {
		// 非对象 patch（数组/标量）无法键级合并，忽略之。
		return base
	}
	for key, value := range patchMap {
		merged[key] = value
	}
	raw, err := json.Marshal(merged)
	if err != nil {
		return base
	}
	return types.JSON(raw)
}

// mergeGraphEvidence 合并证据数组：按 knowledge_id + chunk_id + quote 去重
// （同来源同引文只留一条），base 在前、新增在后，顺序稳定。
func mergeGraphEvidence(base, incoming types.JSON) types.JSON {
	var merged []types.GraphFactEvidence
	if len(base) > 0 {
		_ = json.Unmarshal(base, &merged)
	}
	var add []types.GraphFactEvidence
	if len(incoming) > 0 {
		if err := json.Unmarshal(incoming, &add); err != nil {
			return base
		}
	}
	if len(add) == 0 {
		if len(merged) == 0 {
			return types.JSON("[]")
		}
		return base
	}
	evidenceKey := func(e types.GraphFactEvidence) string {
		return e.KnowledgeID + "\x00" + e.ChunkID + "\x00" + e.Quote
	}
	seen := make(map[string]struct{}, len(merged)+len(add))
	out := make([]types.GraphFactEvidence, 0, len(merged)+len(add))
	for _, item := range merged {
		key := evidenceKey(item)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	for _, item := range add {
		key := evidenceKey(item)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return base
	}
	return types.JSON(raw)
}

// ============================================================================
// GraphCommit diff 与回放（KnowAxis V3.0 §11.4 可回归 / 可重放）
// ============================================================================

// ListEntitiesByCommit returns the entities a commit produced or last touched.
//
// 按 (tenant, commit_id) 查，走 idx_graph_entities_commit 之外的复合条件；
// 表规模是千级到万级，这里不做额外索引——diff 是管理面操作，不在热路径上。
func (r *graphFactRepository) ListEntitiesByCommit(
	ctx context.Context, tenantID uint64, commitID string, limit int,
) ([]*types.GraphEntity, error) {
	if limit <= 0 || limit > types.GraphDiffMaxLimit {
		limit = types.GraphDiffMaxLimit
	}
	var rows []*types.GraphEntity
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND commit_id = ?", tenantID, commitID).
		Order("id ASC").
		Limit(limit).
		Find(&rows).Error
	return rows, err
}

// ListFactsByCommit returns the facts a commit produced or last touched.
func (r *graphFactRepository) ListFactsByCommit(
	ctx context.Context, tenantID uint64, commitID string, limit int,
) ([]*types.GraphFact, error) {
	if limit <= 0 || limit > types.GraphDiffMaxLimit {
		limit = types.GraphDiffMaxLimit
	}
	var rows []*types.GraphFact
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND commit_id = ?", tenantID, commitID).
		Order("id ASC").
		Limit(limit).
		Find(&rows).Error
	return rows, err
}

// LatestCommitAt returns the newest commit at or before asOf.
func (r *graphFactRepository) LatestCommitAt(
	ctx context.Context, tenantID uint64, asOf time.Time,
) (*types.GraphCommit, error) {
	var commit types.GraphCommit
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND created_at <= ?", tenantID, asOf.UTC()).
		Order("created_at DESC").
		First(&commit).Error
	if err != nil {
		return nil, err
	}
	return &commit, nil
}

// ListEntitiesAsOf returns entities that existed at asOf.
//
// 判定是"created_at <= asOf 且 (仍 active 或 被 asOf 之后才废止)"：
//
//	created_at > asOf        → 当时还不存在
//	updated_at <= asOf 的 superseded → 当时就已废止，不算"存在"
//
// 只按 created_at 过滤会把"当时还没建"的行带进来；只按 status 过滤会把
// "现在已废止但当时还在"的行漏掉——回放的价值恰恰包含后者。
func (r *graphFactRepository) ListEntitiesAsOf(
	ctx context.Context, tenantID uint64, asOf time.Time, schemaID string, limit int,
) ([]*types.GraphEntity, error) {
	if limit <= 0 || limit > types.GraphReplayMaxEntities {
		limit = types.GraphReplayMaxEntities
	}
	query := r.db.WithContext(ctx).
		Where("tenant_id = ? AND created_at <= ?", tenantID, asOf.UTC()).
		Where("NOT (status = ? AND updated_at <= ?)",
			string(types.GraphEntityStatusSuperseded), asOf.UTC())
	if schemaID != "" {
		query = query.Where("schema_id = ?", schemaID)
	}
	var rows []*types.GraphEntity
	err := query.Order("id ASC").Limit(limit).Find(&rows).Error
	return rows, err
}

// ListFactsAsOf returns facts that existed at asOf.
func (r *graphFactRepository) ListFactsAsOf(
	ctx context.Context, tenantID uint64, asOf time.Time, limit int,
) ([]*types.GraphFact, error) {
	if limit <= 0 || limit > types.GraphReplayMaxEntities {
		limit = types.GraphReplayMaxEntities
	}
	var rows []*types.GraphFact
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND created_at <= ?", tenantID, asOf.UTC()).
		Where("NOT (status = ? AND updated_at <= ?)",
			string(types.GraphFactStatusSuperseded), asOf.UTC()).
		Order("id ASC").
		Limit(limit).
		Find(&rows).Error
	return rows, err
}
