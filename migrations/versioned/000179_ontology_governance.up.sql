DO $$ BEGIN RAISE NOTICE '[Migration 000179] Adding ontology governance levels...'; END $$;

-- 本体治理分级（KnowAxis V3.0 §8.2 L0–L4）。
--
-- 此前 schema 只有 draft/published 两态：任何一套结构发布后效果相同，
-- "这套本体产出的知识能被信任到什么程度"无处表达，强治理域（价格/合同/
-- 图纸/HR）也无法被强制要求更严的审核。
--
-- 级别决定三类强制动作：
--   L2+  双人审核
--   L3+  结构变更需迁移预演
--   L4   变更需黄金集回归
--
-- 存量 schema 回填 L0（最不可信的一级）：它们从未被评估过，默认按草案
-- 处理比默认按生产处理更安全。

ALTER TABLE ontology_schemas
    ADD COLUMN IF NOT EXISTS governance_level VARCHAR(4) NOT NULL DEFAULT 'L0';
-- domain 决定级别下限：price/contract ≥ L2，drawing/hr ≥ L3。
ALTER TABLE ontology_schemas
    ADD COLUMN IF NOT EXISTS domain VARCHAR(32) NOT NULL DEFAULT '';

UPDATE ontology_schemas SET governance_level = 'L0' WHERE governance_level = '';

CREATE INDEX IF NOT EXISTS idx_ontology_schemas_governance
    ON ontology_schemas (tenant_id, governance_level);
