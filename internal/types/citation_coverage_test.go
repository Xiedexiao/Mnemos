package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// officialHit builds a result that counts as an official source.
func officialHit(level, lifecycle string) *SearchResult {
	return &SearchResult{
		KnowledgeID:              "doc-" + level + "-" + lifecycle,
		KnowledgeLifecycleStatus: lifecycle,
		KnowledgeCustomMetadata:  "knowaxis_level: " + level,
	}
}

// TestAssessAnswerRiskHighMarkers covers the decision-consequence vocabulary.
func TestAssessAnswerRiskHighMarkers(t *testing.T) {
	for _, question := range []string{
		"这笔报销金额能不能批",
		"这个操作是否合规",
		"发生事故后要停产吗",
		"能不能辞退试用期员工",
		"合同报价可以打几折",
		"审计发现问题如何整改",
	} {
		level, markers := AssessAnswerRisk(question)
		assert.Equal(t, AnswerRiskHigh, level, question)
		assert.NotEmpty(t, markers, "the level must be explainable: %s", question)
	}
}

// TestAssessAnswerRiskMediumMarkers covers governed-process questions.
func TestAssessAnswerRiskMediumMarkers(t *testing.T) {
	level, markers := AssessAnswerRisk("设备变更的审批流程是什么")
	assert.Equal(t, AnswerRiskMedium, level)
	assert.Contains(t, markers, "审批")
}

// TestAssessAnswerRiskLowForOrdinaryQuestions keeps the default cheap.
func TestAssessAnswerRiskLowForOrdinaryQuestions(t *testing.T) {
	level, markers := AssessAnswerRisk("E45 报警灯是什么意思")
	assert.Equal(t, AnswerRiskLow, level)
	assert.Empty(t, markers)

	// Empty questions are low, not high: there is nothing to classify.
	level, markers = AssessAnswerRisk("   ")
	assert.Equal(t, AnswerRiskLow, level)
	assert.Empty(t, markers)
}

// TestAssessAnswerRiskHighWinsOverMedium is the "更严的一档优先" rule.
func TestAssessAnswerRiskHighWinsOverMedium(t *testing.T) {
	// Carries both a medium marker (审批) and a high one (金额).
	level, markers := AssessAnswerRisk("审批流程里金额上限是多少")
	assert.Equal(t, AnswerRiskHigh, level)
	assert.Contains(t, markers, "金额")
	assert.NotContains(t, markers, "审批",
		"only the winning tier's markers are reported")
}

// TestIsOfficialSourceAcceptsL2AndL3 pins the definition.
func TestIsOfficialSourceAcceptsL2AndL3(t *testing.T) {
	assert.True(t, IsOfficialSource("L3"))
	assert.True(t, IsOfficialSource("L2"))
	assert.False(t, IsOfficialSource("L1"))
	assert.False(t, IsOfficialSource("L0"))
	assert.False(t, IsOfficialSource(""), "unlabelled uploads are not official")
	assert.False(t, IsOfficialSource("nonsense"))
}

// TestAssessCitationCoverageHighRiskRequiresEverything is the §13.5 rule.
func TestAssessCitationCoverageHighRiskRequiresEverything(t *testing.T) {
	levels := map[string]string{
		"doc-L3-valid": "L3",
		"doc-L1-valid": "L1",
	}
	results := []*SearchResult{
		officialHit("L3", "valid"),
		officialHit("L1", "valid"),
	}
	coverage := AssessCitationCoverage("这笔付款是否合规", results,
		func(hit *SearchResult) string { return levels[hit.KnowledgeID] })

	assert.Equal(t, AnswerRiskHigh, coverage.RiskLevel)
	assert.Equal(t, 2, coverage.CandidateCount)
	assert.Equal(t, 1, coverage.OfficialCount)
	assert.InDelta(t, 0.5, coverage.Coverage, 0.001)
	assert.False(t, coverage.MeetsRequirement,
		"one non-official source breaks the 100% requirement")
	assert.True(t, coverage.RequiresCaveat)
	assert.Contains(t, coverage.Reason, "1 of 2")
}

// TestAssessCitationCoverageHighRiskFullyOfficialPasses is the positive control.
func TestAssessCitationCoverageHighRiskFullyOfficialPasses(t *testing.T) {
	results := []*SearchResult{officialHit("L3", "valid"), officialHit("L2", "valid")}
	coverage := AssessCitationCoverage("这笔付款是否合规", results,
		func(hit *SearchResult) string {
			if hit.KnowledgeID == "doc-L3-valid" {
				return "L3"
			}
			return "L2"
		})
	assert.True(t, coverage.MeetsRequirement)
	assert.InDelta(t, 1.0, coverage.Coverage, 0.001)
	assert.False(t, coverage.RequiresCaveat)
	assert.Empty(t, coverage.Reason)
}

// TestAssessCitationCoverageStaleL2IsNotOfficial is the lifecycle gate: an
// unconfirmed L2 cannot serve as an official source.
func TestAssessCitationCoverageStaleL2IsNotOfficial(t *testing.T) {
	results := []*SearchResult{officialHit("L2", "stale")}
	coverage := AssessCitationCoverage("是否合规", results,
		func(*SearchResult) string { return "L2" })
	assert.Equal(t, 0, coverage.OfficialCount,
		"a pending-review L2 has not been confirmed either")
	assert.False(t, coverage.MeetsRequirement)
	assert.True(t, coverage.RequiresCaveat)
}

// TestAssessCitationCoverageLowRiskHasNoRequirement keeps low risk cheap.
func TestAssessCitationCoverageLowRiskHasNoRequirement(t *testing.T) {
	results := []*SearchResult{officialHit("L1", "valid")}
	coverage := AssessCitationCoverage("E45 报警灯是什么意思", results,
		func(*SearchResult) string { return "L1" })
	assert.Equal(t, AnswerRiskLow, coverage.RiskLevel)
	assert.True(t, coverage.MeetsRequirement,
		"a low-risk question may be answered from reference material")
	assert.False(t, coverage.RequiresCaveat)
	assert.Zero(t, coverage.Coverage, "no official source means zero coverage")
}

// TestAssessCitationCoverageMediumIsNotGated documents the middle tier.
func TestAssessCitationCoverageMediumIsNotGated(t *testing.T) {
	results := []*SearchResult{officialHit("L1", "valid")}
	coverage := AssessCitationCoverage("变更审批流程是什么", results,
		func(*SearchResult) string { return "L1" })
	assert.Equal(t, AnswerRiskMedium, coverage.RiskLevel)
	assert.True(t, coverage.MeetsRequirement)
	assert.False(t, coverage.RequiresCaveat)
}

// TestAssessCitationCoverageWithoutHits covers the empty case: there is nothing
// to caveat (the no-evidence refusal already handles it).
func TestAssessCitationCoverageWithoutHits(t *testing.T) {
	coverage := AssessCitationCoverage("是否合规", nil,
		func(*SearchResult) string { return "" })
	assert.Equal(t, 0, coverage.CandidateCount)
	assert.Zero(t, coverage.Coverage)
	assert.False(t, coverage.MeetsRequirement,
		"a high-risk question with no source cannot meet a 100% requirement")
	assert.False(t, coverage.RequiresCaveat,
		"the no-evidence path already refuses; a second caveat would be noise")
}

// TestAssessCitationCoverageSkipsNilHits keeps a nil row from counting.
func TestAssessCitationCoverageSkipsNilHits(t *testing.T) {
	coverage := AssessCitationCoverage("是否合规",
		[]*SearchResult{nil, officialHit("L3", "valid")},
		func(hit *SearchResult) string {
			if hit == nil {
				return ""
			}
			return "L3"
		})
	assert.Equal(t, 1, coverage.CandidateCount)
	assert.Equal(t, 1, coverage.OfficialCount)
	assert.True(t, coverage.MeetsRequirement)
}

// TestAssessCitationCoverageDuplicateHitsCountTwice proves coverage is per
// citation, not per document: citing the same reference material twice does not
// dilute the official share, and vice versa.
func TestAssessCitationCoverageDuplicateHitsCountTwice(t *testing.T) {
	unofficial := &SearchResult{KnowledgeID: "doc-L1", KnowledgeLifecycleStatus: "valid"}
	coverage := AssessCitationCoverage("是否合规",
		[]*SearchResult{officialHit("L3", "valid"), unofficial, unofficial},
		func(hit *SearchResult) string {
			if hit.KnowledgeID == "doc-L1" {
				return "L1"
			}
			return "L3"
		})
	assert.Equal(t, 3, coverage.CandidateCount)
	assert.Equal(t, 1, coverage.OfficialCount)
	require.InDelta(t, 1.0/3.0, coverage.Coverage, 0.001)
}

// TestAnswerRiskLevelValid covers the enum guard.
func TestAnswerRiskLevelValid(t *testing.T) {
	for _, level := range []AnswerRiskLevel{AnswerRiskLow, AnswerRiskMedium, AnswerRiskHigh} {
		assert.True(t, level.Valid())
	}
	assert.False(t, AnswerRiskLevel("critical").Valid())
}
