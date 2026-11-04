package types

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// conversionPack builds an answered turn with citations.
func conversionPack(answer string) *EvidencePack {
	pack := &EvidencePack{
		RequestID: "req-1",
		Question:  "E45 设备报警怎么处理？",
		Answer:    answer,
	}
	pack.SetCitations([]EvidenceCitation{
		{Index: 1, KnowledgeID: "doc-1", ChunkID: "chunk-1", Title: "报警处理规程"},
		{Index: 2, KnowledgeID: "doc-2", ChunkID: "chunk-2", Title: "设备保养手册"},
		// A web citation has no knowledge id.
		{Index: 3, Title: "厂商 FAQ"},
	})
	return pack
}

// TestAnswerConversionEligibleRejectsRefusals is the "no content, no candidate"
// rule: a refusal has nothing to preserve.
func TestAnswerConversionEligibleRejectsRefusals(t *testing.T) {
	require.NoError(t, AnswerConversionEligible(conversionPack("按规程断电后复位。")))

	refused := conversionPack("按规程断电后复位。")
	refused.Refusal = string(EvidenceRefusalRefused)
	assert.Error(t, AnswerConversionEligible(refused))

	conflict := conversionPack("……")
	conflict.Refusal = string(EvidenceRefusalConflict)
	assert.Error(t, AnswerConversionEligible(conflict),
		"a conflict refusal is still a refusal")
}

// TestAnswerConversionEligibleRejectsEmptyAnswer covers the blank case.
func TestAnswerConversionEligibleRejectsEmptyAnswer(t *testing.T) {
	assert.Error(t, AnswerConversionEligible(conversionPack("   ")))
	assert.Error(t, AnswerConversionEligible(nil))
}

// TestValidateAnswerConversionInput covers the request shape.
func TestValidateAnswerConversionInput(t *testing.T) {
	requestID, err := ValidateAnswerConversionInput(AnswerConversionInput{
		RequestID: "  req-1  ",
	})
	require.NoError(t, err)
	assert.Equal(t, "req-1", requestID)

	_, err = ValidateAnswerConversionInput(AnswerConversionInput{})
	require.Error(t, err, "request_id is the only required field")

	_, err = ValidateAnswerConversionInput(AnswerConversionInput{
		RequestID: "req-1", Level: "L9",
	})
	require.Error(t, err, "an unknown level is rejected, not defaulted")

	long := strings.Repeat("标", AnswerConversionMaxTitleRunes+1)
	_, err = ValidateAnswerConversionInput(AnswerConversionInput{
		RequestID: "req-1", Title: long,
	})
	require.Error(t, err)
}

// TestAnswerConversionTitleDerivesFromQuestion keeps the title readable.
func TestAnswerConversionTitleDerivesFromQuestion(t *testing.T) {
	assert.Equal(t, "E45 设备报警怎么处理",
		AnswerConversionTitle("  E45 设备报警怎么处理？ ", ""))

	// An override wins and is trimmed.
	assert.Equal(t, "自定义标题",
		AnswerConversionTitle("E45 设备报警怎么处理？", "  自定义标题  "))

	// A blank question still yields a usable title rather than an empty one.
	assert.Equal(t, "未命名问答经验", AnswerConversionTitle("   ", ""))

	// Long questions are truncated at the limit, not rejected.
	long := strings.Repeat("题", AnswerConversionMaxTitleRunes+10)
	title := AnswerConversionTitle(long, "")
	assert.Len(t, []rune(title), AnswerConversionMaxTitleRunes)
}

// TestBuildAnswerConversionCardCarriesAnswerAndScope covers the card shape.
func TestBuildAnswerConversionCardCarriesAnswerAndScope(t *testing.T) {
	card := BuildAnswerConversionCard(conversionPack("按规程断电后复位。"), false)
	assert.Equal(t, "按规程断电后复位。", card.What)
	assert.Contains(t, card.Scope, "报警处理规程")
	assert.Contains(t, card.Scope, "设备保养手册")
	// Fields that need human input stay empty — no invented content.
	assert.Empty(t, card.Phenomenon)
	assert.Empty(t, card.CauseCandidate)
	assert.Empty(t, card.Measures)
	assert.Empty(t, card.Validation)
}

// TestBuildAnswerConversionCardMarksTruncation is the honesty rule for a long
// answer: the card says it was cut and where the rest lives.
func TestBuildAnswerConversionCardMarksTruncation(t *testing.T) {
	pack := conversionPack("短答案")
	card := BuildAnswerConversionCard(pack, true)
	assert.Contains(t, card.What, "已截断")
	assert.Contains(t, card.What, "req-1")
}

// TestBuildAnswerConversionCardWithoutCitationsStillScopes keeps the card
// non-empty when the answer carried no evidence.
func TestBuildAnswerConversionCardWithoutCitationsStillScopes(t *testing.T) {
	pack := &EvidencePack{RequestID: "req-2", Answer: "直接结论"}
	card := BuildAnswerConversionCard(pack, false)
	assert.Equal(t, "直接结论", card.What)
	assert.Contains(t, card.Scope, "无引用证据")
}

// TestBuildAnswerConversionEvidenceKeepsKnowledgeIDs is the §2.4 requirement:
// the source document ids must survive so derived permission can be computed.
func TestBuildAnswerConversionEvidenceKeepsKnowledgeIDs(t *testing.T) {
	evidence := BuildAnswerConversionEvidence(conversionPack("答案"))
	require.Len(t, evidence, 3)

	assert.Equal(t, "doc-1", evidence[0].SourceID)
	assert.Equal(t, "chunk-1", evidence[0].Locator)
	assert.Equal(t, "报警处理规程", evidence[0].Quote)
	assert.Equal(t, string(KnowledgeCandidateSourceQAAnswer), evidence[0].SourceType)

	// The web citation keeps its title but has no source id to inherit from.
	assert.Equal(t, "", evidence[2].SourceID)
	assert.Equal(t, "厂商 FAQ", evidence[2].Quote)
}

// TestBuildAnswerConversionEvidenceDropsEmptyRefs keeps blank rows out.
func TestBuildAnswerConversionEvidenceDropsEmptyRefs(t *testing.T) {
	pack := &EvidencePack{RequestID: "req-3", Answer: "答案"}
	pack.SetCitations([]EvidenceCitation{{Index: 1}, {Index: 2, Title: "有标题"}})
	evidence := BuildAnswerConversionEvidence(pack)
	require.Len(t, evidence, 1)
	assert.Equal(t, "有标题", evidence[0].Quote)
}

// TestTruncateAnswerForConversion covers both sides of the limit.
func TestTruncateAnswerForConversion(t *testing.T) {
	short := "短答案"
	out, truncated := TruncateAnswerForConversion(short)
	assert.Equal(t, short, out)
	assert.False(t, truncated)

	long := strings.Repeat("字", AnswerConversionMaxAnswerRunes+50)
	out, truncated = TruncateAnswerForConversion(long)
	assert.True(t, truncated)
	assert.Len(t, []rune(out), AnswerConversionMaxAnswerRunes)
}

// TestAnswerConversionSourceTypeIsValid proves the new source joins the
// whitelist rather than being rejected by CreateCandidate.
func TestAnswerConversionSourceTypeIsValid(t *testing.T) {
	assert.True(t, KnowledgeCandidateSourceQAAnswer.Valid())
	assert.Equal(t, "qa_answer", string(KnowledgeCandidateSourceQAAnswer))
}

// TestAnswerConversionEvidenceCarriesLocatorAndTitle pins the mapping used by
// the publish path when it re-checks derived permission.
func TestAnswerConversionEvidenceCarriesLocatorAndTitle(t *testing.T) {
	pack := &EvidencePack{
		RequestID: "req-4", Answer: "答案",
		CreatedAt: time.Now().UTC(),
	}
	pack.SetCitations([]EvidenceCitation{
		{Index: 1, KnowledgeID: "doc-9", ChunkID: "chunk-9", Title: "机密配方说明"},
	})
	evidence := BuildAnswerConversionEvidence(pack)
	require.Len(t, evidence, 1)
	assert.Equal(t, "doc-9", evidence[0].SourceID)
	assert.Equal(t, "chunk-9", evidence[0].Locator)
	assert.Equal(t, "机密配方说明", evidence[0].Quote)
}
