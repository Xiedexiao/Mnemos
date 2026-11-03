-- 回滚前先把新维度产生的多行合并回 (tenant, day, model)，否则旧唯一约束
-- 无法重建。
WITH grouped AS (
    SELECT tenant_id, day, model_id, MIN(id) AS keep_id,
           SUM(prompt_tokens) AS prompt_tokens,
           SUM(completion_tokens) AS completion_tokens,
           SUM(call_count) AS call_count
    FROM model_usage_daily
    GROUP BY tenant_id, day, model_id
)
UPDATE model_usage_daily AS m
SET prompt_tokens = g.prompt_tokens,
    completion_tokens = g.completion_tokens,
    call_count = g.call_count
FROM grouped AS g
WHERE m.id = g.keep_id;

DELETE FROM model_usage_daily
WHERE id NOT IN (
    SELECT MIN(id) FROM model_usage_daily GROUP BY tenant_id, day, model_id
);

DROP INDEX IF EXISTS uq_model_usage_daily_tenant_day_model_scene_space;

ALTER TABLE model_usage_daily DROP COLUMN IF EXISTS knowledge_base_id;
ALTER TABLE model_usage_daily DROP COLUMN IF EXISTS scene;

ALTER TABLE model_usage_daily
    ADD CONSTRAINT uq_model_usage_daily_tenant_day_model UNIQUE (tenant_id, day, model_id);
