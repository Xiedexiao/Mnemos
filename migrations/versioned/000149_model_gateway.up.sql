DO $$ BEGIN RAISE NOTICE '[Migration 000149] Adding model gateway tables...'; END $$;

-- 模型网关 v1（KnowAxis V3.0 §16.5）：按密级/任务路由 + Prompt 版本化 + 成本记录。
--
-- model_routing_rules：任务/密级 → 模型 的路由规则。
--   - tenant_id = 0 表示平台级规则；租户规则优先于平台级规则命中。
--   - scope = task 时 scope_value 是任务名（chat/embedding/rerank/vlm/asr/...）；
--     scope = classification 时 scope_value 是数据密级（public/internal/
--     secret/confidential，见 internal/types/knowledge_lifecycle.go）。
--   - model_id 指向 models 表的 ID，无外键：模型删除不级联清理规则，
--     ResolveModel 命中的模型若已不存在由调用方回退默认模型。
--   - UNIQUE(tenant_id, scope, scope_value)：同一租户同一任务/密级只有一条
--     生效规则，改路由 = 更新 model_id。
CREATE TABLE IF NOT EXISTS model_routing_rules (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL DEFAULT 0,
    scope VARCHAR(16) NOT NULL,
    scope_value VARCHAR(32) NOT NULL,
    model_id VARCHAR(64) NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_by VARCHAR(36) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_model_routing_rules_tenant_scope_value UNIQUE (tenant_id, scope, scope_value)
);

-- ResolveModel 按 (tenant, scope, scope_value) 点查；该索引同时覆盖平台级
-- (tenant_id = 0) 与租户级查询的前缀。
CREATE INDEX IF NOT EXISTS idx_model_routing_rules_resolve
    ON model_routing_rules (tenant_id, scope, enabled);

-- prompt_versions：提示词模板的版本化覆盖层（V3.0 §16.5）。
--   - template_key 对应 config/prompt_templates/ 文件名去后缀
--     （system_prompt/rewrite/graph_extraction/...）。
--   - tenant_id = 0 表示内置模板的版本行；租户可以有自己的覆盖版本。
--   - 模板加载器仍以 YAML 为内置默认；DB 中的版本是"租户/平台级覆盖"，
--     未命中时回退 YAML（本迁移不改既有加载器，接线见 ModelGatewayService
--     的 LatestVersion 注释）。
--   - 版本只增不改：SaveVersion 恒定 version = 当前最大 + 1，内容与最新版
--     相同时去重（幂等），不做 UPDATE/DELETE。
CREATE TABLE IF NOT EXISTS prompt_versions (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL DEFAULT 0,
    template_key VARCHAR(64) NOT NULL,
    version INT NOT NULL,
    content TEXT NOT NULL,
    change_note TEXT NOT NULL DEFAULT '',
    created_by VARCHAR(36) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_prompt_versions_tenant_key_version UNIQUE (tenant_id, template_key, version)
);

-- Latest 查询按 (tenant, template_key) 取 version 最大一行。
CREATE INDEX IF NOT EXISTS idx_prompt_versions_latest
    ON prompt_versions (tenant_id, template_key, version DESC);

-- model_usage_daily：模型调用的按天成本累计（V3.0 §16.5 成本记录 v1）。
--   - 一行 = 一个租户一天一个模型的累计值；RecordUsage 以
--     UNIQUE(tenant_id, day, model_id) 为冲突目标做增量 upsert。
--   - 无外键：模型删除后历史用量保留（成本对账不受模型下线影响）。
--   - 不做成本限额/单价换算（后续阶段）；本表只记 token 与次数。
CREATE TABLE IF NOT EXISTS model_usage_daily (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    day DATE NOT NULL,
    model_id VARCHAR(64) NOT NULL,
    prompt_tokens BIGINT NOT NULL DEFAULT 0,
    completion_tokens BIGINT NOT NULL DEFAULT 0,
    call_count BIGINT NOT NULL DEFAULT 0,
    CONSTRAINT uq_model_usage_daily_tenant_day_model UNIQUE (tenant_id, day, model_id)
);

-- UsageSummary 按天/模型聚合，默认按 day 升序返回。
CREATE INDEX IF NOT EXISTS idx_model_usage_daily_tenant_day
    ON model_usage_daily (tenant_id, day);
