package types

import (
	"time"
)

// ============================================================================
// 图谱事实层（B 期图谱增强，调研报告 §3.1-§3.5 的 v1 子集）。
//
// 存储总原则：Postgres 是事实层（system of record），Neo4j 只存可重投影的
// 图结构——既有 Neo4j 投影与 graph_search 不动，本层为新增的权威事实存储。
//
//   graph_entities          实体登记（归一主键 + 三级置信度 + 证据）。
//   graph_facts             事实（S-P-O 边 + 证据绑定 + 三级置信度）。
//   graph_commits           图谱变更集（append-only，统计计数 + 回滚锚点）。
//   chunk_extraction_cache  增量抽取缓存（§9.4：内容没改不重复提取）。
//
// 事实的 entity_type / relation_name 对齐 B1 本体（internal/types/ontology.go
// 的 ontology_types.name / ontology_relations.name）；schema_id 为空表示未
// 对齐本体的遗留事实。证据复用 SourceLocator 语义，不造新定位结构。
// ============================================================================

// GraphConfidence 三级置信度（调研报告 §3.4）：
//   - EXTRACTED：schema 合法 + 证据对齐成功 + 分数达阈；
//   - INFERRED：规则/推导产生（另记 rule_id 于 attributes，v1 不单列）；
//   - AMBIGUOUS：类型越界、实体消歧冲突、多来源矛盾、低分——进人工审核
//     队列。AMBIGUOUS 行照实入库（直接带 confidence 存），但永不进入
//     "有效图"：所有默认查询路径只取 EXTRACTED + INFERRED。
type GraphConfidence string

const (
	// GraphConfidenceExtracted 直接抽取且证据对齐成功。
	GraphConfidenceExtracted GraphConfidence = "EXTRACTED"
	// GraphConfidenceInferred 规则/推导产生。
	GraphConfidenceInferred GraphConfidence = "INFERRED"
	// GraphConfidenceAmbiguous 待人工审核（默认查询不可见）。
	GraphConfidenceAmbiguous GraphConfidence = "AMBIGUOUS"
)

// Valid 报告该置信度是否在白名单内（请求体校验用）。
func (c GraphConfidence) Valid() bool {
	switch c {
	case GraphConfidenceExtracted, GraphConfidenceInferred, GraphConfidenceAmbiguous:
		return true
	default:
		return false
	}
}

// Rank 返回置信度序：AMBIGUOUS(1) < INFERRED(2) < EXTRACTED(3)。合并/upsert
// 用"就高不就低"策略：新的 AMBIGUOUS 数据不得把已对齐证据的行拉低。
func (c GraphConfidence) Rank() int {
	switch c {
	case GraphConfidenceAmbiguous:
		return 1
	case GraphConfidenceInferred:
		return 2
	case GraphConfidenceExtracted:
		return 3
	default:
		return 0
	}
}

// String renders the confidence for logs.
func (c GraphConfidence) String() string { return string(c) }

// GraphConfidenceDefaultFilter 是默认查询的置信度白名单：有效图只包含
// EXTRACTED + INFERRED（AMBIGUOUS 待人工审核，不参与路径/影响/检索）。
var GraphConfidenceDefaultFilter = []GraphConfidence{
	GraphConfidenceExtracted, GraphConfidenceInferred,
}

// GraphEntityStatus 实体行状态：active 当前有效；superseded 已废止
// （回滚反向标记，行保留供历史与审计回放，不物理删除）。
type GraphEntityStatus string

const (
	// GraphEntityStatusActive 当前有效。
	GraphEntityStatusActive GraphEntityStatus = "active"
	// GraphEntityStatusSuperseded 已废止（回滚标记；仍可被后续 upsert 复活）。
	GraphEntityStatusSuperseded GraphEntityStatus = "superseded"
)

// GraphFactStatus 事实行状态，语义同 GraphEntityStatus。
type GraphFactStatus string

const (
	// GraphFactStatusActive 当前有效。
	GraphFactStatusActive GraphFactStatus = "active"
	// GraphFactStatusSuperseded 已废止（回滚标记；时间历史保留）。
	GraphFactStatusSuperseded GraphFactStatus = "superseded"
)

// GraphFactEvidence 是一条证据绑定：指向抽取来源 chunk 及其在原文中的位置。
// locator 复用 SourceLocator（页/区块/字符区间定位），quote 是 LLM 输出的
// 原文片段——先经 sourceloc 对齐再落库（管线挂接建议见 graph_fact 服务）。
type GraphFactEvidence struct {
	// KnowledgeID 证据所在的知识条目。
	KnowledgeID string `json:"knowledge_id"`
	// ChunkID 证据所在的 chunk。
	ChunkID string `json:"chunk_id"`
	// Locator 原文定位（SourceLocator 语义，可为空=只有 chunk 级溯源）。
	Locator *SourceLocator `json:"locator,omitempty"`
	// Quote 原文片段（LLM 输出的支撑引文）。
	Quote string `json:"quote,omitempty"`
}

// GraphEntity 实体登记行（表 graph_entities）。行身份 =
// (tenant_id, entity_type, normalized_key)：归一命中即更新而非新增。
// CommitID 记录最后一次产出/更新该行的 commit（回滚按它反向标记）。
type GraphEntity struct {
	ID         string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID   uint64 `json:"tenant_id" gorm:"type:integer;not null;uniqueIndex:uq_graph_entities_identity,priority:1"`
	SchemaID   string `json:"schema_id" gorm:"type:varchar(36);not null;default:'';index:idx_graph_entities_tenant_schema_confidence,priority:2"`
	EntityType string `json:"entity_type" gorm:"type:varchar(128);not null;uniqueIndex:uq_graph_entities_identity,priority:2"`
	Name       string `json:"name" gorm:"type:varchar(255);not null"`
	// NormalizedKey 归一键：小写化、去空白后的名字（实体归一的精确匹配依据，
	// LightRAG exact key 模式；LLM 判定的近邻归并为后续版本）。
	NormalizedKey string `json:"normalized_key" gorm:"type:varchar(255);not null;uniqueIndex:uq_graph_entities_identity,priority:3"`
	Attributes    JSON   `json:"attributes" gorm:"type:jsonb;not null;default:'{}'"`
	// SourceKnowledgeID 首个来源知识（溯源；多来源合并时先到者保留）。
	SourceKnowledgeID string            `json:"source_knowledge_id" gorm:"type:varchar(36);not null;default:'';index:idx_graph_entities_source_knowledge"`
	Confidence        GraphConfidence   `json:"confidence" gorm:"type:varchar(16);not null;default:'EXTRACTED';index:idx_graph_entities_tenant_schema_confidence,priority:3"`
	Status            GraphEntityStatus `json:"status" gorm:"type:varchar(16);not null;default:'active'"`
	CommitID          string            `json:"commit_id" gorm:"type:varchar(36);not null;default:''"`
	CreatedBy         string            `json:"created_by" gorm:"type:varchar(36);not null;default:''"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
}

// TableName pins the GORM table name.
func (GraphEntity) TableName() string { return "graph_entities" }

// GraphFact 事实行（表 graph_facts）：source --[relation_name]--> target。
// relation_name 必须命中同一 schema 的 ontology_relations.name。同一
// (source, relation, target) 的重复陈述合并进既有 active 行，证据去重累积。
type GraphFact struct {
	ID             string          `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID       uint64          `json:"tenant_id" gorm:"type:integer;not null;index:idx_graph_facts_tenant_relation_confidence,priority:1"`
	SchemaID       string          `json:"schema_id" gorm:"type:varchar(36);not null;default:''"`
	RelationName   string          `json:"relation_name" gorm:"type:varchar(128);not null;index:idx_graph_facts_tenant_relation_confidence,priority:2"`
	SourceEntityID string          `json:"source_entity_id" gorm:"type:varchar(36);not null;index:idx_graph_facts_source_entity"`
	TargetEntityID string          `json:"target_entity_id" gorm:"type:varchar(36);not null;index:idx_graph_facts_target_entity"`
	Confidence     GraphConfidence `json:"confidence" gorm:"type:varchar(16);not null;default:'EXTRACTED';index:idx_graph_facts_tenant_relation_confidence,priority:3"`
	// Evidence 证据数组 [{knowledge_id, chunk_id, locator, quote}]。
	Evidence  JSON            `json:"evidence" gorm:"type:jsonb;not null;default:'[]'"`
	Status    GraphFactStatus `json:"status" gorm:"type:varchar(16);not null;default:'active'"`
	CommitID  string          `json:"commit_id" gorm:"type:varchar(36);not null;default:''"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// TableName pins the GORM table name.
func (GraphFact) TableName() string { return "graph_facts" }

// GraphCommit 图谱变更集（表 graph_commits，append-only）。一行 = 一次批量
// 写入的审计锚点：统计计数与批次同事务落库。回滚不产生新 commit 行也不改
// 本行（不可变），回滚结果由 RollbackCommit 的返回值承载。
type GraphCommit struct {
	ID              string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID        uint64 `json:"tenant_id" gorm:"type:integer;not null;index:idx_graph_commits_tenant_time,priority:1"`
	Description     string `json:"description" gorm:"type:text;not null;default:''"`
	EntitiesAdded   int    `json:"entities_added" gorm:"not null;default:0"`
	EntitiesUpdated int    `json:"entities_updated" gorm:"not null;default:0"`
	FactsAdded      int    `json:"facts_added" gorm:"not null;default:0"`
	FactsSuperseded int    `json:"facts_superseded" gorm:"not null;default:0"`
	// GoldenGatePass apply 前的 golden set 回归断言是否通过（§3.5；v1 由
	// 调用方声明，缺省 true）。
	GoldenGatePass bool      `json:"golden_gate_pass" gorm:"not null;default:true"`
	CreatedBy      string    `json:"created_by" gorm:"type:varchar(36);not null"`
	CreatedAt      time.Time `json:"created_at" gorm:"index:idx_graph_commits_tenant_time,priority:2,sort:desc"`
}

// TableName pins the GORM table name.
func (GraphCommit) TableName() string { return "graph_commits" }

// ChunkExtractionCache 增量抽取缓存行（表 chunk_extraction_cache）。
// 命中条件 = chunk 行存在且 ContentHash 与 SchemaVersion 完全相同。
type ChunkExtractionCache struct {
	ID            string    `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID      uint64    `json:"tenant_id" gorm:"type:integer;not null"`
	ChunkID       string    `json:"chunk_id" gorm:"type:varchar(36);not null;uniqueIndex:uq_chunk_extraction_cache_chunk"`
	ContentHash   string    `json:"content_hash" gorm:"type:varchar(128);not null"`
	SchemaVersion int       `json:"schema_version" gorm:"not null;default:1"`
	Extraction    JSON      `json:"extraction" gorm:"type:jsonb;not null;default:'{}'"`
	CreatedAt     time.Time `json:"created_at"`
}

// TableName pins the GORM table name.
func (ChunkExtractionCache) TableName() string { return "chunk_extraction_cache" }

// ============================================================================
// 服务层输入/输出结构
// ============================================================================

// GraphCommitInput 是一次图谱批量提交（CreateCommit）的输入。entities/facts
// 为本批原子操作；GoldenGatePass 为 nil 时按 true 处理。
type GraphCommitInput struct {
	Description string         `json:"description"`
	Entities    []*GraphEntity `json:"entities"`
	Facts       []*GraphFact   `json:"facts"`
	// GoldenGatePass golden set 回归断言结果；nil = 未跑断言，按 true 落库。
	GoldenGatePass *bool `json:"golden_gate_pass"`
}

// GraphEntityUpsertResult 是批量实体 upsert 的结果（POST /graph/facts/entities
// 与 commit 的实体段共用）：added=新插入行数，updated=归一命中的更新行数。
type GraphEntityUpsertResult struct {
	Added    int            `json:"added"`
	Updated  int            `json:"updated"`
	Entities []*GraphEntity `json:"entities"`
}

// GraphFactUpsertResult 是批量事实写入的结果：added=新插入边数（重复陈述
// 合并进既有行不计入）。
type GraphFactUpsertResult struct {
	Added int          `json:"added"`
	Facts []*GraphFact `json:"facts"`
}

// GraphRollbackResult 是回滚的结果：v1 反向标记（置 superseded）而非删除，
// 计数为本次实际标记的行数；原 commit 行保持不可变。
type GraphRollbackResult struct {
	CommitID           string `json:"commit_id"`
	EntitiesSuperseded int    `json:"entities_superseded"`
	FactsSuperseded    int    `json:"facts_superseded"`
}

// GraphPathStep 是路径上的一跳：一条 active 事实边 + 两端实体（实体与关系
// 置信度都随行携带，调用方可按需降权展示 INFERRED 边）。
type GraphPathStep struct {
	Fact   *GraphFact   `json:"fact"`
	Source *GraphEntity `json:"source"`
	Target *GraphEntity `json:"target"`
}

// GraphFactPath 是一条 from → to 的路径（≤maxHops 跳，只走 EXTRACTED/INFERRED
// 的 active 边）。
type GraphFactPath struct {
	Hops  int              `json:"hops"`
	Steps []*GraphPathStep `json:"steps"`
}

// GraphImpactResult 是影响面查询的结果：从根实体沿 outgoing active 边 BFS
// 两层，返回受影响的事实与被波及的实体（含各层深度，便于前端分层展示）。
type GraphImpactResult struct {
	Root             *GraphEntity   `json:"root"`
	Facts            []*GraphFact   `json:"facts"`
	AffectedEntities []*GraphEntity `json:"affected_entities"`
	// Depth 实际走到的层数（1 或 2；根实体无出边时为 0）。
	Depth int `json:"depth"`
}
