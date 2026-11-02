DO $$ BEGIN RAISE NOTICE '[Migration 000163] Adding space kinds and folder ACL...'; END $$;

-- 空间类型与目录级权限（KnowAxis V3.0 §4.1 空间 / §2.3 权限继承与上限）。
--
-- 平台此前只有"知识库"这一个容器概念，没有空间类型及其默认策略；目录只是
-- 一个 folder_path 字符串，不是策略节点，所以"目录单独授权、子目录继承、
-- 目录移动触发权限重算"都无处落地。

-- 空间类型（§4.1 五种）：personal / team / public / project / external_collab。
ALTER TABLE knowledge_bases ADD COLUMN IF NOT EXISTS space_kind VARCHAR(24) NOT NULL DEFAULT 'team';
-- 空间默认密级：新入库内容未显式定密时的兜底（§2.4 定密"无确认时按空间默认密级处理"）。
ALTER TABLE knowledge_bases ADD COLUMN IF NOT EXISTS default_classification VARCHAR(16) NOT NULL DEFAULT 'internal';
-- 空间默认审核流程（approval_flows 的 kb 维度已覆盖，这里只标记是否强制审核）。
ALTER TABLE knowledge_bases ADD COLUMN IF NOT EXISTS require_review BOOLEAN NOT NULL DEFAULT FALSE;
-- 项目/外部协作空间的生命周期终点：到期后自动转只读并归档（§4.1）。
ALTER TABLE knowledge_bases ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ;

-- 目录级 ACL（§2.3 目录可单独授权，但受父级上限约束）。
-- 目录用 (knowledge_base_id, folder_path) 标识：folder_path 是目录树的稳定
-- 路径，与 knowledges.folder_path 同一套归一化规则。
CREATE TABLE IF NOT EXISTS folder_acl (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL,
    -- 归一化后的目录路径；空串表示知识库根
    folder_path VARCHAR(1024) NOT NULL DEFAULT '',
    subject_type VARCHAR(16) NOT NULL DEFAULT 'user',
    subject_id VARCHAR(64) NOT NULL,
    action VARCHAR(24) NOT NULL DEFAULT 'view',
    effect VARCHAR(8) NOT NULL DEFAULT 'allow',
    -- inherit_children=false 表示该目录不再向下继承本行（仅对目录本身生效）
    inherit_children BOOLEAN NOT NULL DEFAULT TRUE,
    granted_by VARCHAR(36) NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_folder_acl UNIQUE (knowledge_base_id, folder_path, subject_type, subject_id, action)
);

CREATE INDEX IF NOT EXISTS idx_folder_acl_lookup
    ON folder_acl (tenant_id, knowledge_base_id, subject_type, subject_id, action);
CREATE INDEX IF NOT EXISTS idx_folder_acl_path
    ON folder_acl (knowledge_base_id, folder_path);

-- 权限上限违规记录（§2.3 唯一权限过多 / 破坏继承比例进入治理清单）。
-- 移动或恢复继承时如果产生"子级比父级更宽"的情况，记录在这里供治理盘点。
CREATE TABLE IF NOT EXISTS acl_ceiling_violations (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL,
    -- 资源：knowledge | folder
    resource_type VARCHAR(16) NOT NULL,
    resource_id VARCHAR(1024) NOT NULL,
    -- subject_ceiling | classification_ceiling | action_ceiling
    kind VARCHAR(32) NOT NULL,
    detail JSONB NOT NULL DEFAULT '{}'::jsonb,
    detected_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_acl_ceiling_violations_tenant
    ON acl_ceiling_violations (tenant_id, detected_at DESC);
