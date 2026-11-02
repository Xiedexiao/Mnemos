DO $$ BEGIN RAISE NOTICE '[Migration 000161] Adding knowledge templates and field definitions...'; END $$;

-- 自定义字段与模板中心（KnowAxis V3.0 §3.2 人工录入 / §4.7 模板与字段治理）。
--
-- 平台此前只有自由 JSON 的 custom_metadata：没有字段定义，也就没有必填校验、
-- 默认值、可见性、召回字段，更没有"字段变更影响存量知识"的迁移任务。模板
-- 中心同样不存在，人工录入只能凭记忆填。
--
-- knowledge_templates：知识模板（故障案例/FAQ/SOP/会议纪要/项目复盘…）。
-- 每次修改都是一个新版本，旧版本保留（§4.7 模板版本化），因为历史知识是
-- 按当时的模板结构录入的。
CREATE TABLE IF NOT EXISTS knowledge_templates (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    -- 模板的业务键：同一租户内唯一、跨版本稳定
    template_key VARCHAR(64) NOT NULL,
    -- 版本号，从 1 开始；同 (tenant, key) 内递增
    version INTEGER NOT NULL DEFAULT 1,
    name VARCHAR(128) NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    -- 适用知识类型（file|manual|faq|...），空表示不限
    knowledge_type VARCHAR(32) NOT NULL DEFAULT '',
    -- 字段定义见 knowledge_fields，通过 template_id 关联
    -- active=false 的旧版本仍可读取（历史知识引用它），但不再用于新建
    active BOOLEAN NOT NULL DEFAULT TRUE,
    created_by VARCHAR(36) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_knowledge_templates_key_version UNIQUE (tenant_id, template_key, version)
);

CREATE INDEX IF NOT EXISTS idx_knowledge_templates_tenant_key
    ON knowledge_templates (tenant_id, template_key, version DESC);

-- knowledge_fields：模板下的字段定义（§3.2 自定义字段：类型/必填/默认值/
-- 校验/可见性/召回字段）。
CREATE TABLE IF NOT EXISTS knowledge_fields (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    template_id VARCHAR(36) NOT NULL,
    field_key VARCHAR(64) NOT NULL,
    label VARCHAR(128) NOT NULL,
    -- text | textarea | number | date | select | multiselect | attachment | reference
    field_type VARCHAR(24) NOT NULL DEFAULT 'text',
    required BOOLEAN NOT NULL DEFAULT FALSE,
    -- 默认值（字符串形式，按 field_type 解析）
    default_value VARCHAR(512) NOT NULL DEFAULT '',
    -- 校验规则：{"min":0,"max":100,"pattern":"...","options":["a","b"]}
    rules JSONB NOT NULL DEFAULT '{}'::jsonb,
    -- 可编辑角色下限：viewer|contributor|admin（超出者为只读）
    editable_role VARCHAR(32) NOT NULL DEFAULT 'contributor',
    -- 召回字段（§3.2）：参与精确检索与排序的优先字段
    recall_field BOOLEAN NOT NULL DEFAULT FALSE,
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_knowledge_fields_key UNIQUE (template_id, field_key)
);

CREATE INDEX IF NOT EXISTS idx_knowledge_fields_template
    ON knowledge_fields (template_id, sort_order);

-- knowledge_field_migrations：字段结构变更对存量知识的影响任务（§4.7
-- "字段新增/删除/改名/改类型需审批，生成迁移任务"）。v1 只记录任务，实际
-- 回填由运营按任务清单执行——不自动改写用户内容。
CREATE TABLE IF NOT EXISTS knowledge_field_migrations (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    template_id VARCHAR(36) NOT NULL,
    -- add | remove | rename | retype
    change_kind VARCHAR(16) NOT NULL,
    field_key VARCHAR(64) NOT NULL,
    -- 旧字段键（rename/retype 时记录）
    previous_key VARCHAR(64) NOT NULL DEFAULT '',
    -- 受影响知识条数（生成任务时的快照）
    affected_count INTEGER NOT NULL DEFAULT 0,
    -- pending | done | waived
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    note TEXT NOT NULL DEFAULT '',
    created_by VARCHAR(36) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_knowledge_field_migrations_tenant
    ON knowledge_field_migrations (tenant_id, status, created_at DESC);
