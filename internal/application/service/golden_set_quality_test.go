package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// stubGoldenRepo is an in-memory golden-set store.
type stubGoldenRepo struct {
	interfaces.GoldenSetRepository
	set       *types.GoldenSet
	questions []*types.GoldenQuestion
	runs      []*types.GoldenSetRun
}

func (s *stubGoldenRepo) GetSet(
	_ context.Context, _ uint64, setID string,
) (*types.GoldenSet, error) {
	if s.set == nil || s.set.ID != setID {
		return nil, assert.AnError
	}
	return s.set, nil
}

func (s *stubGoldenRepo) ListQuestions(
	_ context.Context, _ string, enabledOnly bool,
) ([]*types.GoldenQuestion, error) {
	out := make([]*types.GoldenQuestion, 0, len(s.questions))
	for _, question := range s.questions {
		if enabledOnly && !question.Enabled {
			continue
		}
		out = append(out, question)
	}
	return out, nil
}

func (s *stubGoldenRepo) CreateRun(_ context.Context, run *types.GoldenSetRun) error {
	run.ID = "run-1"
	return nil
}

func (s *stubGoldenRepo) UpdateRun(_ context.Context, run *types.GoldenSetRun) error {
	s.runs = append(s.runs, run)
	return nil
}

func (s *stubGoldenRepo) GetLatestRun(
	_ context.Context, _ uint64, _ string, _ types.GoldenSetRunStatus,
) (*types.GoldenSetRun, error) {
	if len(s.runs) == 0 {
		return nil, assert.AnError
	}
	return s.runs[len(s.runs)-1], nil
}

func (s *stubGoldenRepo) ListSets(
	_ context.Context, _ uint64, _ string,
) ([]*types.GoldenSet, error) {
	if s.set == nil {
		return nil, nil
	}
	return []*types.GoldenSet{s.set}, nil
}

// stubGoldenKB serves HybridSearch.
type stubGoldenKB struct {
	interfaces.KnowledgeBaseService
	results []*types.SearchResult
	err     error
}

func (s *stubGoldenKB) HybridSearch(
	context.Context, string, types.SearchParams,
) ([]*types.SearchResult, error) {
	return s.results, s.err
}

func newGoldenFixture(questions []*types.GoldenQuestion, results []*types.SearchResult) (
	*goldenSetService, *stubGoldenRepo,
) {
	repo := &stubGoldenRepo{
		set:       &types.GoldenSet{ID: "set-1", TenantID: 7, Name: "门禁集", KBID: "kb-1"},
		questions: questions,
	}
	svc := &goldenSetService{
		repo:        repo,
		kbService:   &stubGoldenKB{results: results},
		now:         func() time.Time { return time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC) },
		gateTop5Hit: DefaultGateTop5Hit,
		gateRefusal: types.RefusalCorrectnessThreshold,
		gateAnswer:  types.AnswerSimilarityThreshold,
	}
	return svc, repo
}

func goldenQuestion(id string) *types.GoldenQuestion {
	return &types.GoldenQuestion{
		ID: id, SetID: "set-1",
		Question:             "E45 报警怎么处理？",
		ExpectedEvidence:     types.JSON(`[{"knowledge_id":"k1"}]`),
		FreshnessExpectation: string(types.GoldenFreshnessValid),
		Enabled:              true,
	}
}

// TestEvaluateScoresAnswerQuality covers the new answer metric: the assembled
// retrieved text is compared against expected_answer.
func TestEvaluateScoresAnswerQuality(t *testing.T) {
	question := goldenQuestion("q1")
	question.ExpectedAnswer = "先检查 BMS 通信线束，确认无松动后复位"
	svc, repo := newGoldenFixture([]*types.GoldenQuestion{question}, []*types.SearchResult{
		{KnowledgeID: "k1", Content: "先检查 BMS 通信线束，确认无松动后复位",
			KnowledgeLifecycleStatus: string(types.KnowledgeLifecycleValid),
			KnowledgeClassification:  string(types.KnowledgeClassificationInternal)},
	})

	_, err := svc.RunEvaluation(context.Background(), 7, "set-1", "u-1")
	require.NoError(t, err)
	require.Len(t, repo.runs, 1)
	metrics := runMetrics(t, repo.runs[0])
	require.NotNil(t, metrics.AnswerAccuracy)
	assert.Equal(t, 1.0, *metrics.AnswerAccuracy)
	assert.Equal(t, 1, metrics.ScorableAnswers)
	assert.True(t, metrics.Pass)
}

// TestEvaluateDetectsOverPermissionRecall is the safety metric: a secret
// document appearing in results fails the gate no matter how good recall is.
func TestEvaluateDetectsOverPermissionRecall(t *testing.T) {
	svc, repo := newGoldenFixture([]*types.GoldenQuestion{goldenQuestion("q1")},
		[]*types.SearchResult{
			{KnowledgeID: "k1", Content: "正常",
				KnowledgeLifecycleStatus: string(types.KnowledgeLifecycleValid),
				KnowledgeClassification:  string(types.KnowledgeClassificationInternal)},
			{KnowledgeID: "k2", Content: "机密",
				KnowledgeLifecycleStatus: string(types.KnowledgeLifecycleValid),
				KnowledgeClassification:  string(types.KnowledgeClassificationConfidential)},
		})

	_, err := svc.RunEvaluation(context.Background(), 7, "set-1", "u-1")
	require.NoError(t, err)
	metrics := runMetrics(t, repo.runs[0])
	assert.Equal(t, 0.5, metrics.OverPermissionRecall)
	assert.False(t, metrics.Pass, "one leaked secret document must fail the gate")
}

// TestEvaluateDetectsExpiredMisuse covers the freshness metric: a question
// expecting valid content that retrieves an expired document is misuse.
func TestEvaluateDetectsExpiredMisuse(t *testing.T) {
	svc, repo := newGoldenFixture([]*types.GoldenQuestion{goldenQuestion("q1")},
		[]*types.SearchResult{
			{KnowledgeID: "k1", Content: "旧版",
				KnowledgeLifecycleStatus: string(types.KnowledgeLifecycleExpired),
				KnowledgeClassification:  string(types.KnowledgeClassificationInternal)},
		})

	_, err := svc.RunEvaluation(context.Background(), 7, "set-1", "u-1")
	require.NoError(t, err)
	metrics := runMetrics(t, repo.runs[0])
	assert.Equal(t, 1.0, metrics.ExpiredMisuseRate)
}

// TestEvaluateRefusalCorrectness covers both directions: a must-refuse
// question that returned live evidence is wrong, and a normal question that
// was refused is also wrong.
func TestEvaluateRefusalCorrectness(t *testing.T) {
	mustRefuse := goldenQuestion("q1")
	mustRefuse.FreshnessExpectation = string(types.GoldenFreshnessExpiredMustRefuse)

	svc, repo := newGoldenFixture([]*types.GoldenQuestion{mustRefuse},
		[]*types.SearchResult{
			{KnowledgeID: "k1", Content: "有效内容",
				KnowledgeLifecycleStatus: string(types.KnowledgeLifecycleValid)},
		})
	_, err := svc.RunEvaluation(context.Background(), 7, "set-1", "u-1")
	require.NoError(t, err)
	metrics := runMetrics(t, repo.runs[0])
	require.NotNil(t, metrics.RefusalCorrectness)
	assert.Equal(t, 0.0, *metrics.RefusalCorrectness,
		"returning live evidence to a must-refuse question is a failure")
	assert.False(t, metrics.Pass)
}

// TestEvaluateRefusalCountsCorrectRefusal is the inverse.
func TestEvaluateRefusalCountsCorrectRefusal(t *testing.T) {
	mustRefuse := goldenQuestion("q1")
	mustRefuse.FreshnessExpectation = string(types.GoldenFreshnessExpiredMustRefuse)

	svc, repo := newGoldenFixture([]*types.GoldenQuestion{mustRefuse},
		[]*types.SearchResult{
			{KnowledgeID: "k1", Content: "旧版",
				KnowledgeLifecycleStatus: string(types.KnowledgeLifecycleExpired)},
		})
	_, err := svc.RunEvaluation(context.Background(), 7, "set-1", "u-1")
	require.NoError(t, err)
	metrics := runMetrics(t, repo.runs[0])
	require.NotNil(t, metrics.RefusalCorrectness)
	assert.Equal(t, 1.0, *metrics.RefusalCorrectness)
}

// TestEvaluateMetricsSkipWhatWasNotAsked keeps "not evaluated" distinct from
// "evaluated and zero".
//
// 答案质量只在题目写了期望答案时才有分母，所以这里必须是 nil。拒答正确率
// 则**始终**有分母——普通题目"不该被拒"本身就是一条预期，误拒同样算错，
// 这正是它比"只测该拒的题"更有用的地方。
func TestEvaluateMetricsSkipWhatWasNotAsked(t *testing.T) {
	svc, repo := newGoldenFixture([]*types.GoldenQuestion{goldenQuestion("q1")},
		[]*types.SearchResult{
			{KnowledgeID: "k1", Content: "内容",
				KnowledgeLifecycleStatus: string(types.KnowledgeLifecycleValid)},
		})
	_, err := svc.RunEvaluation(context.Background(), 7, "set-1", "u-1")
	require.NoError(t, err)
	metrics := runMetrics(t, repo.runs[0])
	assert.Nil(t, metrics.AnswerAccuracy, "no reference answer means no answer metric")
	require.NotNil(t, metrics.RefusalCorrectness,
		"a normal question still carries the 'must not refuse' expectation")
	assert.Equal(t, 1.0, *metrics.RefusalCorrectness, "the answer was not refused")
	assert.True(t, metrics.Pass)
}

// TestCheckGateFailsClosedWithoutARun keeps "never evaluated" from passing.
func TestCheckGateFailsClosedWithoutARun(t *testing.T) {
	svc, _ := newGoldenFixture(nil, nil)
	pass, run, err := svc.CheckGate(context.Background(), 7, "set-1")
	require.Error(t, err)
	assert.False(t, pass)
	assert.Nil(t, run)
}

// runMetrics decodes the persisted metrics of a finished run.
func runMetrics(t *testing.T, run *types.GoldenSetRun) types.GoldenRunMetrics {
	t.Helper()
	var metrics types.GoldenRunMetrics
	require.NoError(t, json.Unmarshal(run.Metrics, &metrics))
	return metrics
}
