package repository

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

// goldenSetRepository implements interfaces.GoldenSetRepository.
type goldenSetRepository struct {
	db *gorm.DB
}

// NewGoldenSetRepository constructs the golden set repository.
func NewGoldenSetRepository(db *gorm.DB) interfaces.GoldenSetRepository {
	return &goldenSetRepository{db: db}
}

// CreateSet inserts one set. The (tenant_id, name) uniqueness is enforced by
// the migration's constraint; the service pre-checks and translates the
// duplicate into a 400.
func (r *goldenSetRepository) CreateSet(ctx context.Context, set *types.GoldenSet) error {
	return r.db.WithContext(ctx).Create(set).Error
}

// UpdateSet persists the full mutable state of a loaded set (the service
// loads → mutates → saves).
func (r *goldenSetRepository) UpdateSet(ctx context.Context, set *types.GoldenSet) error {
	return r.db.WithContext(ctx).Save(set).Error
}

// DeleteSet removes one set together with its questions and runs in a single
// transaction (删除级联 questions+runs; questions/runs carry no FK, the
// cascade is explicit so a half-deleted set can never linger).
func (r *goldenSetRepository) DeleteSet(ctx context.Context, tenantID uint64, setID string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.
			Where("set_id = ?", setID).
			Delete(&types.GoldenQuestion{}).Error; err != nil {
			return err
		}
		if err := tx.
			Where("tenant_id = ? AND set_id = ?", tenantID, setID).
			Delete(&types.GoldenSetRun{}).Error; err != nil {
			return err
		}
		result := tx.
			Where("tenant_id = ? AND id = ?", tenantID, setID).
			Delete(&types.GoldenSet{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
}

// GetSet resolves one set by ID within the tenant.
func (r *goldenSetRepository) GetSet(ctx context.Context, tenantID uint64, setID string) (*types.GoldenSet, error) {
	var set types.GoldenSet
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, setID).
		First(&set).Error; err != nil {
		return nil, err
	}
	return &set, nil
}

// GetSetByName resolves one set by its tenant-unique name.
func (r *goldenSetRepository) GetSetByName(ctx context.Context, tenantID uint64, name string) (*types.GoldenSet, error) {
	var set types.GoldenSet
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND name = ?", tenantID, name).
		First(&set).Error; err != nil {
		return nil, err
	}
	return &set, nil
}

// ListSets lists the tenant's sets; domain is an optional filter.
func (r *goldenSetRepository) ListSets(ctx context.Context, tenantID uint64, domain string) ([]*types.GoldenSet, error) {
	query := r.db.WithContext(ctx).Model(&types.GoldenSet{}).Where("tenant_id = ?", tenantID)
	if domain != "" {
		query = query.Where("domain = ?", domain)
	}
	var sets []*types.GoldenSet
	err := query.Order("updated_at DESC").Find(&sets).Error
	return sets, err
}

// CreateQuestions bulk-inserts questions in one statement (batch import fast
// path). The BeforeCreate hook fills missing IDs before the insert.
func (r *goldenSetRepository) CreateQuestions(ctx context.Context, questions []*types.GoldenQuestion) error {
	if len(questions) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Create(&questions).Error
}

// UpdateQuestion persists the full mutable state of a loaded question.
func (r *goldenSetRepository) UpdateQuestion(ctx context.Context, question *types.GoldenQuestion) error {
	return r.db.WithContext(ctx).Save(question).Error
}

// DeleteQuestion removes one question. Zero rows affected means the question
// is gone; surfaced as ErrRecordNotFound.
func (r *goldenSetRepository) DeleteQuestion(ctx context.Context, tenantID uint64, setID, questionID string) error {
	// 题目表没有 tenant 列：join 回 set 上做租户校验，防跨租户删除。
	result := r.db.WithContext(ctx).
		Where("id = ? AND set_id IN (?)", questionID, r.db.WithContext(ctx).
			Model(&types.GoldenSet{}).
			Select("id").
			Where("tenant_id = ? AND id = ?", tenantID, setID)).
		Delete(&types.GoldenQuestion{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// GetQuestion resolves one question of a set. The service tenant-checks the
// set first (questions carry no tenant column of their own).
func (r *goldenSetRepository) GetQuestion(ctx context.Context, setID, questionID string) (*types.GoldenQuestion, error) {
	var question types.GoldenQuestion
	if err := r.db.WithContext(ctx).
		Where("set_id = ? AND id = ?", setID, questionID).
		First(&question).Error; err != nil {
		return nil, err
	}
	return &question, nil
}

// FindQuestionByText resolves one question of a set by its exact text (批量
// 导入按文本 upsert 的匹配键).
func (r *goldenSetRepository) FindQuestionByText(ctx context.Context, setID, question string) (*types.GoldenQuestion, error) {
	var questionRow types.GoldenQuestion
	if err := r.db.WithContext(ctx).
		Where("set_id = ? AND question = ?", setID, question).
		First(&questionRow).Error; err != nil {
		return nil, err
	}
	return &questionRow, nil
}

// ListQuestions lists a set's questions in stable creation order;
// enabledOnly filters to the ones that participate in runs.
func (r *goldenSetRepository) ListQuestions(ctx context.Context, setID string, enabledOnly bool) ([]*types.GoldenQuestion, error) {
	query := r.db.WithContext(ctx).Model(&types.GoldenQuestion{}).Where("set_id = ?", setID)
	if enabledOnly {
		query = query.Where("enabled = ?", true)
	}
	var questions []*types.GoldenQuestion
	err := query.Order("created_at ASC").Find(&questions).Error
	return questions, err
}

// CreateRun inserts one run row in status running.
func (r *goldenSetRepository) CreateRun(ctx context.Context, run *types.GoldenSetRun) error {
	return r.db.WithContext(ctx).Create(run).Error
}

// UpdateRun persists the full mutable state of a run (status, metrics,
// per_question, finished_at).
func (r *goldenSetRepository) UpdateRun(ctx context.Context, run *types.GoldenSetRun) error {
	return r.db.WithContext(ctx).Save(run).Error
}

// GetRun resolves one run by ID within the tenant.
func (r *goldenSetRepository) GetRun(ctx context.Context, tenantID uint64, runID string) (*types.GoldenSetRun, error) {
	var run types.GoldenSetRun
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, runID).
		First(&run).Error; err != nil {
		return nil, err
	}
	return &run, nil
}

// ListRuns lists a set's runs, newest first.
func (r *goldenSetRepository) ListRuns(ctx context.Context, tenantID uint64, setID string, limit int) ([]*types.GoldenSetRun, error) {
	if limit <= 0 {
		limit = defaultGoldenRunListLimit
	}
	var runs []*types.GoldenSetRun
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND set_id = ?", tenantID, setID).
		Order("created_at DESC").
		Limit(limit).
		Find(&runs).Error
	return runs, err
}

// GetLatestRun returns the newest run of a set with the given status; an
// empty status means any status. Serves the release gate (最近一次 completed
// run). gorm.ErrRecordNotFound when the set has no such run.
func (r *goldenSetRepository) GetLatestRun(ctx context.Context, tenantID uint64, setID string, status types.GoldenSetRunStatus) (*types.GoldenSetRun, error) {
	query := r.db.WithContext(ctx).
		Model(&types.GoldenSetRun{}).
		Where("tenant_id = ? AND set_id = ?", tenantID, setID)
	if status != "" {
		query = query.Where("status = ?", string(status))
	}
	var run types.GoldenSetRun
	if err := query.Order("created_at DESC").First(&run).Error; err != nil {
		return nil, err
	}
	return &run, nil
}

// defaultGoldenRunListLimit bounds ListRuns when the caller passes no limit.
const defaultGoldenRunListLimit = 50
