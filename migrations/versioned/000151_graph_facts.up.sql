DO $$ BEGIN RAISE NOTICE '[Migration 000151] Adding graph fact layer tables...'; END $$;

-- 图谱事实层（B 期图谱增强，调研报告 §3.1/§3.2：Postgres 是事实层
-- system of record，Neo4j 只是可重投影的投影层）。
--
-- 四张表各管一件事：
--   graph_entities          实体登记表（归一主键 + 三级置信度 + 证据）。
--   graph_facts             事实表（本体关系边 + 证据绑定 + 三级置信度）。
--   graph_commits           图谱变更集（append-only，统计计数 + 回滚锚点）。
--   chunk_extraction_cache  增量抽取缓存（§9.4：内容没改不重复提取）。
--
-- 置信度三级（confidence）：EXTRACTED（schema 合法 + 证据对齐 + 分数达阈）/
-- INFERRED（规则/推导产生）/ AMBIGUOUS（类型越界、消歧冲突、多来源矛盾、
-- 低分——进人工审核队列）。AMBIGUOUS 行照实入库（带 confidence 字段），
-- 但永不进入"有效图"：所有默认查询路径只取 EXTRACTED + INFERRED。
--
-- 行状态（status）：active | superseded。废止不删行（Graphiti 模式）：
-- RollbackCommit 把该 commit 产出的行置 superseded，历史与审计可回放。

-- graph_entities：实体登记表。行身份 = (tenant_id, entity_type,
-- normalized_key)：归一命中即更新（attributes 合并、置信度就高不就低），
-- 不产生重复行。schema_id 对齐 B1 本体（ontology_schemas.id），'' 表示
-- 未对齐本体的遗留事实。entity_type / 关系词汇必须命中已发布 schema 的
-- 类型与关系名（服务层校验；越界输出降级进 AMBIGUOUS 桶）。
CREATE TABLE IF NOT EXISTS graph_entities (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    schema_id VARCHAR(36) NOT NULL DEFAULT '',
    entity_type VARCHAR(128) NOT NULL,
    name VARCHAR(255) NOT NULL,
    normalized_key VARCHAR(255) NOT NULL,
    attributes JSONB NOT NULL DEFAULT '{}'::jsonb,
    source_knowledge_id VARCHAR(36) NOT NULL DEFAULT '',
    confidence VARCHAR(16) NOT NULL DEFAULT 'EXTRACTED',
    status VARCHAR(16) NOT NULL DEFAULT 'active',
    commit_id VARCHAR(36) NOT NULL DEFAULT '',
    created_by VARCHAR(36) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_graph_entities_identity UNIQUE (tenant_id, entity_type, normalized_key)
);

CREATE INDEX IF NOT EXISTS idx_graph_entities_tenant_schema_confidence
    ON graph_entities (tenant_id, schema_id, confidence);
CREATE INDEX IF NOT EXISTS idx_graph_entities_source_knowledge
    ON graph_entities (source_knowledge_id);

-- graph_facts：事实表（S-P-O 三元组）。relation_name 必须命中同一 schema 的
-- ontology_relations.name（KAG 谓词模型）。evidence 为证据数组
-- [{knowledge_id, chunk_id, locator, quote}]，locator 复用
-- internal/types/source_locator.go 的 SourceLocator 语义（页/区块/字符区间），
-- 不造新定位结构。同一 (source, relation, target) 的重复陈述合并进既有
-- active 行（证据去重累积），不产生重复边。
CREATE TABLE IF NOT EXISTS graph_facts (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    schema_id VARCHAR(36) NOT NULL DEFAULT '',
    relation_name VARCHAR(128) NOT NULL,
    source_entity_id VARCHAR(36) NOT NULL,
    target_entity_id VARCHAR(36) NOT NULL,
    confidence VARCHAR(16) NOT NULL DEFAULT 'EXTRACTED',
    evidence JSONB NOT NULL DEFAULT '[]'::jsonb,
    status VARCHAR(16) NOT NULL DEFAULT 'active',
    commit_id VARCHAR(36) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_graph_facts_tenant_relation_confidence
    ON graph_facts (tenant_id, relation_name, confidence);
CREATE INDEX IF NOT EXISTS idx_graph_facts_source_entity
    ON graph_facts (source_entity_id);
CREATE INDEX IF NOT EXISTS idx_graph_facts_target_entity
    ON graph_facts (target_entity_id);

-- graph_commits：图谱变更集，append-only。一行 = 一次批量写入的审计锚点：
-- 统计计数（新增/更新实体、新增事实）在应用批次时与批次同事务落库，
-- 本批 entity/fact 行回填本 commit 的 id。硬规则：本表只允许 INSERT 与
-- SELECT——纠错通过 RollbackCommit 反向标记（v1 置 superseded 而非删除），
-- 不得 UPDATE/DELETE 既有 commit 行。
CREATE TABLE IF NOT EXISTS graph_commits (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    entities_added INT NOT NULL DEFAULT 0,
    entities_updated INT NOT NULL DEFAULT 0,
    facts_added INT NOT NULL DEFAULT 0,
    facts_superseded INT NOT NULL DEFAULT 0,
    golden_gate_pass BOOLEAN NOT NULL DEFAULT TRUE,
    created_by VARCHAR(36) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_graph_commits_tenant_time
    ON graph_commits (tenant_id, created_at DESC);

-- chunk_extraction_cache：增量抽取缓存（§9.4，LightRAG KV 缓存模式）。
-- 命中条件 = chunk 行存在且 content_hash 与 schema_version 完全相同：
-- 内容没改、本体没换版本，就不再调 LLM 重复提取。UNIQUE(chunk_id) 使缓存
-- 行为 chunk 的"当前缓存"，重复写入即覆盖（旧缓存无需保留）。
CREATE TABLE IF NOT EXISTS chunk_extraction_cache (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    chunk_id VARCHAR(36) NOT NULL,
    content_hash VARCHAR(128) NOT NULL,
    schema_version INT NOT NULL DEFAULT 1,
    extraction JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_chunk_extraction_cache_chunk UNIQUE (chunk_id)
);
