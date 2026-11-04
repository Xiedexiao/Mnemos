package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestAnswerSimilarityIdentical allows formatting differences: the score is
// about content overlap, not byte equality.
func TestAnswerSimilarityIdentical(t *testing.T) {
	assert.Equal(t, 1.0, AnswerSimilarity("先检查 BMS 通信线束，再复位。", "先检查 BMS 通信线束，再复位。"))
	assert.Equal(t, 1.0, AnswerSimilarity("先检查 BMS 通信线束", "  先检查 BMS 通信线束！ "))
	assert.Equal(t, 1.0, AnswerSimilarity("check the BMS harness", "Check The BMS Harness"))
}

// TestAnswerSimilarityPartial covers a partially correct answer.
func TestAnswerSimilarityPartial(t *testing.T) {
	reference := "先检查 BMS 通信线束，确认无松动后复位"
	actual := "先检查 BMS 通信线束"
	score := AnswerSimilarity(reference, actual)
	assert.Greater(t, score, 0.5)
	assert.Less(t, score, 1.0, "a truncated answer must not score as complete")
}

// TestAnswerSimilarityContradictionStillScores is an honest limitation: the
// metric is lexical, so a negated answer that reuses the same words scores
// high. It is a regression gate, not a correctness oracle, and the test pins
// that so nobody mistakes it for one.
func TestAnswerSimilarityContradictionStillScores(t *testing.T) {
	score := AnswerSimilarity("必须先停机再开盖", "不必停机直接开盖")
	assert.Greater(t, score, 0.5,
		"token overlap cannot detect negation; human sampling remains required")
}

// TestAnswerSimilarityEmpty covers the degenerate cases.
func TestAnswerSimilarityEmpty(t *testing.T) {
	assert.Equal(t, 0.0, AnswerSimilarity("", "有内容"))
	assert.Equal(t, 0.0, AnswerSimilarity("有内容", ""))
	assert.Equal(t, 1.0, AnswerSimilarity("", ""))
}

// TestAnswerSimilarityChineseTokenization keeps single CJK characters as
// tokens, so Chinese answers are comparable without a segmenter.
func TestAnswerSimilarityChineseTokenization(t *testing.T) {
	assert.Equal(t, 1.0, AnswerSimilarity("停机检查", "停机检查"))
	assert.Less(t, AnswerSimilarity("停机检查", "检查"), 1.0)
	// Digits and latin runs stay whole.
	assert.Equal(t, 1.0, AnswerSimilarity("E45 报警", "E45报警"))
}
