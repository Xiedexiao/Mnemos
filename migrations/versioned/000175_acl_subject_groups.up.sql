DO $$ BEGIN RAISE NOTICE '[Migration 000175] Adding ACL subject groups...'; END $$;

-- 授权主体组（KnowAxis V3.0 §2.1）。
--
-- knowledge_acl.subject_type 早就允许 'group'/'role'，但没有组表也没有解析器，
-- 写进去的组授权从不生效。这里补组与成员两张表。
--
-- role 主体不建表：租户角色已经在 tenant_members 里，再存一份只会产生两份
-- 真相，且成员调岗时容易漏改。

CREATE TABLE IF NOT EXISTS acl_subject_groups (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    name VARCHAR(128) NOT NULL,
    description VARCHAR(512) NOT NULL DEFAULT '',
    created_by VARCHAR(36) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_acl_group_name UNIQUE (tenant_id, name)
);

CREATE TABLE IF NOT EXISTS acl_subject_group_members (
    group_id VARCHAR(36) NOT NULL,
    user_id VARCHAR(36) NOT NULL,
    tenant_id INTEGER NOT NULL,
    added_by VARCHAR(36) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (group_id, user_id)
);

-- 展开"这个用户属于哪些组"是每次鉴权的必经查询，按 (tenant, user) 建索引。
CREATE INDEX IF NOT EXISTS idx_acl_group_members_tenant
    ON acl_subject_group_members (tenant_id, user_id);

-- 组授权按 subject_id 查 knowledge_acl；已有的 idx_knowledge_acl_subject
-- 覆盖 (tenant_id, subject_type, subject_id, action)，无需新增。
