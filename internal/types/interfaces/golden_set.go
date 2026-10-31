package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// GoldenSetRepository persists golden sets, their questions and their
// evaluation runs (KnowAxis V3.0 §12.5/§14.8).
type GoldenSetRepository interface {
	// CreateSet inserts one set (ID assigned by the model hook).
	CreateSet(ctx context.Context, set *types.GoldenSet) error
	// UpdateSet persists the full mutable state of a loaded set.
	UpdateSet(ctx context.Context, set *types.GoldenSet) error
	// DeleteSet removes one set together with its questions and runs in a
	// single transaction (删除级联 questions+runs).
	DeleteSet(ctx context.Context, tenantID uint64, setID string) error
	// GetSet resolves one set by ID within the tenant.
	GetSet(ctx context.Context, tenantID uint64, setID string) (*types.GoldenSet, error)
	// GetSetByName resolves one set by its tenant-unique name
	// (duplicate-name guard and rename checks).
	GetSetByName(ctx context.Context, tenantID uint64, name string) (*types.GoldenSet, error)
	// ListSets lists the tenant's sets; domain is an optional filter.
	ListSets(ctx context.Context, tenantID uint64, domain string) ([]*types.GoldenSet, error)

	// CreateQuestions bulk-inserts questions (batch import fast path; IDs
	// assigned by the model hook).
	CreateQuestions(ctx context.Context, questions []*types.GoldenQuestion) error
	// UpdateQuestion persists the full mutable state of a loaded question.
	UpdateQuestion(ctx context.Context, question *types.GoldenQuestion) error
	// DeleteQuestion removes one question from a set. Zero rows affected is
	// surfaced as gorm.ErrRecordNotFound.
	DeleteQuestion(ctx context.Context, tenantID uint64, setID, questionID string) error
	// GetQuestion resolves one question of a set (set is tenant-checked by
	// the service before calling; questions carry no tenant column).
	GetQuestion(ctx context.Context, setID, questionID string) (*types.GoldenQuestion, error)
	// FindQuestionByText resolves one question of a set by its exact text
	// (批量导入按文本 upsert 的匹配键).
	FindQuestionByText(ctx context.Context, setID, question string) (*types.GoldenQuestion, error)
	// ListQuestions lists a set's questions; enabledOnly filters to the ones
	// that participate in runs.
	ListQuestions(ctx context.Context, setID string, enabledOnly bool) ([]*types.GoldenQuestion, error)

	// CreateRun inserts one run row in status running (started_at filled by
	// the caller).
	CreateRun(ctx context.Context, run *types.GoldenSetRun) error
	// UpdateRun persists the full mutable state of a run (status, metrics,
	// per_question, finished_at).
	UpdateRun(ctx context.Context, run *types.GoldenSetRun) error
	// GetRun resolves one run by ID within the tenant.
	GetRun(ctx context.Context, tenantID uint64, runID string) (*types.GoldenSetRun, error)
	// ListRuns lists a set's runs, newest first.
	ListRuns(ctx context.Context, tenantID uint64, setID string, limit int) ([]*types.GoldenSetRun, error)
	// GetLatestRun returns the newest run of a set with the given status
	// (发布门禁读取最近一次 completed run; status 为空 = 不限状态).
	GetLatestRun(ctx context.Context, tenantID uint64, setID string, status types.GoldenSetRunStatus) (*types.GoldenSetRun, error)
}

// GoldenSetService manages golden question sets and runs their retrieval
// evaluations (KnowAxis V3.0 §12.5/§14.8 的 v1 子集). v1 scores retrieval
// metrics only (top5_hit / recall_at_5 / NDCG / MRR against expected
// evidence); answer-quality and refusal-correctness metrics are stubs until
// the full QA pipeline is wired in.
type GoldenSetService interface {
	// CreateSet creates one set (audit: goldenset.created).
	CreateSet(ctx context.Context, set *types.GoldenSet) (*types.GoldenSet, error)
	// UpdateSet patches the mutable fields of one set. v1 audit vocabulary
	// is created/evaluated only; field-level updates are visible via
	// updated_at until the vocabulary grows.
	UpdateSet(
		ctx context.Context, tenantID uint64, setID string,
		input *types.GoldenSetUpdate, actorID string,
	) (*types.GoldenSet, error)
	// DeleteSet removes one set with its questions and runs. v1 audit
	// vocabulary is created/evaluated only; deletions remain traceable via
	// the removed rows' absence plus updated_at history.
	DeleteSet(ctx context.Context, tenantID uint64, setID, actorID string) error
	// GetSet resolves one set by ID within the tenant.
	GetSet(ctx context.Context, tenantID uint64, setID string) (*types.GoldenSet, error)
	// ListSets lists the tenant's sets; domain is an optional filter.
	ListSets(ctx context.Context, tenantID uint64, domain string) ([]*types.GoldenSet, error)

	// ImportQuestions batch-upserts questions by exact question text:
	// hit = overwrite the mutable fields, miss = insert.
	ImportQuestions(
		ctx context.Context, tenantID uint64, setID string,
		input []*types.GoldenQuestion, actorID string,
	) (*types.GoldenQuestionImportResult, error)
	// ListQuestions lists a set's questions.
	ListQuestions(ctx context.Context, tenantID uint64, setID string) ([]*types.GoldenQuestion, error)
	// UpdateQuestion patches one question (text immutable).
	UpdateQuestion(
		ctx context.Context, tenantID uint64, setID, questionID string,
		input *types.GoldenQuestionUpdate, actorID string,
	) (*types.GoldenQuestion, error)
	// DeleteQuestion removes one question.
	DeleteQuestion(ctx context.Context, tenantID uint64, setID, questionID, actorID string) error

	// RunEvaluation runs the v1 retrieval evaluation synchronously: for every
	// enabled question it hybrid-searches the set's bound KB and scores
	// top5_hit / recall_at_5 / NDCG / MRR against expected_evidence. The
	// result is persisted as one golden_set_run
	// (audit: goldenset.evaluated).
	RunEvaluation(ctx context.Context, tenantID uint64, setID, userID string) (*types.GoldenSetRun, error)
	// ListRuns lists a set's runs, newest first (limit ≤ 0 = default).
	ListRuns(ctx context.Context, tenantID uint64, setID string, limit int) ([]*types.GoldenSetRun, error)
	// GetRun resolves one run by ID within the tenant.
	GetRun(ctx context.Context, tenantID uint64, runID string) (*types.GoldenSetRun, error)
	// CheckGate reads the latest completed run of a set and reports the
	// release-gate verdict. pass=false when no completed run exists (门禁
	// 失败闭合：没有评测记录 = 不放行).
	CheckGate(ctx context.Context, tenantID uint64, setID string) (bool, *types.GoldenSetRun, error)
}
