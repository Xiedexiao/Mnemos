DO $$ BEGIN RAISE NOTICE '[Migration 000143] Adding glossary terms table...'; END $$;

-- 术语表（KnowAxis V3.0 §4.4 的 v1 子集）。
--
-- glossary_terms：租户内一份"标准术语 → 别名/缩写/英文名"的受控映射。
--   - aliases 是字符串数组 JSONB（别名/缩写/英文名），供搜索同义词扩展。
--   - counter_examples 记录反例（"不指什么"），约束模型幻觉。
--   - 删除是软的：status 置 deprecated 且 superseded_by_id 指向接替术语，
--     保留映射供历史回答的术语重定向；不做物理删除（审计可回溯）。
--   - UNIQUE(tenant_id, standard_name)：同一租户内标准名唯一；别名允许
--     重复（同名别名归属哪个术语以先创建的 active 术语为准，v1 不做
--     别名级唯一约束）。
CREATE TABLE IF NOT EXISTS glossary_terms (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    standard_name VARCHAR(128) NOT NULL,
    aliases JSONB NOT NULL DEFAULT '[]'::jsonb,
    definition TEXT NOT NULL DEFAULT '',
    counter_examples TEXT DEFAULT '',
    domain VARCHAR(64) DEFAULT '',
    owner_id VARCHAR(36) DEFAULT '',
    status VARCHAR(16) NOT NULL DEFAULT 'active',
    superseded_by_id VARCHAR(36) DEFAULT '',
    created_by VARCHAR(36) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_glossary_terms_tenant_name UNIQUE (tenant_id, standard_name)
);

CREATE INDEX IF NOT EXISTS idx_glossary_terms_tenant_domain
    ON glossary_terms (tenant_id, domain);
