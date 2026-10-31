DO $$ BEGIN RAISE NOTICE '[Migration 000147] Adding golden question sets and evaluation runs...'; END $$;

-- 黄金问题集 + 评测持久化（KnowAxis V3.0 §12.5/§14.8 的 v1 子集）。
--
-- golden_sets：租户内一份可复用的"黄金问题集"——发布门禁的基准问答集。
--   - kb_id 绑定评测用的知识库（运行评测时对它做 HybridSearch）；
--   - UNIQUE(tenant_id, name)：同一租户内集合名唯一；
--   - domain 可选领域标签（对齐术语表/知识条目的领域口径）。
CREATE TABLE IF NOT EXISTS golden_sets (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    name VARCHAR(128) NOT NULL,
    domain VARCHAR(64) DEFAULT '',
    kb_id VARCHAR(36) DEFAULT '',
    description TEXT DEFAULT '',
    created_by VARCHAR(36) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_golden_sets_tenant_name UNIQUE (tenant_id, name)
);

CREATE INDEX IF NOT EXISTS idx_golden_sets_tenant_domain
    ON golden_sets (tenant_id, domain);

-- golden_questions：集合内的黄金问题。
--   - expected_evidence 是期望证据引用数组 [{knowledge_id, chunk_id, locator}]，
--     检索指标（top5_hit / recall_at_5 / MRR）以 knowledge_id 为命中粒度；
--   - simulated_user_role 记录模拟提问者的角色（权限边界评测用，v1 只存
--     不参与判定）；risk_level / freshness_expectation 描述问题的风险与
--     时效预期（expired_must_refuse 表示"过期内容必须拒答"，供后续
--     拒答评测使用）；
--   - 题目文本在集合内由服务层保证唯一（批量导入按文本 upsert），
--     数据库层不加唯一约束以便 v1 灵活导入。
CREATE TABLE IF NOT EXISTS golden_questions (
    id VARCHAR(36) PRIMARY KEY,
    set_id VARCHAR(36) NOT NULL,
    question TEXT NOT NULL,
    expected_answer TEXT DEFAULT '',
    expected_evidence JSONB DEFAULT '[]'::jsonb,
    simulated_user_role VARCHAR(16) DEFAULT 'viewer',
    risk_level VARCHAR(16) DEFAULT 'low',
    freshness_expectation VARCHAR(16) DEFAULT 'valid',
    enabled BOOLEAN DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_golden_questions_set
    ON golden_questions (set_id);

-- golden_set_runs：一次评测的持久化结果（v1 同步执行，落库后可回溯）。
--   - status: running|completed|failed；
--   - metrics 聚合指标 {recall_at_5, ndcg, mrr, top5_hit,
--     answer_accuracy, refusal_correctness, pass}（answer/refusal 两项 v1
--     置 null，待接完整问答链路）；
--   - per_question 逐题得分与缺口 [{question_id, top5_hit, first_hit_rank,
--     recall_at_5, mrr, ndcg, missing_knowledge_ids, ...}]。
CREATE TABLE IF NOT EXISTS golden_set_runs (
    id VARCHAR(36) PRIMARY KEY,
    set_id VARCHAR(36) NOT NULL,
    tenant_id INTEGER NOT NULL,
    triggered_by VARCHAR(36) NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'running',
    metrics JSONB DEFAULT '{}'::jsonb,
    per_question JSONB DEFAULT '[]'::jsonb,
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_golden_set_runs_tenant_created
    ON golden_set_runs (tenant_id, created_at);

-- 门禁/趋势查询按 (tenant, set) 维度取最近一次 completed run。
CREATE INDEX IF NOT EXISTS idx_golden_set_runs_set_created
    ON golden_set_runs (set_id, created_at);
