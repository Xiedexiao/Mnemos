DO $$ BEGIN RAISE NOTICE '[Migration 000146] Adding knowledge comments and recycle bin...'; END $$;

-- 知识评论（KnowAxis V3.0 §5.2 的 v1 子集）。
--
-- knowledge_comments：知识条目上的协作讨论。
--   - parent_id 非空表示回复；一层嵌套（回复的回复不允许，服务层校验
--     parent 自身必须是顶层评论）。
--   - anchor 是划词锚点 {chunk_id, start_offset, end_offset, quote}，
--     可空 = 全局评论（不锚定具体片段）。
--   - resolved/resolved_by 记录"已解决"状态（评审收口）。
--   - 删除是软删（deleted_at）：树形列表剔除后审计仍可回溯；作者或
--     Admin 可删（权限在服务层判定）。
CREATE TABLE IF NOT EXISTS knowledge_comments (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    knowledge_id VARCHAR(36) NOT NULL,
    parent_id VARCHAR(36) NOT NULL DEFAULT '',
    author_id VARCHAR(36) NOT NULL,
    body TEXT NOT NULL,
    anchor JSONB DEFAULT NULL,
    resolved BOOLEAN NOT NULL DEFAULT FALSE,
    resolved_by VARCHAR(36) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_knowledge_comments_tenant_knowledge_created
    ON knowledge_comments (tenant_id, knowledge_id, created_at);

-- 回收站（KnowAxis V3.0 §5.5 的 v1 子集）。
--
-- recycle_bin：软删实体的登记簿 + 清空倒计时。
--   - entity_type v1 只有 'knowledge'；entity_id 是被软删实体的 ID。
--   - purge_at = 删除时刻 + 30 天（created_at + INTERVAL '30 days'），
--     超期后由定时任务物理删除（PurgeDue，v1 只提供方法）。
--   - restored_at 非空表示已恢复：恢复后的条目不再参与 PurgeDue
--     （防止把"活"实体物理删掉）。
--   - UNIQUE(entity_type, entity_id)：同一实体至多一条登记（重复删除
--     走 upsert：重置 title/deleted_by/purge_at/created_at 并清空
--     restored_at，重开 30 天窗口）。
CREATE TABLE IF NOT EXISTS recycle_bin (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    entity_type VARCHAR(16) NOT NULL,
    entity_id VARCHAR(36) NOT NULL,
    title VARCHAR(255) DEFAULT '',
    deleted_by VARCHAR(36) NOT NULL,
    purge_at TIMESTAMPTZ NOT NULL,
    restored_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_recycle_bin_entity UNIQUE (entity_type, entity_id)
);

-- PurgeDue 扫描：按租户找超期未恢复的条目。
CREATE INDEX IF NOT EXISTS idx_recycle_bin_tenant_purge
    ON recycle_bin (tenant_id, purge_at);
