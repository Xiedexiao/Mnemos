package types

import (
	"math"
	"strings"
	"unicode"
)

// AnswerSimilarity scores a generated answer against the reference answer
// (KnowAxis V3.0 §14.8 答案质量评测).
//
// 口径是 token 级 F1（SQuAD 风格），而不是 embedding 相似度：
//   - 可解释：读者能看到"命中了哪些词、漏了哪些词"；
//   - 无需模型：评测在 CI/离线都能跑，不依赖外部服务可用性；
//   - 中文友好：按"汉字单字 + 连续字母数字串"切分，不依赖分词器。
//
// 它衡量的是内容重合度，不是语义正确性——把"必须停机"答成"不必停机"仍会
// 得高分。因此它是回归门禁（"答案没被改坏"），不能替代人工抽检。
func AnswerSimilarity(expected, actual string) float64 {
	expectedTokens := answerTokens(expected)
	actualTokens := answerTokens(actual)
	if len(expectedTokens) == 0 && len(actualTokens) == 0 {
		return 1
	}
	if len(expectedTokens) == 0 || len(actualTokens) == 0 {
		return 0
	}
	expectedCounts := make(map[string]int, len(expectedTokens))
	for _, token := range expectedTokens {
		expectedCounts[token]++
	}
	actualCounts := make(map[string]int, len(actualTokens))
	overlap := 0
	for _, token := range actualTokens {
		actualCounts[token]++
		if actualCounts[token] <= expectedCounts[token] {
			overlap++
		}
	}
	if overlap == 0 {
		return 0
	}
	precision := float64(overlap) / float64(len(actualTokens))
	recall := float64(overlap) / float64(len(expectedTokens))
	if precision+recall == 0 {
		return 0
	}
	f1 := 2 * precision * recall / (precision + recall)
	// 浮点误差会让完全相同的短答案得到 0.9999999；把 1 附近收敛到 1。
	if math.Abs(f1-1) < 1e-9 {
		return 1
	}
	return f1
}

// answerTokens splits text into comparable units: one token per CJK rune and
// one per contiguous alphanumeric run, lowercased. Punctuation and whitespace
// are separators, so formatting differences do not change the score.
func answerTokens(text string) []string {
	tokens := make([]string, 0, len(text)/2)
	var word strings.Builder
	flush := func() {
		if word.Len() == 0 {
			return
		}
		tokens = append(tokens, strings.ToLower(word.String()))
		word.Reset()
	}
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
			unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r):
			flush()
			tokens = append(tokens, string(r))
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			word.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return tokens
}

// AnswerSimilarityThreshold is the default release gate for answer quality.
//
// 0.6 是"内容基本重合"的保守下限：同一份 SOP 的不同措辞通常能过，把结论
// 说反或漏掉关键步骤过不了。真实阈值应由每个租户在自己的语料上校准。
const AnswerSimilarityThreshold = 0.6

// RefusalCorrectnessThreshold is the default release gate for refusals.
const RefusalCorrectnessThreshold = 0.9
