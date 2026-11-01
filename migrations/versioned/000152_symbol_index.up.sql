DO $$ BEGIN RAISE NOTICE '[Migration 000152] Adding symbol_index table...'; END $$;

-- 精确检索通道：符号索引（KnowAxis V3.0 §12.7 精准索引 v1）。
--
-- symbol_index：编号/错误码/型号等"符号"的精确命中倒排。普通检索
-- （向量 + 关键词）对 SOP-EQ-012 / E023 / XR-5000 这类符号召回不稳：
-- 分词器把符号切碎、向量把符号语义稀释；本表把符号归一（大写、去
-- 空格）后存行，ExactLookup 用等值查询命中并返回 chunk 级定位。
--
--   - symbol 归一规则：大写 + 去空格（"sop - eq-012" → "SOP-EQ-012"），
--     查询侧同规则归一后再等值匹配。
--   - symbol_type 标符号类别（code/document_number/model/error_code/
--     part_number/custom），v1 由抽取侧形状启发式判定，仅信息性——
--     命中匹配只看 symbol 等值，不看 type；part_number 留给后续
--     人工标注（custom 同）。
--   - locator 复用 source locators 语义（页码/段落定位，单个
--     SourceLocator 的 JSON 对象，字段见 internal/types/source_locator.go），
--     NULL 表示只有 chunk 级定位、没有更细的原文定位。
--   - chunk_id 空串 = 整篇文档级索引行（IndexSymbols 未带 chunk）。
--   - UNIQUE(tenant_id, symbol, knowledge_id, chunk_id)：重复建索引幂等
--     （服务层 ON CONFLICT 只覆盖 symbol_type/locator）。
CREATE TABLE IF NOT EXISTS symbol_index (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    kb_id VARCHAR(36) NOT NULL,
    knowledge_id VARCHAR(36) NOT NULL,
    -- 空串 = 文档级索引行（未按 chunk 建索引）。
    chunk_id VARCHAR(36) DEFAULT '',
    -- 归一后的符号（大写去空格）：SOP-EQ-012 / E023 / 型号编号等。
    symbol VARCHAR(255) NOT NULL,
    -- code | document_number | model | error_code | part_number | custom。
    symbol_type VARCHAR(24) DEFAULT 'code',
    -- 页码/段落定位，复用 source locators 语义（SourceLocator JSON）。
    locator JSONB DEFAULT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_symbol_index_hit UNIQUE (tenant_id, symbol, knowledge_id, chunk_id)
);

-- ExactLookup 主路径：租户 + 符号等值。
CREATE INDEX IF NOT EXISTS idx_symbol_index_tenant_symbol
    ON symbol_index (tenant_id, symbol);

-- /symbol-lookup 按 kb_ids 限定命中面。
CREATE INDEX IF NOT EXISTS idx_symbol_index_kb_symbol
    ON symbol_index (kb_id, symbol);
