DO $$ BEGIN RAISE NOTICE '[Migration 000160] Adding multi-stage approval chain...'; END $$;

-- 多级审批链（KnowAxis V3.0 §6.3 审批模式：或签 / 会签 / 逐级 / 加签 / 转签 / 委托）。
--
-- v1 只有"攒票"：一个任务记 N 票，够数就过。这表达不了"先部门 Owner、
-- 再质量、最后法务"这种逐级链，也表达不了"这一层 3 个人里任一人签即可"
-- （或签）与"这一层 3 个人必须全签"（会签）的区别。
--
-- 模型分三层：
--   approval_flow_nodes    流程定义里的节点模板（谁、按什么模式签）
--   approval_task_nodes    提交时按定义实例化出的在途节点（可加签/转签）
--   approval_votes          每一票（含代理委托的 delegator）

CREATE TABLE IF NOT EXISTS approval_flow_nodes (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    flow_id VARCHAR(36) NOT NULL,
    -- 节点顺序，从 1 开始；逐级审批按它推进
    seq INTEGER NOT NULL,
    name VARCHAR(64) NOT NULL DEFAULT '',
    -- or = 或签（任一通过）；and = 会签（全部通过）
    mode VARCHAR(8) NOT NULL DEFAULT 'or',
    -- 审批人选择方式：role（按租户角色整层）| users（显式用户列表）
    assignee_kind VARCHAR(16) NOT NULL DEFAULT 'role',
    assignee_role VARCHAR(32) NOT NULL DEFAULT 'admin',
    assignee_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_approval_flow_nodes UNIQUE (flow_id, seq)
);

CREATE INDEX IF NOT EXISTS idx_approval_flow_nodes_flow ON approval_flow_nodes (flow_id, seq);

-- 在途节点：提交时从 flow 定义快照实例化，之后加签/转签只改这里，不改定义。
CREATE TABLE IF NOT EXISTS approval_task_nodes (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    task_id VARCHAR(36) NOT NULL,
    seq INTEGER NOT NULL,
    name VARCHAR(64) NOT NULL DEFAULT '',
    mode VARCHAR(8) NOT NULL DEFAULT 'or',
    approver_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    -- pending = 未激活（前序未完成）；active = 当前该签；approved/rejected/skipped
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    -- 会签需要的通过人数（= 当时 approver_ids 长度；加签会同步 +1）
    required_count INTEGER NOT NULL DEFAULT 1,
    decided_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_approval_task_nodes UNIQUE (task_id, seq)
);

CREATE INDEX IF NOT EXISTS idx_approval_task_nodes_task ON approval_task_nodes (task_id, seq);

-- 每一票。delegate_of 记录"这一票是某人委托此人代投的"（§6.3 委托/休假代理）。
CREATE TABLE IF NOT EXISTS approval_votes (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    task_id VARCHAR(36) NOT NULL,
    node_id VARCHAR(36) NOT NULL,
    approver_id VARCHAR(36) NOT NULL,
    -- 委托投票时填被代理人；普通投票为空
    delegate_of VARCHAR(36) NOT NULL DEFAULT '',
    decision VARCHAR(16) NOT NULL,
    comment TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_approval_votes_task ON approval_votes (task_id, created_at ASC);
CREATE INDEX IF NOT EXISTS idx_approval_votes_node ON approval_votes (node_id);

-- 委托关系（§6.3 委托：休假代理）。有效期内的委托让 delegate_id 可以代
-- delegator_id 投票。
CREATE TABLE IF NOT EXISTS approval_delegations (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    delegator_id VARCHAR(36) NOT NULL,
    delegate_id VARCHAR(36) NOT NULL,
    starts_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ,
    reason VARCHAR(255) NOT NULL DEFAULT '',
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_approval_delegations_pair
    ON approval_delegations (tenant_id, delegator_id, delegate_id);
