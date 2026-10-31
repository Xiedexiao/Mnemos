DO $$ BEGIN RAISE NOTICE '[Migration 000149] Dropping model gateway tables...'; END $$;

DROP INDEX IF EXISTS idx_model_usage_daily_tenant_day;
DROP TABLE IF EXISTS model_usage_daily;

DROP INDEX IF EXISTS idx_prompt_versions_latest;
DROP TABLE IF EXISTS prompt_versions;

DROP INDEX IF EXISTS idx_model_routing_rules_resolve;
DROP TABLE IF EXISTS model_routing_rules;
