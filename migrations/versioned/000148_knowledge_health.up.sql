DO $$ BEGIN RAISE NOTICE '[Migration 000148] Adding knowledge health snapshots table...'; END $$;

-- 知识健康度看板快照（KnowAxis V3.0 §14.6 / §16.1 v1）。
--
-- knowledge_health_snapshots：每次计算追加一行，看板读最新一行，历史留档
-- 供趋势回看。
--   - scope: tenant | knowledge_base。tenant 行的 scope_id 为空串。
--   - metrics: 六维度指标 JSONB（freshness/completeness/usage/structure/
--     governance/total + score），由 KnowledgeHealthService 计算落库。
--   - 无外键：快照是历史留档，KB 删除后快照仍保留（只读看板不做级联清理）。
CREATE TABLE IF NOT EXISTS knowledge_health_snapshots (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    scope VARCHAR(16) NOT NULL,
    scope_id VARCHAR(36) NOT NULL DEFAULT '',
    metrics JSONB NOT NULL DEFAULT '{}'::jsonb,
    computed_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Latest 查询按 (tenant, scope, scope_id) 取 computed_at 最新一行；
-- 前缀 (tenant_id, scope) 已能支撑 scope_id 过滤的索引扫描。
CREATE INDEX IF NOT EXISTS idx_knowledge_health_snapshots_scope
    ON knowledge_health_snapshots (tenant_id, scope, computed_at DESC);
