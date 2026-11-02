package service

import (
	"context"

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
