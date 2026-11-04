package types

import (
	"fmt"
	"strings"
)

// ============================================================================
// 高风险问题的引用覆盖率（KnowAxis V3.0 §13.5 "高风险决策缺少官方来源"）。
//
// §13.5 的场景表里定义了 no_official_source 这个拒答标记，但平台此前**没有
// 任何生产方**：没有风险分级，也就永远不会判定"这是高风险决策且缺少官方
// 来源"。同时"高风险问题引用覆盖率必须 100%"这条验收要求也没有校验。
//
// 三个决定：
//  1. 风险分级是**查询侧**判定，不是内容侧。同一条制度既可以回答"流程怎么
//     走"（低风险），也可以回答"这个金额能不能批"（高风险）——风险取决于
//     问题要做什么决策，而不是取决于文档密级。
//  2. 分级用**可解释的关键词表**而不是模型分类。误判的代价不对称：把一个
//     低风险问题判成高风险，代价是多一条提示；反过来则是让人拿参考材料去
//     做合规/资金决策。因此宁可宽进，且每一档都写明命中的词，便于复核与
//     调词表。
//  3. "官方来源"有明确口径：L3 权威层级，或生命周期为 valid 的 L2。一份
//     待复审的 L2 不算官方——它自己都还没被确认过。
// ============================================================================

// AnswerRiskLevel is the decision-risk of a question (§13.5).
type AnswerRiskLevel string

const (
	// AnswerRiskLow 一般事实/流程咨询。
	AnswerRiskLow AnswerRiskLevel = "low"
	// AnswerRiskMedium 涉及操作、变更、审批流程。
	AnswerRiskMedium AnswerRiskLevel = "medium"
	// AnswerRiskHigh 合规、安全、资金、人事处分等高风险决策。
	AnswerRiskHigh AnswerRiskLevel = "high"
)

// Valid reports whether the level is known.
func (l AnswerRiskLevel) Valid() bool {
	switch l {
	case AnswerRiskLow, AnswerRiskMedium, AnswerRiskHigh:
		return true
	default:
		return false
	}
}

// highRiskMarkers are the terms that make a question a high-stakes decision.
//
// 词表刻意按"决策后果"而不是按业务领域组织：任何领域都可能出现"能不能
// 批/能不能放行/是否合规"这类问题，而它们共同的特性是**答错的代价由组织
// 承担**。命中任一词即判高风险，不做词频加权——漏判的代价远高于误判。
var highRiskMarkers = []string{
	// 合规与法律
	"合规", "违法", "违规", "法律责任", "处罚", "诉讼", "仲裁", "免责",
	"审计", "监管", "资质", "认证是否",
	// 资金与合同
	"金额", "付款", "报销", "预算", "合同", "报价", "折扣", "结算",
	"能不能批", "可否批准", "是否批准", "能否签",
	// 安全与人身
	"安全", "事故", "伤亡", "风险等级", "停产", "停线", "紧急停机",
	"是否安全", "能不能继续运行",
	// 人事与处分
	"辞退", "开除", "处分", "绩效评定", "薪酬", "晋升",
}

// mediumRiskMarkers mark questions that touch a governed process.
var mediumRiskMarkers = []string{
	"流程", "审批", "变更", "申请", "工单", "检修", "保养周期",
	"谁负责", "审批人", "升级", "例外",
}

// AssessAnswerRisk classifies a question and explains why.
//
// 返回的 matched 是命中的词（去重、按出现顺序），调用方据此在证据包里
// 留下"为什么判成高风险"，也便于运营按命中词调词表。
func AssessAnswerRisk(question string) (AnswerRiskLevel, []string) {
	normalized := strings.ToLower(strings.TrimSpace(question))
	if normalized == "" {
		return AnswerRiskLow, nil
	}
	matched := matchMarkers(normalized, highRiskMarkers)
	if len(matched) > 0 {
		return AnswerRiskHigh, matched
	}
	matched = matchMarkers(normalized, mediumRiskMarkers)
	if len(matched) > 0 {
		return AnswerRiskMedium, matched
	}
	return AnswerRiskLow, nil
}

// matchMarkers returns the markers present in the normalized question.
func matchMarkers(normalized string, markers []string) []string {
	out := make([]string, 0, 2)
	for _, marker := range markers {
		if strings.Contains(normalized, marker) {
			out = append(out, marker)
		}
	}
	return out
}

// IsOfficialSource reports whether one hit may serve as an official basis.
//
// 口径：L3 权威层级，或 valid 生命周期下的 L2。
//   - L3 是发布门禁（Admin + 非含糊置信）授予的"可作权威答案"层级；
//   - L2 是"已确认"，但只有仍处于 valid 时才算官方——待复审/疑似过期的
//     L2 自己都还没被确认过，不能拿它当官方来源。
//   - 手工上传的文档没有层级标记，一律不算官方来源。
func IsOfficialSource(level string) bool {
	switch KnowledgeCandidateLevel(level) {
	case KnowledgeCandidateLevelL3:
		return true
	case KnowledgeCandidateLevelL2:
		// 生命周期在调用方一并传入时由调用方判断；这里只认层级，
		// 生命周期门槛见 CitationCoverage.Assess。
		return true
	default:
		return false
	}
}

// CitationCoverage is the §13.5 coverage verdict for one answered turn.
type CitationCoverage struct {
	// RiskLevel is the question's decision risk.
	RiskLevel AnswerRiskLevel `json:"risk_level"`
	// RiskMarkers are the terms that produced the level (empty for low).
	RiskMarkers []string `json:"risk_markers,omitempty"`
	// CandidateCount is how many hits the answer would cite.
	CandidateCount int `json:"candidate_count"`
	// OfficialCount is how many of them are official sources.
	OfficialCount int `json:"official_count"`
	// Coverage is OfficialCount / CandidateCount (0 when there are no hits).
	Coverage float64 `json:"coverage"`
	// MeetsRequirement reports whether the §13.5 requirement holds for this
	// risk level. For high risk that means Coverage == 1.
	MeetsRequirement bool `json:"meets_requirement"`
	// RequiresCaveat reports whether the answer must carry the
	// no_official_source notice.
	RequiresCaveat bool `json:"requires_caveat"`
	// Reason explains a failed requirement in one line.
	Reason string `json:"reason,omitempty"`
}

// AssessCitationCoverage computes the §13.5 coverage verdict.
//
// 只有高风险要求 100%：中低风险允许只有参考来源（它们答错不构成决策依据），
// 高风险则要求每一条引用都必须能作为官方依据——一条参考材料混在官方依据里，
// 读者无法区分哪一句是"必须遵守"、哪一句是"仅供参考"。
//
// officials 与 total 都按**去重前的引用条数**计：同一篇文档被引用两次
// 不会让覆盖率更好看，也不会更差。
func AssessCitationCoverage(
	question string, results []*SearchResult, levelOf func(*SearchResult) string,
) CitationCoverage {
	risk, markers := AssessAnswerRisk(question)
	coverage := CitationCoverage{RiskLevel: risk, RiskMarkers: markers}
	for _, result := range results {
		if result == nil {
			continue
		}
		coverage.CandidateCount++
		if isOfficialResult(result, levelOf(result)) {
			coverage.OfficialCount++
		}
	}
	if coverage.CandidateCount > 0 {
		coverage.Coverage = float64(coverage.OfficialCount) / float64(coverage.CandidateCount)
	}
	switch risk {
	case AnswerRiskHigh:
		coverage.MeetsRequirement = coverage.CandidateCount > 0 &&
			coverage.OfficialCount == coverage.CandidateCount
		if !coverage.MeetsRequirement && coverage.CandidateCount > 0 {
			coverage.RequiresCaveat = true
			coverage.Reason = fmt.Sprintf(
				"high-risk question: %d of %d cited sources are not official",
				coverage.CandidateCount-coverage.OfficialCount, coverage.CandidateCount)
		}
	default:
		// 中低风险没有覆盖率要求：它们答错不构成决策依据。
		coverage.MeetsRequirement = true
	}
	return coverage
}

// isOfficialResult checks the level plus the lifecycle gate.
//
// L2 只有在仍处于 valid 时才算官方（见 IsOfficialSource 的注释）；L3 不受
// 生命周期影响——它由发布门禁授予，降级会走显式治理动作。
func isOfficialResult(result *SearchResult, level string) bool {
	if result == nil || !IsOfficialSource(level) {
		return false
	}
	if KnowledgeCandidateLevel(level) == KnowledgeCandidateLevelL2 {
		return ParseKnowledgeLifecycleStatus(result.KnowledgeLifecycleStatus) ==
			KnowledgeLifecycleValid
	}
	return true
}
