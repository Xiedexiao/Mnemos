DO $$ BEGIN RAISE NOTICE '[Migration 000157] Adding permission version ledger (revocation propagation)...'; END $$;

-- 权限版本账本（KnowAxis V3.0 §2.6 撤权传播 + §2.7 答案缓存）。
--
-- 每次授权变更（ACL 授予/撤销、分享链接签发/撤销、密级升降、知识移动导致的
-- 授权重置）都让受影响范围的权限版本 +1。缓存类通道（答案缓存、图谱查询
-- 缓存、检索建议）把版本号编进缓存键，版本一变旧键自然失效；不需要逐个通道
-- 发失效消息，也不会因为漏发一条消息而继续返回越权内容。
--
-- 三个作用域层级：
--   tenant     — 全租户（角色矩阵、租户级策略变更）
--   knowledge  — 单篇文档（文档 ACL、密级变更）
--   space      — 知识库（分享图变更、批量授权）
CREATE TABLE IF NOT EXISTS permission_versions (
    id VARCHAR(36) PRIMARY KEY DEFAULT uuid_generate_v4(),
    tenant_id INTEGER NOT NULL,
    scope_type VARCHAR(16) NOT NULL,
    scope_id VARCHAR(64) NOT NULL DEFAULT '',
    version BIGINT NOT NULL DEFAULT 1,
    -- 最近一次让版本前进的变更类型，便于排障时回答"为什么失效"。
    last_reason VARCHAR(64) NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (tenant_id, scope_type, scope_id)
);

CREATE INDEX IF NOT EXISTS idx_permission_versions_scope
    ON permission_versions (tenant_id, scope_type, scope_id);

-- 撤权传播台账：§2.6 要求"撤权完成"的标准不是事件已发出，而是每个出口都
-- 验证过看不到。这里记录一次撤权事件对每个出口的处置结果，供撤权演练与
-- 审计回放核对。
CREATE TABLE IF NOT EXISTS revocation_propagations (
    id VARCHAR(36) PRIMARY KEY DEFAULT uuid_generate_v4(),
    tenant_id INTEGER NOT NULL,
    knowledge_id VARCHAR(36) NOT NULL DEFAULT '',
    subject_id VARCHAR(64) NOT NULL DEFAULT '',
    -- 触发的变更类型：acl_revoke / classification_change / move / share_revoke
    trigger VARCHAR(32) NOT NULL,
    -- 出口名（search_index / vector_index / graph / answer_cache / ...）
    outlet VARCHAR(32) NOT NULL,
    -- 处置结果：invalidated / not_applicable / pending
    outcome VARCHAR(16) NOT NULL DEFAULT 'pending',
    permission_version BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_revocation_propagations_tenant
    ON revocation_propagations (tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_revocation_propagations_knowledge
    ON revocation_propagations (knowledge_id);
