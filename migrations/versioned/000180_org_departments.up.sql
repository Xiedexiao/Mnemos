DO $$ BEGIN RAISE NOTICE '[Migration 000180] Adding organization departments...'; END $$;

-- 组织树（KnowAxis V3.0 §2.1 权限 L1 层 + §16.3 组织架构同步）。
--
-- 权限模型是四层：租户 → 组织/部门 → 空间 → 文档。此前只有租户层与文档层，
-- 中间那层不存在，因此"部门经理只能管理本部门"这类判断无处落地。
--
-- 用物化路径（path）而不是递归查询：鉴权路径上要频繁回答"这个部门是不是我的
-- 下级"，一次字符串前缀比较远比递归 CTE 便宜，且跨 SQLite/MySQL/Postgres
-- 行为一致。path 用 id 段而不是名字——改名不影响层级，同级重名也不会混淆。

CREATE TABLE IF NOT EXISTS org_departments (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    -- 空字符串表示根部门
    parent_id VARCHAR(36) NOT NULL DEFAULT '',
    name VARCHAR(128) NOT NULL,
    -- "/root-id/child-id"；(tenant, path) 唯一保证一个节点只有一条路径
    path VARCHAR(1024) NOT NULL,
    depth INTEGER NOT NULL DEFAULT 0,
    manager_user_id VARCHAR(36) NOT NULL DEFAULT '',
    -- SCIM externalId：IdP 改名/重连不会产生重复部门
    external_id VARCHAR(191) NOT NULL DEFAULT '',
    -- 绑定的 ACL 主体组：部门成员同时是组成员，组上授权立即生效
    subject_group_id VARCHAR(36) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_org_department_path UNIQUE (tenant_id, path)
);

-- externalId 只在非空时唯一（手工创建的部门可以没有）。
CREATE UNIQUE INDEX IF NOT EXISTS uq_org_department_external
    ON org_departments (tenant_id, external_id)
    WHERE external_id <> '';

CREATE INDEX IF NOT EXISTS idx_org_departments_tenant
    ON org_departments (tenant_id, path);
CREATE INDEX IF NOT EXISTS idx_org_departments_manager
    ON org_departments (tenant_id, manager_user_id);

-- 一个用户可以同时属于多个部门（兼任很常见），每个部门内只有一条记录。
CREATE TABLE IF NOT EXISTS org_department_members (
    department_id VARCHAR(36) NOT NULL,
    user_id VARCHAR(36) NOT NULL,
    tenant_id INTEGER NOT NULL,
    is_manager BOOLEAN NOT NULL DEFAULT FALSE,
    added_by VARCHAR(36) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (department_id, user_id)
);

-- 展开"这个用户属于哪些部门"是每次范围判定的必经查询。
CREATE INDEX IF NOT EXISTS idx_org_dept_members_tenant
    ON org_department_members (tenant_id, user_id);
