package service

import (
	"context"
	"strings"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

// Refusal assessment for the trusted-RAG answer path (KnowAxis V3.0 §13.5).
//
// The pipeline already refuses when retrieval returns nothing. The spec's other
// six cases were not distinguished, so a question answered only from expired
// evidence read exactly like a normal answer, and an out-of-scope question was
// answered from whatever the caller happened to be able to see.
//
// The assessment runs on the merged candidate set, after permission and
// lifecycle gating, and decides one of three things per request:
//
//	silent    — answer normally
//	caveated  — answer, but the reply must carry the scenario's notice
//	refuse    — do not answer; the pack records the marker

// RefusalDecision is the outcome of assessRefusal.
type RefusalDecision struct {
	// Marker is the Evidence Pack refusal value ("" means a normal answer).
	Marker types.EvidencePackRefusal
	// Notice is the user-facing caveat when AllowsAnswer is true.
	Notice string
	// AllowsAnswer reports whether evidence still backs an answer.
	AllowsAnswer bool
	// HidesExistence reports whether the reply must not reveal that matching
	// content exists (the out-of-scope case, §2.10).
	HidesExistence bool
}

// Silent reports whether the assessment changes nothing about the reply.
func (d RefusalDecision) Silent() bool {
	return d.Marker == types.EvidenceRefusalNone
}

// assessRefusal classifies the merged evidence set against the §13.5 matrix.
//
// Only the cases that can be decided from evidence metadata are handled here;
// question-scope judgements (speculation, out-of-scope intent) need the query
// classifier and are reported by the caller. A conflict marker wins over a
// freshness marker because "two authoritative sources disagree" is a stronger
// reason not to give a single answer than "the source is old".
func assessRefusal(results []*types.SearchResult) RefusalDecision {
	if len(results) == 0 {
		scenario := types.RefusalScenarios[types.EvidenceRefusalNoEvidence]
		return RefusalDecision{Marker: scenario.Marker, Notice: scenario.Message}
	}

	var (
		live        int
		staleOnly   int
		expiredOnly int
	)
	for _, result := range results {
		if result == nil {
			continue
		}
		status := types.ParseKnowledgeLifecycleStatus(result.KnowledgeLifecycleStatus)
		switch {
		case status == types.KnowledgeLifecycleConflict:
			// §7.1：冲突必须展示双方，不强行下结论。
			scenario := types.RefusalScenarios[types.EvidenceRefusalConflict]
			return RefusalDecision{Marker: scenario.Marker, Notice: scenario.Message}
		case status.Authoritative():
			live++
		case status == types.KnowledgeLifecyclePendingReviewCycle || status == types.KnowledgeLifecycleStale:
			staleOnly++
		default:
			// expired / replaced / delisted / archived / draft 等不应到这里
			// （检索前已排除），但历史数据可能残留。
			expiredOnly++
		}
	}

	switch {
	case live > 0:
		return RefusalDecision{Marker: types.EvidenceRefusalNone}
	case staleOnly > 0:
		// 只有待复审/疑似过期：可以回答，但必须带时效提示，且不作为权威依据。
		scenario := types.RefusalScenarios[types.EvidenceRefusalStaleOnly]
		return RefusalDecision{
			Marker:       scenario.Marker,
			Notice:       scenario.Message,
			AllowsAnswer: scenario.AllowsAnswer,
		}
	default:
		scenario := types.RefusalScenarios[types.EvidenceRefusalExpiredOnly]
		return RefusalDecision{Marker: scenario.Marker, Notice: scenario.Message}
	}
}

// assessHighRiskCoverage applies the §13.5 "high-risk decision without an
// official source" rule on top of the freshness matrix.
//
// 判定需要两件事：问题的决策风险（查询侧关键词分级）与每条引用的知识层级
// （L2/L3 才算官方来源）。层级来自 CustomMetadata 的 knowaxis_level，因此
// 这里从结果的自定义元数据里读——命中行已经带了它，不必再查一次库。
//
// 只有高风险才要求覆盖率 100%。不足时**不拒答**（场景表里 AllowsAnswer
// 为 true），而是加一条必须先声明的提示：读者拿到的是参考资料，不能直接
// 当决策依据。这与"无依据拒答"是两件事——后者是完全没有材料。
func assessHighRiskCoverage(
	chatManage *types.ChatManage, results []*types.SearchResult,
) types.CitationCoverage {
	question := ""
	if chatManage != nil {
		question = chatManage.Query
	}
	levelOf := func(result *types.SearchResult) string {
		if result == nil {
			return ""
		}
		return knowledgeLevelFromCustomMetadata(result.KnowledgeCustomMetadata)
	}
	return types.AssessCitationCoverage(question, results, levelOf)
}

// knowledgeLevelFromCustomMetadata extracts the §10.4 level from the
// human-readable metadata text carried on a hit.
//
// KnowledgeCustomMetadataText 是 "key: value" 逐行的可读形式（它同时进提示
// 词），所以这里按行解析而不是当 JSON。解析不出来就是"未标注"，与"标注为
// 低层级"是两件事，都会落回"非官方来源"。
func knowledgeLevelFromCustomMetadata(text string) string {
	for _, line := range strings.Split(text, "\n") {
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		if strings.TrimSpace(key) != types.KnowledgeCandidateMetaLevelKey {
			continue
		}
		level := strings.TrimSpace(value)
		if types.KnowledgeCandidateLevel(level).Valid() {
			return level
		}
		return ""
	}
	return ""
}

// refusalNoticeFor returns the user-facing text for a marker, if any.
func refusalNoticeFor(marker types.EvidencePackRefusal) string {
	if marker == types.EvidenceRefusalNone {
		return ""
	}
	if scenario, ok := types.RefusalScenarios[marker]; ok {
		return scenario.Message
	}
	return ""
}

// applyRefusalAssessment runs the §13.5 matrix over the merged candidates and
// decides whether this turn may be answered.
//
// Returns true when the caller must stop the pipeline (a hard refusal was
// handled here). A caveated answer returns false after recording the notice on
// the ChatManage so the output stage can append it.
//
// A search that already returned nothing keeps the existing fallback path: that
// case predates this matrix and its wording is configured per tenant.
func (s *sessionService) applyRefusalAssessment(
	ctx context.Context, chatManage *types.ChatManage,
) bool {
	if chatManage == nil || len(chatManage.MergeResult) == 0 {
		return false
	}
	decision := assessRefusal(chatManage.MergeResult)
	if decision.Silent() {
		// 资料本身可用，再看 §13.5 的高风险覆盖率要求：高风险问题里只要
		// 有一条引用不是官方来源，读者就无法区分"必须遵守"与"仅供参考"。
		coverage := assessHighRiskCoverage(chatManage, chatManage.MergeResult)
		if coverage.RequiresCaveat {
			scenario := types.RefusalScenarios[types.EvidenceRefusalNoOfficialSource]
			chatManage.RefusalMarker = scenario.Marker
			chatManage.RefusalNotice = scenario.Message
			logger.Warnf(ctx,
				"high-risk answer carries %d/%d official sources for session %s (%s)",
				coverage.OfficialCount, coverage.CandidateCount,
				chatManage.SessionID, coverage.Reason)
			return false
		}
		chatManage.RefusalMarker = types.EvidenceRefusalNone
		chatManage.RefusalNotice = ""
		return false
	}
	chatManage.RefusalMarker = decision.Marker
	if decision.AllowsAnswer {
		chatManage.RefusalNotice = decision.Notice
		logger.Infof(ctx, "answer carries a §13.5 caveat (%s) for session %s",
			decision.Marker, chatManage.SessionID)
		return false
	}
	// 硬拒答：不进入生成阶段，用 fallback 回应并记录标记。
	chatManage.RefusalNotice = ""
	logger.Warnf(ctx, "answer refused by the §13.5 matrix (%s) for session %s: %d candidate(s)",
		decision.Marker, chatManage.SessionID, len(chatManage.MergeResult))
	s.handleFallbackResponse(ctx, chatManage)
	return true
}
