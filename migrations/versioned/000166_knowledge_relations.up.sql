DO $$ BEGIN RAISE NOTICE '[Migration 000166] Adding knowledge relations...'; END $$;

-- 知识关联（KnowAxis V3.0 §4.5）。
--
-- 平台此前只有两类"关联"：图谱事实（实体之间）与 wiki 反链（wiki 页面之间）。
-- 文档与文档之间没有关系表，所以"这条 SOP 依据那份制度""新版替代旧版"
-- "这两份文件结论冲突"都无处表达，删除检查也无从判断"谁引用了它"。
--
-- 边的语义（§4.5 关联可标注类型）：
--   based_on   依据
--   replaces   替代
--   supplements 补充
--   conflicts  冲突
--   prerequisite 前置
--   responsible 责任
--   related    一般相关
--
-- 双向存储：插入一条 A→B 时同时写 B→A 的反向行，让"谁引用了当前页面"成为
-- 一次索引查询而不是全表扫描。两条行共享同一 pair_key，删除时成对移除。
CREATE TABLE IF NOT EXISTS knowledge_relations (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    -- 关系方向：从 from 指向 to
    from_knowledge_id VARCHAR(36) NOT NULL,
    to_knowledge_id VARCHAR(36) NOT NULL,
    relation_type VARCHAR(24) NOT NULL DEFAULT 'related',
    -- 双向配对键：min(from,to) || ':' || max(from,to) || ':' || type
    pair_key VARCHAR(120) NOT NULL,
    -- 发起方向：forward = 原始方向；reverse = 自动生成的反向镜像
    direction VARCHAR(8) NOT NULL DEFAULT 'forward',
    -- 置信/来源：manual | ai_suggested | migration
    origin VARCHAR(16) NOT NULL DEFAULT 'manual',
    -- 备注/理由（人工关联时写"为什么相关"）
    note VARCHAR(512) NOT NULL DEFAULT '',
    created_by VARCHAR(36) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_knowledge_relations_pair UNIQUE (tenant_id, from_knowledge_id, to_knowledge_id, relation_type)
);

CREATE INDEX IF NOT EXISTS idx_knowledge_relations_from
    ON knowledge_relations (tenant_id, from_knowledge_id, relation_type);
CREATE INDEX IF NOT EXISTS idx_knowledge_relations_to
    ON knowledge_relations (tenant_id, to_knowledge_id, relation_type);
CREATE INDEX IF NOT EXISTS idx_knowledge_relations_pair
    ON knowledge_relations (tenant_id, pair_key);
