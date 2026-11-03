DO $$ BEGIN RAISE NOTICE '[Migration 000170] Adding learning tasks and exam questions...'; END $$;

-- 学习任务与考试题库（KnowAxis V3.0 §15.5）。
--
-- §15.5 要求"与 SOP 变更联动：新版本发布 → 学习任务"以及"与考试/上岗资格
-- 关联"。平台此前只有学习路径与逐步完成：SOP 改版不会给任何人派学习任务，
-- 也没有题库可用于认证前的考核。

-- 学习任务：把"这条知识变了，相关岗位要重新学"落成可追踪的待办。
CREATE TABLE IF NOT EXISTS learning_tasks (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    -- 任务指向的学习路径与具体步骤（可空：只要求通读一条知识时没有路径）
    path_id VARCHAR(36) NOT NULL DEFAULT '',
    step_order INTEGER NOT NULL DEFAULT 0,
    -- 需要学习的内容：通常是刚变更的那条知识
    knowledge_id VARCHAR(36) NOT NULL DEFAULT '',
    -- 指派对象
    assignee_id VARCHAR(36) NOT NULL,
    -- 触发来源：sop_change | review_cycle | manual | certification
    source VARCHAR(24) NOT NULL DEFAULT 'manual',
    title VARCHAR(255) NOT NULL DEFAULT '',
    -- open | done | waived
    status VARCHAR(16) NOT NULL DEFAULT 'open',
    due_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    -- 完成后是否已重新认证
    recertified BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_learning_tasks_assignee
    ON learning_tasks (tenant_id, assignee_id, status);
CREATE INDEX IF NOT EXISTS idx_learning_tasks_knowledge
    ON learning_tasks (tenant_id, knowledge_id);

-- 考试题：认证前的考核题库（§15.5 与考试/上岗资格关联）。
CREATE TABLE IF NOT EXISTS learning_exam_questions (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    path_id VARCHAR(36) NOT NULL,
    -- 题目与选项（JSONB：{options:[...], correct:index}）
    prompt VARCHAR(512) NOT NULL,
    options JSONB NOT NULL DEFAULT '[]'::jsonb,
    -- 正确答案索引
    correct_index INTEGER NOT NULL DEFAULT 0,
    -- 解释（答错时展示）
    explanation TEXT NOT NULL DEFAULT '',
    created_by VARCHAR(36) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_learning_exam_questions_path
    ON learning_exam_questions (tenant_id, path_id);

-- 考试作答记录：通过才发认证。
CREATE TABLE IF NOT EXISTS learning_exam_attempts (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    path_id VARCHAR(36) NOT NULL,
    user_id VARCHAR(36) NOT NULL,
    -- 得分 0-100
    score INTEGER NOT NULL DEFAULT 0,
    passed BOOLEAN NOT NULL DEFAULT FALSE,
    -- 每题作答（JSONB：[{question_id, choice, correct}]）
    answers JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_learning_exam_attempts_user
    ON learning_exam_attempts (tenant_id, user_id, path_id, created_at DESC);
