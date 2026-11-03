package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
)

// TestLearningTaskSourcesAndStatuses pins the §15.5 enums.
func TestLearningTaskSourcesAndStatuses(t *testing.T) {
	for _, source := range []types.LearningTaskSource{
		types.LearningTaskSourceSOPChange, types.LearningTaskSourceReviewCycle,
		types.LearningTaskSourceCertification, types.LearningTaskSourceManual,
	} {
		assert.True(t, source.Valid(), "%s must be valid", source)
	}
	assert.False(t, types.LearningTaskSource("bogus").Valid())

	for _, status := range []types.LearningTaskStatus{
		types.LearningTaskOpen, types.LearningTaskDone, types.LearningTaskWaived,
	} {
		assert.True(t, status.Valid(), "%s must be valid", status)
	}
	assert.False(t, types.LearningTaskStatus("bogus").Valid())
}

// TestExamQuestionValidation covers the shape rules that make a question
// gradable at all: a prompt, at least two options, and an in-range answer.
func TestExamQuestionValidation(t *testing.T) {
	valid := &types.LearningExamQuestion{
		Prompt:       "E45 报警首先检查什么？",
		Options:      types.JSON(`["BMS 通信线束","更换主板","重启整机"]`),
		CorrectIndex: 0,
	}
	require.NoError(t, valid.Valid())

	require.Error(t, (&types.LearningExamQuestion{}).Valid(), "prompt is required")
	require.Error(t, (&types.LearningExamQuestion{
		Prompt:  "x",
		Options: types.JSON(`["only one"]`),
	}).Valid(), "at least two options are required")
	require.Error(t, (&types.LearningExamQuestion{
		Prompt:       "x",
		Options:      types.JSON(`["a","b"]`),
		CorrectIndex: 5,
	}).Valid(), "an out-of-range answer must be rejected")
}

// TestGradeExamUsesTheBank is the anti-cheat property: scoring reads the
// question bank's correct index, never the submitted `correct` flag.
func TestGradeExamUsesTheBank(t *testing.T) {
	questions := []*types.LearningExamQuestion{
		{ID: "q1", Options: types.JSON(`["a","b"]`), CorrectIndex: 0},
		{ID: "q2", Options: types.JSON(`["a","b"]`), CorrectIndex: 1},
	}
	// Both answered right.
	score, answers := types.GradeExam(questions, map[string]int{"q1": 0, "q2": 1})
	assert.Equal(t, 100, score)
	require.Len(t, answers, 2)
	assert.True(t, answers[0].Correct)

	// One right, one wrong.
	score, _ = types.GradeExam(questions, map[string]int{"q1": 0, "q2": 0})
	assert.Equal(t, 50, score)

	// Both wrong.
	score, _ = types.GradeExam(questions, map[string]int{"q1": 1, "q2": 0})
	assert.Equal(t, 0, score)
}

// TestGradeExamHandlesUnanswered keeps a skipped question from counting as
// correct by accident.
func TestGradeExamHandlesUnanswered(t *testing.T) {
	questions := []*types.LearningExamQuestion{
		{ID: "q1", Options: types.JSON(`["a","b"]`), CorrectIndex: 0},
		{ID: "q2", Options: types.JSON(`["a","b"]`), CorrectIndex: 0},
	}
	score, answers := types.GradeExam(questions, map[string]int{"q1": 0})
	assert.Equal(t, 50, score, "an unanswered question is wrong, not skipped")
	require.Len(t, answers, 2)
	assert.False(t, answers[1].Correct)
}

// TestGradeExamEmptyBank covers the degenerate case.
func TestGradeExamEmptyBank(t *testing.T) {
	score, answers := types.GradeExam(nil, map[string]int{"q1": 0})
	assert.Zero(t, score)
	assert.Empty(t, answers)
}

// TestExamPassThreshold pins the §15.5 pass mark.
func TestExamPassThreshold(t *testing.T) {
	assert.Equal(t, 80, types.DefaultExamPassScore)
	// A score at the threshold passes, one below does not.
	assert.True(t, 80 >= types.DefaultExamPassScore)
	assert.False(t, 79 >= types.DefaultExamPassScore)
}
