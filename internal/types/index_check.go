package types

import (
	"fmt"
	"strings"
)

// ============================================================================
// 索引覆盖自检（KnowAxis V3.0 §17.1 性能 / §17.4 可运维性）。
//
// 平台有 351 个索引，但**没有任何机制检查关键查询是否真的用上了它们**。
// 这不是"压测"能替代的：压测告诉你"慢"，索引检查告诉你"为什么慢"以及
// "哪条查询会随数据量线性变差"。
//
// 三个决定：
//  1. 检查**计划**而不是耗时。耗时依赖数据量与机器，同一条查询在测试库上
//     毫秒级、在生产上几十秒；而"是否全表扫描"是一个与规模无关的布尔事实，
//     今天就能判定明天会不会出问题。
//  2. 覆盖关键路径的少数查询，不是全部。全表检查会产出一堆无关紧要的
//     警告，而告警一旦开始被忽略就失去了价值。
//  3. 明确方言差异：SQLite 用 `EXPLAIN QUERY PLAN`（输出 SCAN/SEARCH），
//     PostgreSQL 用 `EXPLAIN`（输出 Seq Scan/Index Scan）。解析逻辑分开，
//     不能用同一个字符串匹配硬套。
// ============================================================================

// IndexCheckQuery is one query the self-check inspects.
type IndexCheckQuery struct {
	// Name identifies the check in reports.
	Name string
	// SQL is the query, with ? placeholders.
	SQL string
	// Args bind the placeholders (dummy values are fine: the plan does not
	// depend on the values, only on the predicates).
	Args []interface{}
	// Why explains what breaks if this query degrades.
	Why string
}

// IndexCheckResult is one check's verdict.
type IndexCheckResult struct {
	// Name echoes the check.
	Name string `json:"name"`
	// Plan is the raw plan text, truncated for the report.
	Plan string `json:"plan"`
	// ScansAll reports a full table scan.
	ScansAll bool `json:"scans_all"`
	// Verdict is "ok" or "full_scan".
	Verdict string `json:"verdict"`
	// Why / Advice explain the consequence and the fix.
	Why    string `json:"why,omitempty"`
	Advice string `json:"advice,omitempty"`
}

// IndexCheckVerdict values.
const (
	IndexCheckOK       = "ok"
	IndexCheckFullScan = "full_scan"
	// IndexCheckUnknown means the plan could not be read; reported rather than
	// silently counted as passing.
	IndexCheckUnknown = "unknown"
)

// maxPlanReportRunes bounds the plan text kept in a report.
const maxPlanReportRunes = 600

// IndexCheckQueries is the set of queries worth checking.
//
// 选的是**热路径 + 会随数据量线性变差**的查询：检索前的生命周期/权限过滤、
// 列表分页、证据包回放、审计检索。刻意不追求覆盖所有查询——一份包含几十条
// 无关警告的报告没人会读完。
func IndexCheckQueries() []IndexCheckQuery {
	return []IndexCheckQuery{
		{
			Name: "knowledges.by_tenant_kb",
			SQL: "SELECT id FROM knowledges WHERE tenant_id = ? AND knowledge_base_id = ? " +
				"AND deleted_at IS NULL ORDER BY created_at DESC LIMIT 20",
			Args: []interface{}{1, "x"},
			Why:  "文档列表分页；退化会让每个知识库的首页变慢",
		},
		{
			Name: "knowledges.lifecycle_exclusion",
			SQL: "SELECT id FROM knowledges WHERE tenant_id = ? AND knowledge_base_id IN (?, ?) " +
				"AND lifecycle_status IN (?, ?) AND deleted_at IS NULL",
			Args: []interface{}{1, "a", "b", "draft", "expired"},
			Why:  "检索前的生命周期门禁；退化为全表扫描会让每次搜索都扫全库",
		},
		{
			Name: "chunks.by_knowledge",
			SQL:  "SELECT id FROM chunks WHERE knowledge_id = ? ORDER BY chunk_index ASC LIMIT 50",
			Args: []interface{}{"x"},
			Why:  "阅读与证据回放取分块；退化会让打开文档变慢",
		},
		{
			Name: "knowledge_relations.by_pair",
			SQL: "SELECT id FROM knowledge_relations WHERE tenant_id = ? AND " +
				"(from_knowledge_id = ? OR to_knowledge_id = ?)",
			Args: []interface{}{1, "a", "b"},
			Why:  "知识关联与影响分析；退化会让每次关系查询扫全表",
		},
		{
			Name: "evidence_packs.by_request",
			SQL:  "SELECT id FROM evidence_packs WHERE tenant_id = ? AND request_id = ? LIMIT 1",
			Args: []interface{}{1, "x"},
			Why:  "证据包回放与 MCP knowledge_evidence；这是唯一索引，退化说明索引缺失",
		},
		{
			Name: "search_logs.by_tenant_time",
			SQL:  "SELECT COUNT(*) FROM search_logs WHERE tenant_id = ? AND created_at >= ?",
			Args: []interface{}{1, "2026-01-01"},
			Why:  "搜索质量与运营看板；退化会让看板随日志增长线性变慢",
		},
		{
			Name: "knowledge_view_events.by_user_history",
			SQL: "SELECT knowledge_id FROM knowledge_view_events WHERE tenant_id = ? AND user_id = ? " +
				"AND created_at >= ? ORDER BY created_at DESC LIMIT 100",
			Args: []interface{}{1, "u", "2026-01-01"},
			Why:  "浏览历史与协同过滤；退化会让推荐位随阅读量增长线性变慢",
		},
	}
}

// ParseIndexPlan reads a plan text and reports whether it scans a full table.
//
// dialect 决定解析方式：SQLite 的 `EXPLAIN QUERY PLAN` 输出 `SCAN table` 或
// `SEARCH table USING INDEX`；PostgreSQL 的 `EXPLAIN` 输出 `Seq Scan on table`
// 或 `Index Scan using ...`。把两种混在一个匹配里会把 PostgreSQL 的
// "Index Scan" 误判成全扫描（它同时含 "Scan"）。
func ParseIndexPlan(dialect, plan string) IndexCheckResult {
	lowered := strings.ToLower(plan)
	result := IndexCheckResult{Plan: truncatePlan(plan)}
	if strings.TrimSpace(plan) == "" {
		result.Verdict = IndexCheckUnknown
		result.Advice = "计划为空，无法判定"
		return result
	}
	switch dialect {
	case "postgres":
		// "Seq Scan" is the full-table path; "Index Scan" / "Bitmap Index Scan"
		// are fine. Match the exact phrase rather than the word "scan".
		result.ScansAll = strings.Contains(lowered, "seq scan")
	case "sqlite":
		// SQLite prints "SCAN tablename" for a full scan and
		// "SEARCH tablename USING INDEX ..." when an index is used. A bare
		// "SCAN" with no "USING" is the full-scan form.
		lines := strings.Split(lowered, "\n")
		for _, line := range lines {
			if !strings.Contains(line, "scan") {
				continue
			}
			if strings.Contains(line, "using") {
				continue
			}
			result.ScansAll = true
			break
		}
	default:
		result.Verdict = IndexCheckUnknown
		result.Advice = fmt.Sprintf("不认识的方言 %q", dialect)
		return result
	}
	if result.ScansAll {
		result.Verdict = IndexCheckFullScan
		result.Advice = "该查询会随表增长线性变慢；补一个覆盖其谓词的索引"
	} else {
		result.Verdict = IndexCheckOK
	}
	return result
}

// truncatePlan bounds one plan string for a report.
func truncatePlan(plan string) string {
	trimmed := strings.TrimSpace(plan)
	runes := []rune(trimmed)
	if len(runes) <= maxPlanReportRunes {
		return trimmed
	}
	return string(runes[:maxPlanReportRunes]) + "…"
}

// IndexCheckReport is the whole self-check output.
type IndexCheckReport struct {
	// Dialect the checks ran against.
	Dialect string `json:"dialect"`
	// Results per query.
	Results []IndexCheckResult `json:"results"`
	// FullScans / Unknown are the counts, so a caller can gate on them without
	// iterating.
	FullScans int `json:"full_scans"`
	Unknown   int `json:"unknown"`
	// CheckedAt is when it ran.
	CheckedAt string `json:"checked_at"`
}

// Pass reports whether every check produced a usable, non-scanning plan.
//
// 未知也算不通过：一次跑不通的检查不能被当成通过，否则"自检通过"会变成
// 一句没人敢信的话。
func (r IndexCheckReport) Pass() bool {
	return r.FullScans == 0 && r.Unknown == 0
}
