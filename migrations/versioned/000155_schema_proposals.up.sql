DO $$ BEGIN RAISE NOTICE '[Migration 000155] Adding schema proposals (AI-driven ontology modeling + governance)...'; END $$;

-- SchemaProposal 治理表（KnowAxis V3.0 §8.2/§8.4：文档驱动 AI 自动建模 →
-- SchemaProposal → 兼容性检查 → 审批 → 落成本体 Schema）。
--
-- 一行 = 一次"本体建模提案"。提案本身不动 ontology_schemas：只有 Apply
-- 通过后，才由 OntologyService 把提案内容落成 draft schema（或追加到同名
-- draft schema），并回填 applied_schema_id。这样 ontology_schemas 里永远
-- 只有"已被人批准过的结构"，AI 输出不会直接污染生效本体。
--
-- 状态机：pending → approved → applied；pending → rejected（终态）。
-- 审批要求 Admin 且审批人 != created_by（四眼），由服务层强制。
--
-- proposal 为结构化提案 JSON，与 internal/types/schema_proposal.go 的
-- ProposalPayload 同构：
--   {"types":[{"name","display_name","description","aliases":[],
--              "properties":[{"name","data_type","required"}]}],
--    "relations":[{"name","source_type","target_type","cardinality","description"}]}
-- data_type 对齐 OWL 2（string/number/boolean/date/enum/ref），cardinality
-- 对齐 ontology_relations（one_to_one/one_to_many/many_to_many）。
--
-- compatibility_report 为 CheckCompatibility 的结果（对齐调研报告 §3.2 的
-- "兼容性检查结果"）：新增类型（可自动通过）/ 同名类型属性变更（warning）/
-- 关系端点缺失（blocker），另含可能的改名识别（warning）。Apply 的硬门禁
-- 是"无 blocker"，warning 只做提示。
--
-- source_knowledge_ids 记录提案的输入文档（≤8 篇，服务层限制），用于评审
-- 时回看原始依据与后续复跑。
CREATE TABLE IF NOT EXISTS schema_proposals (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    name VARCHAR(128) NOT NULL,
    domain VARCHAR(64) NOT NULL DEFAULT '',
    source_knowledge_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    proposal JSONB NOT NULL DEFAULT '{}'::jsonb,
    rationale TEXT NOT NULL DEFAULT '',
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    reviewed_by VARCHAR(36) NOT NULL DEFAULT '',
    review_comment TEXT NOT NULL DEFAULT '',
    applied_schema_id VARCHAR(36) NOT NULL DEFAULT '',
    compatibility_report JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_by VARCHAR(36) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 待审列表按状态过滤（GET /ontology/proposals?status=pending），
-- 也是"某租户还有多少未决议提案"这类治理视图的唯一入口。
CREATE INDEX IF NOT EXISTS idx_schema_proposals_tenant_status
    ON schema_proposals (tenant_id, status);
