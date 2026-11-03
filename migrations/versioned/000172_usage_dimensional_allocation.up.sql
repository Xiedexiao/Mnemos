DO $$ BEGIN RAISE NOTICE '[Migration 000172] Adding cost allocation dimensions to model usage...'; END $$;

-- 成本按空间/场景分摊（KnowAxis V3.0 §16.1）。
--
-- 000149 的台账只有 (tenant, day, model)：能回答"这个月花了多少 token"，
-- 回答不了"哪个空间、哪类场景花掉的"。没有这两个维度，按空间分摊只能靠
-- 猜。这里把维度并入累计键：同一模型同一天在不同场景/空间各占一行。

ALTER TABLE model_usage_daily
    ADD COLUMN IF NOT EXISTS scene VARCHAR(32) NOT NULL DEFAULT 'other';
ALTER TABLE model_usage_daily
    ADD COLUMN IF NOT EXISTS knowledge_base_id VARCHAR(36) NOT NULL DEFAULT '';

-- 存量行全部来自问答链路，回填为 chat 是事实而不是猜测；空间维度无法从
-- 历史数据恢复，保持空串，由看板计入"未分摊"。
UPDATE model_usage_daily SET scene = 'chat' WHERE scene = 'other';

-- 旧唯一约束把三个维度压成一行，必须换成五维键，否则场景/空间维度会被
-- ON CONFLICT DO UPDATE 合并回一行。
ALTER TABLE model_usage_daily
    DROP CONSTRAINT IF EXISTS uq_model_usage_daily_tenant_day_model;

CREATE UNIQUE INDEX IF NOT EXISTS uq_model_usage_daily_tenant_day_model_scene_space
    ON model_usage_daily (tenant_id, day, model_id, scene, knowledge_base_id);
