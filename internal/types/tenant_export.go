package types

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// ============================================================================
// 租户级逻辑导出（KnowAxis V3.0 §16.3 备份与恢复 / §16.5 数据可携带）。
//
// 平台此前没有任何租户级导出：全库备份依赖 pg_dump 与对象存储（运维层，
// 应用层无法自证），而"把我的工作区数据带走"是一个纯粹的应用层需求——
// 换部署、做审计归档、迁到另一个环境，都需要它。
//
// 四个决定：
//  1. **逻辑导出，不是物理备份**。产物是 JSON 清单（表名 → 行数组）加一份
//     元数据，不含索引、序列、WAL。它能回答"我的数据在哪"和"能不能搬走"，
//     不能替代数据库备份——这两件事在文档与接口名里都要说清楚。
//  2. **白名单，不是黑名单**。只有显式列出的表会被导出。漏掉一张表的代价
//     是"导出不完整"（可见、可补）；而黑名单漏掉一张表的代价是**泄露**
//     （比如凭据、密钥、邀请令牌）。方向必须选错得安全的那一边。
//  3. 显式**排除**凭据、令牌、密钥类表，即使它们在白名单里也拒绝——白名单
//     是人工维护的，人工维护就会犯错。
//  4. 导出走 ExportControl 的审批与密级上限：secret/confidential 内容必须
//     人工审批（四眼）。导出是最大的一次性数据外流，不能比单篇下载更宽松。
// ============================================================================

// TenantExportFormatVersion is the manifest schema version.
//
// 回灌方据此决定怎么读：格式变了要能识别旧版本，而不是猜。
const TenantExportFormatVersion = 1

// TenantExportTable names one exportable table.
type TenantExportTable struct {
	// Name is the database table name.
	Name string
	// TenantColumn is the column holding the owning tenant. Empty means the
	// table is keyed some other way (see the join in the repository).
	TenantColumn string
	// Description explains what the table carries, for the manifest.
	Description string
}

// tenantExportTables is the whitelist, in a stable order.
//
// 这是"能带走什么"的定义。加入新表需要显式的一行，且必须回答两个问题：
// 它是否含凭据/密钥？它是否只是派生数据（可重建）？
var tenantExportTables = []TenantExportTable{
	{Name: "knowledge_bases", TenantColumn: "tenant_id", Description: "知识空间"},
	{Name: "knowledges", TenantColumn: "tenant_id", Description: "知识文档元数据"},
	{Name: "chunks", TenantColumn: "tenant_id", Description: "分块正文"},
	{Name: "knowledge_tags", TenantColumn: "tenant_id", Description: "标签"},
	{Name: "knowledge_tag_relations", TenantColumn: "", Description: "文档-标签关联"},
	{Name: "knowledge_relations", TenantColumn: "tenant_id", Description: "知识关联边"},
	{Name: "knowledge_versions", TenantColumn: "tenant_id", Description: "已发布版本快照"},
	{Name: "knowledge_candidates", TenantColumn: "tenant_id", Description: "经验候选卡片"},
	{Name: "glossary_terms", TenantColumn: "tenant_id", Description: "术语表"},
	{Name: "official_answers", TenantColumn: "tenant_id", Description: "官方答案"},
	{Name: "approval_tasks", TenantColumn: "tenant_id", Description: "审批任务"},
	{Name: "approval_flows", TenantColumn: "tenant_id", Description: "审批流配置"},
	{Name: "approval_flow_nodes", TenantColumn: "tenant_id", Description: "审批流节点"},
	{Name: "change_requests", TenantColumn: "tenant_id", Description: "变更单"},
	{Name: "change_items", TenantColumn: "", Description: "变更条目"},
	{Name: "impact_tasks", TenantColumn: "tenant_id", Description: "影响任务"},
	{Name: "quality_tasks", TenantColumn: "tenant_id", Description: "质量任务"},
	{Name: "sampling_inspections", TenantColumn: "tenant_id", Description: "抽检记录"},
	{Name: "evidence_packs", TenantColumn: "tenant_id", Description: "问答证据包"},
	{Name: "knowledge_gaps", TenantColumn: "tenant_id", Description: "知识缺口"},
	{Name: "saved_knowledge_views", TenantColumn: "tenant_id", Description: "保存的视图"},
	{Name: "org_departments", TenantColumn: "tenant_id", Description: "组织部门树"},
	{Name: "ontology_schemas", TenantColumn: "tenant_id", Description: "本体 schema"},
	{Name: "graph_entities", TenantColumn: "tenant_id", Description: "图谱实体"},
	{Name: "graph_facts", TenantColumn: "tenant_id", Description: "图谱事实"},
	{Name: "graph_commits", TenantColumn: "tenant_id", Description: "图谱变更集"},
	{Name: "token_budgets", TenantColumn: "tenant_id", Description: "Token 预算"},
	{Name: "model_fallback_chains", TenantColumn: "tenant_id", Description: "模型降级链"},
}

// revokedTenantExportTables are refused even if someone adds them to the
// whitelist above.
//
// 白名单是人工维护的，人工维护就会犯错。这些表一旦进入导出产物，泄露的不是
// 内容而是**访问权**：拿到一个 API key 或邀请令牌就等于拿到了账号。因此这里
// 做第二道拒绝，并把理由写下来。
var revokedTenantExportTables = map[string]string{
	"api_keys":                "an exported API key is an exported credential",
	"users":                   "accounts are provisioned, not cloned; passwords would leak with them",
	"auth_tokens":             "session tokens are live credentials",
	"tenant_invitations":      "an invitation link is a one-time credential",
	"mcp_services":            "carries OAuth client secrets",
	"network_access_policies": "a restored allow-list could lock the target workspace out",
}

// TenantExportTableAllowed reports whether one table may be exported.
//
// 返回的 reason 在拒绝时说明理由，成功时为空。
func TenantExportTableAllowed(name string) (bool, string) {
	if reason, revoked := revokedTenantExportTables[name]; revoked {
		return false, reason
	}
	for _, table := range tenantExportTables {
		if table.Name == name {
			return true, ""
		}
	}
	return false, "the table is not in the export whitelist"
}

// ExportableTables returns the effective whitelist.
//
// 派生自同一份数据，且已经剔除被显式吊销的表——调用方不需要自己再过滤一遍，
// 避免两处判断不一致。
func ExportableTables() []TenantExportTable {
	out := make([]TenantExportTable, 0, len(tenantExportTables))
	for _, table := range tenantExportTables {
		if _, revoked := revokedTenantExportTables[table.Name]; revoked {
			continue
		}
		out = append(out, table)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// TenantExportManifest is the header of one export.
type TenantExportManifest struct {
	// FormatVersion is the schema version of this document.
	FormatVersion int `json:"format_version"`
	// TenantID is the workspace that was exported.
	TenantID uint64 `json:"tenant_id"`
	// ExportedAt is when the export ran (UTC).
	ExportedAt time.Time `json:"exported_at"`
	// ExportedBy is the user who ran it.
	ExportedBy string `json:"exported_by"`
	// ExportRequestID links back to the approved ExportControl request.
	ExportRequestID string `json:"export_request_id,omitempty"`
	// Tables summarises what was written, per table.
	Tables []TenantExportTableSummary `json:"tables"`
	// Note explains what this artefact is and is not.
	Note string `json:"note"`
}

// TenantExportTableSummary is the per-table row count.
type TenantExportTableSummary struct {
	// Name is the table.
	Name string `json:"name"`
	// Rows is how many rows were written.
	Rows int `json:"rows"`
	// Skipped is true when the table does not exist in this deployment (the
	// whitelist is shared across versions).
	Skipped bool `json:"skipped,omitempty"`
	// Reason explains a skip.
	Reason string `json:"reason,omitempty"`
}

// TenantExportDocument is a complete export: manifest plus row data.
type TenantExportDocument struct {
	// Manifest describes the export.
	Manifest TenantExportManifest `json:"manifest"`
	// Data maps table name → rows. Rows are generic maps so the exporter does
	// not need a struct per table, and a schema change does not break it.
	Data map[string][]map[string]any `json:"data"`
}

const (
	// TenantExportMaxRowsPerTable bounds one table in one export.
	//
	// 导出是管理动作，不是流式迁移。一个有百万 chunk 的租户应当走迁移工具；
	// 这里设上限是为了让"意外导出一个巨大的库"变成一个明确的拒绝，而不是
	// 一个把进程 OOM 掉的请求。
	TenantExportMaxRowsPerTable = 50000
	// TenantExportNote is the standing caveat attached to every export.
	TenantExportNote = "这是逻辑导出（表数据 JSON），不是数据库备份：不含索引、序列与 WAL，" +
		"不能用于数据库级恢复。凭据、会话令牌与邀请链接被显式排除。"
)

// NewTenantExportManifest builds the manifest header.
func NewTenantExportManifest(
	tenantID uint64, exportedBy string, exportRequestID string, now time.Time,
) TenantExportManifest {
	return TenantExportManifest{
		FormatVersion:   TenantExportFormatVersion,
		TenantID:        tenantID,
		ExportedAt:      now.UTC(),
		ExportedBy:      exportedBy,
		ExportRequestID: strings.TrimSpace(exportRequestID),
		Tables:          []TenantExportTableSummary{},
		Note:            TenantExportNote,
	}
}

// ValidateTenantExportManifest checks a manifest before a restore reads the data.
//
// 回灌方必须先过这一关：格式版本不认识就拒绝，而不是"尽力而为"地把新版数据
// 塞进旧 schema——那会写进半份数据且很难察觉。
func ValidateTenantExportManifest(manifest TenantExportManifest) error {
	if manifest.FormatVersion <= 0 {
		return fmt.Errorf("the export has no format version")
	}
	if manifest.FormatVersion > TenantExportFormatVersion {
		return fmt.Errorf(
			"the export uses format version %d, newer than this build supports (%d)",
			manifest.FormatVersion, TenantExportFormatVersion)
	}
	if manifest.TenantID == 0 {
		return fmt.Errorf("the export has no tenant id")
	}
	candidates := make([]string, 0, len(manifest.Tables))
	for _, table := range manifest.Tables {
		candidates = append(candidates, table.Name)
	}
	sort.Strings(candidates)
	return nil
}

// TenantExportClassificationCeiling returns the highest classification a
// tenant export must be approved for.
//
// 导出是最大的一次性外流：只要窗口里有一条 secret 内容，整份导出就要按
// secret 审批。按"平均密级"审批是最容易犯的错——平均会把一条机密稀释成
// 无关紧要的数字，而它就在文件里。
func TenantExportClassificationCeiling(classifications []string) KnowledgeClassification {
	highest := KnowledgeClassificationPublic
	for _, raw := range classifications {
		candidate := ParseKnowledgeClassification(raw)
		if candidate.Rank() > highest.Rank() {
			highest = candidate
		}
	}
	return highest
}

// TenantExportNeedsApproval reports whether a ceiling requires human approval.
//
// 与 ExportControl 的既有策略一致：secret/confidential 必须人工审批。
func TenantExportNeedsApproval(ceiling KnowledgeClassification) bool {
	return ceiling.Rank() >= KnowledgeClassificationSecret.Rank()
}
