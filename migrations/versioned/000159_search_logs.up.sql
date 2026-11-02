DO $$ BEGIN RAISE NOTICE '[Migration 000159] Adding search logs and knowledge gaps...'; END $$;

-- 搜索质量运营（KnowAxis V3.0 §12.5）与知识缺口（§14.4）。
--
-- 平台此前完全没有查询日志：零结果分析、热门搜索、Top5 命中、点击率、
-- 满意度这些指标都无从计算，"高频零结果词进入知识缺口清单"更是没有数据源。
--
-- 一轮搜索 = 一行。行内同时记下"授权过滤掉了多少候选"，因为 §2.10 要求
-- 计数不泄露——运营看到的是"被权限过滤了 N 条"，而不是"还有 N 条你看不到"。
CREATE TABLE IF NOT EXISTS search_logs (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    user_id VARCHAR(36) NOT NULL DEFAULT '',
    -- 查询原文（已截断，防超长 payload）
    query VARCHAR(512) NOT NULL,
    -- 查询归一化形式（小写/去空白），零结果聚类用
    query_normalized VARCHAR(512) NOT NULL DEFAULT '',
    -- 检索方式：knowledge_qa | hybrid | symbol | graph
    channel VARCHAR(32) NOT NULL DEFAULT 'hybrid',
    knowledge_base_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    -- 结果数（授权后）
    result_count INTEGER NOT NULL DEFAULT 0,
    -- 被权限/生命周期过滤掉的候选数（存在性保护：只暴露聚合数字）
    permission_filtered_count INTEGER NOT NULL DEFAULT 0,
    -- 首条结果分数，用于判断"有结果但都不相关"
    top_score DOUBLE PRECISION NOT NULL DEFAULT 0,
    duration_ms INTEGER NOT NULL DEFAULT 0,
    -- 用户随后是否点击/引用（点击回填）
    clicked BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_search_logs_tenant_time ON search_logs (tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_search_logs_zero_result
    ON search_logs (tenant_id, query_normalized) WHERE result_count = 0;

-- 知识缺口（§14.4 "高频搜索无结果、低满意度问题、问答拒答聚类"）。
-- 同一租户下同一个归一化问题聚合成一行；出现次数达标后由 Owner 处理
-- （补知识/给术语/关闭）。状态机保持极简：open → acknowledged → closed。
CREATE TABLE IF NOT EXISTS knowledge_gaps (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    -- 归一化后的问题，唯一键的一部分
    query_normalized VARCHAR(512) NOT NULL,
    -- 展示用的原始问题（取首次出现的原文）
    query VARCHAR(512) NOT NULL,
    -- 来源：search_zero_result | qa_refusal | low_satisfaction | manual
    source VARCHAR(32) NOT NULL DEFAULT 'search_zero_result',
    -- 累计出现次数
    occurrences INTEGER NOT NULL DEFAULT 1,
    -- 最近一次出现时间
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- 显式失效时间窗：只有窗口内有新出现才继续累计
    status VARCHAR(16) NOT NULL DEFAULT 'open',
    -- 分派到的 Owner（人工）
    assignee_id VARCHAR(36) NOT NULL DEFAULT '',
    -- 推荐模板/专家（运营填写，供 UI 展示）
    suggestion VARCHAR(255) NOT NULL DEFAULT '',
    resolved_knowledge_id VARCHAR(36) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_knowledge_gaps_tenant_query UNIQUE (tenant_id, query_normalized)
);

CREATE INDEX IF NOT EXISTS idx_knowledge_gaps_tenant_status
    ON knowledge_gaps (tenant_id, status, occurrences DESC);
