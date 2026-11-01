DO $$ BEGIN RAISE NOTICE '[Migration 000156] Adding parse quality score table...'; END $$;

-- 解析质量评分（KnowAxis V3.0 §3.5 / §9.4）。
--
-- parse_quality_scores：每个知识条目一行"当前解析质量"，六个维度各自 0-100，
-- total_score 是 §3.5 权重表的加权和（25/20/15/10/15/15，见
-- internal/types/parse_quality.go 的 ParseQualityWeight* 常量）：
--
--   text_completeness      文本完整度 25  是否提取出主要正文
--   structure_completeness 结构完整度 20  标题层级 / 段落分块是否保留
--   table_usability        表格可用性 15  表头和单元格是否可理解
--   ocr_quality            OCR 质量   10  图片文字识别可信度
--   metadata_completeness  元数据完整度 15 标题/描述/自定义元数据/文件哈希
--   citable                可引用性   15  能否定位回原文（SourceLocators）
--
-- grade ∈ {good(≥85), fair(≥60), poor(<60)}；below_threshold 是
-- total_score < 60 的落库冗余（让解析异常队列能走单列索引，不必在 SQL 里
-- 重复阈值常量）。低质量文档「可搜但不可作高置信来源」——队列只是把它标
-- 出来，检索侧是否降权由消费方读 below_threshold 决定。
--
-- 行身份 = knowledge_id（UNIQUE）：评分是知识条目的"当前快照"，重算即覆盖，
-- 不保留历史（历史口径由 computed_at 与审计 parse_quality.computed 承载）。
-- details JSONB 保留打分输入信号，便于回放"为什么是这个分"。
CREATE TABLE IF NOT EXISTS parse_quality_scores (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    knowledge_id VARCHAR(36) NOT NULL,
    text_completeness INT NOT NULL DEFAULT 0,
    structure_completeness INT NOT NULL DEFAULT 0,
    table_usability INT NOT NULL DEFAULT 0,
    ocr_quality INT NOT NULL DEFAULT 0,
    metadata_completeness INT NOT NULL DEFAULT 0,
    citable INT NOT NULL DEFAULT 0,
    total_score INT NOT NULL DEFAULT 0,
    grade VARCHAR(16) NOT NULL DEFAULT 'unknown',
    below_threshold BOOLEAN NOT NULL DEFAULT FALSE,
    details JSONB NOT NULL DEFAULT '{}'::jsonb,
    computed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_parse_quality_scores_knowledge UNIQUE (knowledge_id)
);

-- 解析异常队列：WHERE tenant_id = ? AND below_threshold = TRUE
-- ORDER BY total_score ASC。前缀 (tenant_id, below_threshold) 足够，
-- 队列体量小（低质量文档是少数），排序走内存。
CREATE INDEX IF NOT EXISTS idx_parse_quality_scores_tenant_below_threshold
    ON parse_quality_scores (tenant_id, below_threshold);
