package interfaces

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// GraphFactRepository 图谱事实层（Postgres system of record）的存储原语，
// 对应迁移 000151 的四张表：graph_entities / graph_facts / graph_commits /
// chunk_extraction_cache。Neo4j 既有投影与 graph_search 不在本接口范围
// （可重投影层后续从本层同步）。
type GraphFactRepository interface {
	// ---- 实体 ----

	// UpsertEntities 批量写入实体。归一规则：按行身份
	// (tenant_id, entity_type, normalized_key) 查找——未命中插入（计入
	// added）；命中则合并 attributes（键级覆盖）、置信度就高不就低
	// （AMBIGUOUS 不拉低已对齐行）、superseded 行复活为 active，计入
	// updated。整个批次在一个事务内完成，全成功或全失败。
	UpsertEntities(ctx context.Context, tenantID uint64, entities []*types.GraphEntity) (added int, updated int, err error)
	// GetEntityByID 按租户 + ID 取实体；不存在返回 gorm.ErrRecordNotFound。
	GetEntityByID(ctx context.Context, tenantID uint64, entityID string) (*types.GraphEntity, error)
	// GetEntitiesByIDs 批量取实体（保持调用方给出的顺序，缺失的 ID 不在
	// 返回里）。
	GetEntitiesByIDs(ctx context.Context, tenantID uint64, entityIDs []string) ([]*types.GraphEntity, error)
	// SearchEntities 按 name / normalized_key 模糊匹配（keyword 为空返回全部
	// 过滤结果）。schemaID 为空不过滤 schema；confidenceFilter 为空时由调用
	// 方决定默认值（服务层传 EXTRACTED+INFERRED）。
	SearchEntities(ctx context.Context, tenantID uint64, keyword, schemaID string, confidenceFilter []types.GraphConfidence) ([]*types.GraphEntity, error)

	// ---- 事实 ----

	// UpsertFacts 批量写入事实。同一 (tenant, source, relation, target) 的
	// active 事实命中时合并证据（按 knowledge_id+chunk_id+quote 去重）、置信
	// 度就高、回填 commit_id，不计入 added；否则插入新边。事务内完成。
	UpsertFacts(ctx context.Context, tenantID uint64, facts []*types.GraphFact) (added int, err error)
	// GetFactsByIDs 批量取事实（路径装配用；顺序不保证，调用方自行排序）。
	GetFactsByIDs(ctx context.Context, tenantID uint64, factIDs []string) ([]*types.GraphFact, error)
	// ListActiveFactsFromSources 返回 source 命中给定实体集合的 active 且
	// EXTRACTED/INFERRED 的出边（影响面 BFS 与路径查询的逐层展开都用它）。
	ListActiveFactsFromSources(ctx context.Context, tenantID uint64, sourceEntityIDs []string) ([]*types.GraphFact, error)

	// ---- 路径 ----

	// FindFactIDPaths 用 Postgres WITH RECURSIVE 在 active 且
	// EXTRACTED/INFERRED 的边上找 from→to 的简单路径（不走环，深度 ≤ maxHops），
	// 按跳数升序返回至多 limit 条路径的有序 fact-id 链。非 Postgres 方言
	// （Lite 构建的 SQLite）走仓储内等价的逐层 BFS 回退。
	FindFactIDPaths(ctx context.Context, tenantID uint64, fromEntityID, toEntityID string, maxHops, limit int) ([][]string, error)

	// ---- 变更集 ----

	// ApplyCommit 在单个事务内：upsert 实体（统计 added/updated）→ upsert
	// 事实（统计 added）→ 插入 commit 行（计数落库）。批次行须预先带上
	// commit_id（服务层回填）。任一步失败整体回滚。
	ApplyCommit(ctx context.Context, commit *types.GraphCommit, entities []*types.GraphEntity, facts []*types.GraphFact) (*types.GraphCommit, error)
	// GetCommit 按租户 + ID 取 commit；不存在返回 gorm.ErrRecordNotFound。
	GetCommit(ctx context.Context, tenantID uint64, commitID string) (*types.GraphCommit, error)
	// ListCommits 按创建时间倒序返回 commit（append-only 审计面）。
	ListCommits(ctx context.Context, tenantID uint64, limit int) ([]*types.GraphCommit, error)
	// RollbackCommit 把该 commit 产出的 active 实体/事实批量置 superseded
	//（v1 反向标记而非删除），返回实际标记的行数；commit 行本身不可变。
	// 单事务完成。
	RollbackCommit(ctx context.Context, tenantID uint64, commitID string) (entitiesSuperseded, factsSuperseded int, err error)
	// ListEntitiesByCommit returns the entities a commit produced or last
	// touched (§11.4 可回归: "这次提交改了什么").
	ListEntitiesByCommit(
		ctx context.Context, tenantID uint64, commitID string, limit int,
	) ([]*types.GraphEntity, error)
	// ListFactsByCommit returns the facts a commit produced or last touched.
	ListFactsByCommit(
		ctx context.Context, tenantID uint64, commitID string, limit int,
	) ([]*types.GraphFact, error)
	// LatestCommitAt returns the newest commit created at or before `asOf`,
	// which is the anchor a replay is taken at.
	LatestCommitAt(
		ctx context.Context, tenantID uint64, asOf time.Time,
	) (*types.GraphCommit, error)
	// ListEntitiesAsOf returns entities that existed at `asOf`: created before
	// it and either still active or superseded by a later commit. Rows are
	// returned with their CURRENT status so a replay can say what happened to
	// them since.
	ListEntitiesAsOf(
		ctx context.Context, tenantID uint64, asOf time.Time, schemaID string, limit int,
	) ([]*types.GraphEntity, error)
	// ListFactsAsOf returns the facts that existed at `asOf`.
	ListFactsAsOf(
		ctx context.Context, tenantID uint64, asOf time.Time, limit int,
	) ([]*types.GraphFact, error)

	// ---- 增量抽取缓存（§9.4）----

	// GetExtractionCache 命中判断：chunk 缓存行存在且 content_hash 与
	// schema_version 完全相同才返回缓存；否则返回
	// repository.ErrExtractionCacheMiss（调用方据此决定是否重抽）。
	GetExtractionCache(ctx context.Context, tenantID uint64, chunkID, contentHash string, schemaVersion int) (*types.ChunkExtractionCache, error)
	// LetExtractionCache 写入缓存（UNIQUE(chunk_id)：重复写入即覆盖）。
	LetExtractionCache(ctx context.Context, cache *types.ChunkExtractionCache) error
	// PeekExtractionCache 按主键读取缓存行（管理面查看，不做命中判断）；
	// 不存在返回 gorm.ErrRecordNotFound。
	PeekExtractionCache(ctx context.Context, tenantID uint64, chunkID string) (*types.ChunkExtractionCache, error)
}

// GraphFactService 图谱事实层业务服务。除显式收 tenantID 的方法外，全部
// 要求 tenantID > 0，缺失报 service.ErrGraphTenantMissing；写操作要求
// actorID 非空，缺失报 service.ErrGraphActorMissing。
type GraphFactService interface {
	// UpsertEntities 批量归一写入实体（补 ID/归一键/置信度默认值后落库）。
	UpsertEntities(ctx context.Context, tenantID uint64, entities []*types.GraphEntity, actorID string) (*types.GraphEntityUpsertResult, error)
	// UpsertFacts 批量写入事实（补 ID/默认值；Evidence 为调用方以
	// json.Marshal 序列化好的 JSONB）。抽取管线挂接点（不改 extract.go 的
	// 接线建议见服务注释）。
	UpsertFacts(ctx context.Context, tenantID uint64, facts []*types.GraphFact, actorID string) (*types.GraphFactUpsertResult, error)
	// CreateCommit 提交一批变更：生成 commit id → 回填批次行的 commit_id →
	// 事务内 upsert + 统计计数落 graph_commits。GoldenGatePass 为 nil 按 true。
	CreateCommit(ctx context.Context, tenantID uint64, input *types.GraphCommitInput, actorID string) (*types.GraphCommit, error)
	// RollbackCommit 回滚一个 commit：把其产出的实体/事实置 superseded
	//（反向标记，不删除；commit 行不可变）。
	RollbackCommit(ctx context.Context, tenantID uint64, commitID, actorID string) (*types.GraphRollbackResult, error)
	// ListCommits 按创建时间倒序返回 commit（limit 由服务层钳制）。
	ListCommits(ctx context.Context, tenantID uint64, limit int) ([]*types.GraphCommit, error)
	// DiffCommit 返回一个 commit 实际改动的实体与事实（§11.4 可回归）。
	// 计数不是内容："加了 12 条实体"回答不了"哪 12 条"。
	DiffCommit(ctx context.Context, tenantID uint64, commitID string) (*types.GraphCommitDiff, error)
	// Replay 重建 `asOf` 时刻的图谱快照（§11.4 可重放）。只读：不修改任何
	// 数据，也不需要重放写入。
	Replay(
		ctx context.Context, tenantID uint64, request types.GraphReplayRequest,
	) (*types.GraphReplayResult, error)

	// SearchEntities 实体检索：name/normalized_key 模糊；schemaID 为空不过滤；
	// confidenceFilter 为空时默认 EXTRACTED+INFERRED（AMBIGUOUS 默认不可见）。
	SearchEntities(ctx context.Context, tenantID uint64, keyword, schemaID string, confidenceFilter []types.GraphConfidence) ([]*types.GraphEntity, error)
	// PathQuery 路径查询 v1：递归 SQL 走 graph_facts 的 active 边，
	// maxHops 钳制 1..3（0/缺省=3），只走 EXTRACTED+INFERRED 边，返回实体 +
	// 关系 + 置信度。任一端实体不存在报 service.ErrGraphEntityNotFound。
	PathQuery(ctx context.Context, tenantID uint64, fromEntityID, toEntityID string, maxHops int) ([]*types.GraphFactPath, error)
	// ImpactQuery 影响面查询：从实体沿 outgoing active 边（EXTRACTED+INFERRED）
	// BFS 两层，返回受影响 facts 与被波及实体。根实体不存在报
	// service.ErrGraphEntityNotFound。
	ImpactQuery(ctx context.Context, tenantID uint64, entityID string) (*types.GraphImpactResult, error)

	// GetExtractionCache 增量抽取缓存命中判断（hash+schema_version 相同才
	// 命中）；未命中报 service.ErrGraphCacheMiss。
	GetExtractionCache(ctx context.Context, tenantID uint64, chunkID, contentHash string, schemaVersion int) (*types.ChunkExtractionCache, error)
	// LetExtractionCache 写入/覆盖缓存。
	LetExtractionCache(ctx context.Context, tenantID uint64, chunkID, contentHash string, schemaVersion int, extraction types.JSON) (*types.ChunkExtractionCache, error)
	// PeekExtractionCache 按主键读缓存行（管理面）；不存在报
	// service.ErrGraphCacheMiss。
	PeekExtractionCache(ctx context.Context, tenantID uint64, chunkID string) (*types.ChunkExtractionCache, error)
}
