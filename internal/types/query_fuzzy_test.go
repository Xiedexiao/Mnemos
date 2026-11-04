package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEditDistanceCountsRunesNotBytes is the core correctness point for CJK:
// one replaced character is distance 1, not 3.
func TestEditDistanceCountsRunesNotBytes(t *testing.T) {
	assert.Equal(t, 0, EditDistance("报警", "报警"))
	assert.Equal(t, 1, EditDistance("报警", "报井"), "one substituted character")
	assert.Equal(t, 2, EditDistance("报警处理", "报井处埋"), "two substitutions")
	assert.Equal(t, 1, EditDistance("报警", "报警器"), "one insertion")
	assert.Equal(t, 1, EditDistance("报警器", "报警"), "one deletion")
	assert.Equal(t, 4, EditDistance("", "报警处理"))
	assert.Equal(t, 1, EditDistance("E45", "E450"), "one appended character")
}

// TestEditDistanceIsSymmetric guards against an asymmetric implementation
// (the rolling array swaps operands).
func TestEditDistanceIsSymmetric(t *testing.T) {
	pairs := [][2]string{
		{"报警", "报井"}, {"备件清单", "备件清丹"}, {"E45", "e45"},
		{"处理", "处理流程"}, {"", "abc"},
	}
	for _, pair := range pairs {
		assert.Equal(t, EditDistance(pair[0], pair[1]), EditDistance(pair[1], pair[0]),
			"%q vs %q", pair[0], pair[1])
	}
}

// TestPinyinOfKnownCharacters pins a handful of readings against the embedded
// table, so a regenerated table that loses coverage fails loudly.
func TestPinyinOfKnownCharacters(t *testing.T) {
	assert.Contains(t, PinyinOf('报'), "bao")
	assert.Contains(t, PinyinOf('警'), "jing")
	assert.Contains(t, PinyinOf('备'), "bei")
	assert.Contains(t, PinyinOf('件'), "jian")
	// A non-Han rune has no reading.
	assert.Empty(t, PinyinOf('E'))
	assert.Empty(t, PinyinOf('4'))
}

// TestPinyinOfTextJoinsSyllables is the input users actually type.
func TestPinyinOfTextJoinsSyllables(t *testing.T) {
	assert.Equal(t, "baojing", PinyinOfText("报警"))
	assert.Equal(t, "baojingchuli", PinyinOfText("报警处理"))
	// Latin and digits survive so an identifier in the query is not lost.
	assert.Equal(t, "e45baojing", PinyinOfText("E45 报警"))
	assert.Equal(t, "", PinyinOfText(""))
}

// TestPinyinOfPolyphoneKeepsBothReadings covers the 重/长/乐 cases; the
// generated table records the common reading and the name reading where the
// two ICU transforms disagree.
//
// 覆盖边界如实记录：两种转写只对 27 个字给出不同读音，所以"行"这类两种
// 转写一致的常见多音字仍只有一条（xing）。多音字召回是尽力而为，不是完备。
func TestPinyinOfPolyphoneKeepsBothReadings(t *testing.T) {
	assert.Len(t, PinyinOf('重'), 2, "chong and zhong are both valid")
	assert.Contains(t, PinyinOf('长'), "chang")
	assert.Contains(t, PinyinOf('长'), "zhang")
	assert.Equal(t, []string{"xing"}, PinyinOf('行'),
		"both transforms agree on 行, so only one reading is recorded")
}

// TestPinyinSyllablesPreservesLatinSegments keeps "E45" as one segment.
func TestPinyinSyllablesPreservesLatinSegments(t *testing.T) {
	assert.Equal(t, []string{"e45", "bao", "jing"}, PinyinSyllables("E45 报警"))
	assert.Equal(t, []string{"bao", "jing"}, PinyinSyllables("报警"))
	assert.Empty(t, PinyinSyllables("   "))
}

// TestFuzzyTokenizeSplitsHanIntoBigrams documents the granularity choice.
func TestFuzzyTokenizeSplitsHanIntoBigrams(t *testing.T) {
	assert.Equal(t, []string{"报警", "警处", "处理"}, FuzzyTokenize("报警处理"))
	assert.Equal(t, []string{"e45"}, FuzzyTokenize("E45"))
	// A lone Han character survives so single-character vocabularies still work.
	assert.Equal(t, []string{"报"}, FuzzyTokenize("报"))
	assert.Empty(t, FuzzyTokenize("   "))
}

// TestVocabularyPinyinMatchesFullAndPrefix covers §12.1 拼音搜索.
func TestVocabularyPinyinMatchesFullAndPrefix(t *testing.T) {
	vocabulary := []string{"报警处理", "备件清单", "工单流程"}

	assert.Equal(t, []string{"报警处理"}, VocabularyPinyinMatches("baojing", vocabulary, 4))
	// Separators and casing are irrelevant.
	assert.Equal(t, []string{"报警处理"}, VocabularyPinyinMatches("Bao Jing", vocabulary, 4))
	// A partial spelling still matches: the user may not have finished typing.
	assert.Equal(t, []string{"报警处理"}, VocabularyPinyinMatches("baoj", vocabulary, 4))
	// Full pinyin of the whole term.
	assert.Equal(t, []string{"报警处理"}, VocabularyPinyinMatches("baojingchuli", vocabulary, 4))
}

// TestVocabularyPinyinMatchesIgnoresNonHanTerms keeps Latin vocabulary out of
// the pinyin channel — it already matches itself.
func TestVocabularyPinyinMatchesIgnoresNonHanTerms(t *testing.T) {
	assert.Empty(t, VocabularyPinyinMatches("e45", []string{"E45", "SOP"}, 4))
}

// TestVocabularyPinyinMatchesRequiresEnoughInput refuses one-letter guesses.
func TestVocabularyPinyinMatchesRequiresEnoughInput(t *testing.T) {
	assert.Empty(t, VocabularyPinyinMatches("b", []string{"报警处理"}, 4))
	assert.Empty(t, VocabularyPinyinMatches("", []string{"报警处理"}, 4))
}

// TestVocabularyPinyinMatchesReturnsNothingForNonLatin keeps a Chinese query
// from being run through the pinyin channel.
func TestVocabularyPinyinMatchesReturnsNothingForNonLatin(t *testing.T) {
	assert.Empty(t, VocabularyPinyinMatches("报警", []string{"报警处理"}, 4))
}

// TestSuggestCorrectionsFindsHomophone is the headline case: 报井 → 报警.
func TestSuggestCorrectionsFindsHomophone(t *testing.T) {
	corrections := SuggestCorrections("报井处理", []string{"报警处理", "备件清单"}, 5)
	require.NotEmpty(t, corrections)
	assert.Equal(t, "报井", corrections[0].Original)
	assert.Equal(t, "报警", corrections[0].Suggestion)
	assert.Equal(t, 1, corrections[0].Distance)
	assert.Equal(t, FuzzyReasonHomophone, corrections[0].Reason)
}

// TestSuggestCorrectionsSkipsIdentifiers is the §12.1 "编号精确" rule: E45 must
// never be "corrected" into E46.
func TestSuggestCorrectionsSkipsIdentifiers(t *testing.T) {
	corrections := SuggestCorrections("E45 报警", []string{"E46 报警", "E450 报警"}, 5)
	for _, correction := range corrections {
		assert.NotEqual(t, "e45", correction.Original, "identifiers are not corrected")
	}
	// And a pure identifier query yields nothing at all.
	assert.Empty(t, SuggestCorrections("E45", []string{"E46"}, 5))
}

// TestSuggestCorrectionsRespectsThreshold keeps distant terms out.
func TestSuggestCorrectionsRespectsThreshold(t *testing.T) {
	// "报警" vs "工单" is distance 2 but shares no characters; for a 2-rune
	// token the threshold is 1, so it must not be suggested.
	corrections := SuggestCorrections("工单", []string{"报警"}, 5)
	assert.Empty(t, corrections)
}

// TestSuggestCorrectionsPrefersHomophoneOverShapeMatch pins the tie-break:
// 同一查询词可能有两个同距离候选，同音的那条更可能是用户想打的字。
func TestSuggestCorrectionsPrefersHomophoneOverShapeMatch(t *testing.T) {
	// "报井" → "报竟" is distance 1 and a homophone (baojing);
	// "报井" → "报开" is also distance 1 but not a homophone (baokai).
	corrections := SuggestCorrections("报井", []string{"报开", "报竟"}, 5)
	require.NotEmpty(t, corrections)
	assert.Equal(t, "报竟", corrections[0].Suggestion,
		"an equal-distance homophone outranks a shape-only match")
	assert.Equal(t, FuzzyReasonHomophone, corrections[0].Reason)
}

// TestSuggestCorrectionsBreaksHomophoneTiesDeterministically covers the case
// where two candidates are equally good (both homophones, same distance):
// without frequency data the only defensible order is a stable one.
func TestSuggestCorrectionsBreaksHomophoneTiesDeterministically(t *testing.T) {
	corrections := SuggestCorrections("报井", []string{"报警", "报京"}, 5)
	require.Len(t, corrections, 2)
	for _, correction := range corrections {
		assert.Equal(t, 1, correction.Distance)
		assert.Equal(t, FuzzyReasonHomophone, correction.Reason)
	}
	assert.Equal(t, "报京", corrections[0].Suggestion, "codepoint order, reproducible")
	assert.Equal(t, "报警", corrections[1].Suggestion)
}

// TestSuggestCorrectionsIsDeterministic proves the same input yields the same
// order (map iteration must not leak into the result).
func TestSuggestCorrectionsIsDeterministic(t *testing.T) {
	vocabulary := []string{"报警处理", "报警记录", "报警设置", "备件清单"}
	first := SuggestCorrections("报井处理", vocabulary, 5)
	for i := 0; i < 5; i++ {
		assert.Equal(t, first, SuggestCorrections("报井处理", vocabulary, 5))
	}
}

// TestSuggestCorrectionsWithoutVocabulary returns nothing rather than guessing.
func TestSuggestCorrectionsWithoutVocabulary(t *testing.T) {
	assert.Empty(t, SuggestCorrections("报井处理", nil, 5))
	assert.Empty(t, SuggestCorrections("", []string{"报警"}, 5))
}

// TestBuildQueryRewriteKeepsOriginalAndAddsAlternatives is the contract: the
// original is always preserved, alternatives are additions.
func TestBuildQueryRewriteKeepsOriginalAndAddsAlternatives(t *testing.T) {
	rewrite := BuildQueryRewrite("报井处理", []string{"报警处理"}, 5)
	require.NotNil(t, rewrite)
	assert.Equal(t, "报井处理", rewrite.Original)
	assert.Equal(t, "baojingchuli", rewrite.Pinyin)
	assert.Equal(t, []string{"报警处理"}, rewrite.Alternatives,
		"both token corrections collapse into one alternative sentence")
}

// TestBuildQueryRewriteAddsPinyinCandidates covers the Latin-input path.
func TestBuildQueryRewriteAddsPinyinCandidates(t *testing.T) {
	rewrite := BuildQueryRewrite("baojing", []string{"报警处理", "备件清单"}, 5)
	assert.Equal(t, []string{"报警处理"}, rewrite.ChineseFromPinyin)
	assert.Equal(t, []string{"报警处理"}, rewrite.Alternatives)
}

// TestBuildQueryRewriteDropsDuplicateOfOriginal keeps "报警" from being listed
// as an alternative to itself.
func TestBuildQueryRewriteDropsDuplicateOfOriginal(t *testing.T) {
	rewrite := BuildQueryRewrite("报警", []string{"报警"}, 5)
	assert.Empty(t, rewrite.Alternatives)
}

// TestBuildQueryRewriteEmptyQuery is the no-op case.
func TestBuildQueryRewriteEmptyQuery(t *testing.T) {
	rewrite := BuildQueryRewrite("   ", []string{"报警"}, 5)
	assert.Equal(t, "", rewrite.Original)
	assert.Empty(t, rewrite.Alternatives)
	assert.Empty(t, rewrite.Corrections)
}

// TestBuildQueryRewriteReplacesOnlyTheToken keeps the rest of the query: a
// whole-sentence replacement would drop the user's other constraints.
func TestBuildQueryRewriteReplacesOnlyTheToken(t *testing.T) {
	rewrite := BuildQueryRewrite("E45 报井处理", []string{"报警处理"}, 5)
	require.NotEmpty(t, rewrite.Alternatives)
	assert.Contains(t, rewrite.Alternatives, "E45 报警处理",
		"the equipment number stays in front of the corrected phrase")
}

// TestPinyinTableCoverage is a guard rail on the generated table: it must know
// the vocabulary this product actually uses.
func TestPinyinTableCoverage(t *testing.T) {
	for _, term := range []string{
		"报警", "备件", "工单", "流程", "设备", "保养", "制度", "审批", "知识", "培训",
	} {
		assert.NotEmpty(t, PinyinOfText(term), "pinyin table has no reading for %q", term)
	}
}
