package repository

import (
	"context"
	"errors"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

// ErrAIPreReviewRunNotFound 预审运行不存在（或不属于本租户）。service 层
// 据此映射业务错误，与 approval 系列的哨兵同一风格。
var ErrAIPreReviewRunNotFound = errors.New("ai pre-review run not found")

// aiPreReviewRunListMaxLimit 是运行历史列表的硬上限：预审历史是人读的
// 审批上下文，50 条足够（一次审核通常只有个位数重跑），防止无界扫描。
const aiPreReviewRunListMaxLimit = 50

// aiPreReviewRepository implements interfaces.AIPreReviewRepository. One
// table, append-then-finalize semantics: rows are created in running state
// and only ever receive their terminal status once.
type aiPreReviewRepository struct {
	db *gorm.DB
}

// NewAIPreReviewRepository constructs the AI pre-review repository.
func NewAIPreReviewRepository(db *gorm.DB) interfaces.AIPreReviewRepository {
	return &aiPreReviewRepository{db: db}
}

// CreateRun inserts one run row. Status defaults to running and findings to
// an empty array so a partially built run can never violate the migration's
// NOT NULL constraints.
func (r *aiPreReviewRepository) CreateRun(ctx context.Context, run *types.AIPreReviewRun) error {
	if run == nil {
		return errors.New("ai pre-review: run is nil")
	}
	if run.Status == "" {
		run.Status = string(types.PreReviewStatusRunning)
	}
	if len(run.Findings) == 0 {
		run.Findings = types.JSON(`[]`)
	}
	if run.CreatedAt.IsZero() {
		run.CreatedAt = time.Now()
	}
	return r.db.WithContext(ctx).Create(run).Error
}

// CompleteRun writes the terminal state (completed or failed) in one UPDATE.
// Only terminal columns move: identity/task columns are immutable after
// creation, so a late writer cannot re-point a run at another task.
func (r *aiPreReviewRepository) CompleteRun(ctx context.Context, run *types.AIPreReviewRun) error {
	if run == nil || run.ID == "" {
		return errors.New("ai pre-review: run id is required")
	}
	status := run.Status
	switch types.PreReviewStatus(status) {
	case types.PreReviewStatusCompleted, types.PreReviewStatusFailed:
	default:
		return errors.New("ai pre-review: complete requires a terminal status")
	}
	findings := run.Findings
	if len(findings) == 0 {
		findings = types.JSON(`[]`)
	}
	finishedAt := run.FinishedAt
	if finishedAt == nil {
		now := time.Now()
		finishedAt = &now
	}

	result := r.db.WithContext(ctx).
		Model(&types.AIPreReviewRun{}).
		Where("tenant_id = ? AND id = ?", run.TenantID, run.ID).
		Updates(map[string]interface{}{
			"status":        status,
			"findings":      findings,
			"model_version": run.ModelVersion,
			"prompt_version": run.PromptVersion,
			"error_message": run.ErrorMessage,
			"finished_at":   finishedAt,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrAIPreReviewRunNotFound
	}
	// 回填到调用方加载的行上，让 service 的返回值与库内一致。
	run.Status = status
	run.Findings = findings
	run.FinishedAt = finishedAt
	return nil
}

// ListByTask returns the runs of one approval task, newest first. The
// migration's (tenant_id, task_id, created_at DESC) index serves both the
// filter and the ordering.
func (r *aiPreReviewRepository) ListByTask(
	ctx context.Context, tenantID uint64, taskID string, limit int,
) ([]*types.AIPreReviewRun, error) {
	if tenantID == 0 || taskID == "" {
		return nil, nil
	}
	if limit <= 0 || limit > aiPreReviewRunListMaxLimit {
		limit = aiPreReviewRunListMaxLimit
	}
	var runs []*types.AIPreReviewRun
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND task_id = ?", tenantID, taskID).
		Order("created_at DESC").
		Limit(limit).
		Find(&runs).Error
	if err != nil {
		return nil, err
	}
	return runs, nil
}
