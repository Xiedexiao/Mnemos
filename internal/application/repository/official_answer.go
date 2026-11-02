package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// ErrOfficialAnswerNotFound is returned when an official-answer lookup misses.
var ErrOfficialAnswerNotFound = errors.New("official answer not found")

// officialAnswerRepository implements interfaces.OfficialAnswerRepository.
type officialAnswerRepository struct {
	db *gorm.DB
}

// NewOfficialAnswerRepository constructs the repository.
func NewOfficialAnswerRepository(db *gorm.DB) interfaces.OfficialAnswerRepository {
	return &officialAnswerRepository{db: db}
}

// Create inserts one official answer.
func (r *officialAnswerRepository) Create(
	ctx context.Context, answer *types.OfficialAnswer,
) error {
	return r.db.WithContext(ctx).Create(answer).Error
}

// Update persists the whole row (caller loads, mutates, saves).
func (r *officialAnswerRepository) Update(
	ctx context.Context, answer *types.OfficialAnswer,
) error {
	return r.db.WithContext(ctx).Save(answer).Error
}

// GetByID returns one answer within the tenant.
func (r *officialAnswerRepository) GetByID(
	ctx context.Context, tenantID uint64, answerID string,
) (*types.OfficialAnswer, error) {
	var answer types.OfficialAnswer
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, answerID).
		First(&answer).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrOfficialAnswerNotFound
	}
	if err != nil {
		return nil, err
	}
	return &answer, nil
}

// List returns answers of one tenant, newest first.
func (r *officialAnswerRepository) List(
	ctx context.Context, tenantID uint64, status types.OfficialAnswerStatus, limit int,
) ([]*types.OfficialAnswer, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	query := r.db.WithContext(ctx).Where("tenant_id = ?", tenantID)
	if status != "" {
		query = query.Where("status = ?", string(status))
	}
	var rows []*types.OfficialAnswer
	err := query.Order("updated_at DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

// FindAnswerable looks up the answer for a normalized question in one space.
//
// Only approved/published rows are candidates; the read-time validity window is
// applied by the service so an expired answer can be reported as expired rather
// than silently vanishing.
func (r *officialAnswerRepository) FindAnswerable(
	ctx context.Context, tenantID uint64, normalizedQuestion, kbID string,
) (*types.OfficialAnswer, error) {
	query := r.db.WithContext(ctx).
		Where("tenant_id = ? AND question_normalized = ? AND status IN ?",
			tenantID, normalizedQuestion,
			[]string{string(types.OfficialAnswerApproved), string(types.OfficialAnswerPublished)})
	if kbID != "" {
		// 空间内答案优先；未限定空间的全局答案也可见。
		query = query.Where("(knowledge_base_id = ? OR knowledge_base_id = '')", kbID)
	}
	var answer types.OfficialAnswer
	err := query.Order("version DESC").First(&answer).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrOfficialAnswerNotFound
	}
	if err != nil {
		return nil, err
	}
	return &answer, nil
}

// FindActiveByQuestion returns the live answer for a question, ignoring the
// knowledge-base scope (used to enforce "one active answer per question").
func (r *officialAnswerRepository) FindActiveByQuestion(
	ctx context.Context, tenantID uint64, normalizedQuestion string,
) (*types.OfficialAnswer, error) {
	var answer types.OfficialAnswer
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND question_normalized = ? AND status IN ?",
			tenantID, normalizedQuestion,
			[]string{string(types.OfficialAnswerApproved), string(types.OfficialAnswerPublished)}).
		Order("version DESC").
		First(&answer).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrOfficialAnswerNotFound
	}
	if err != nil {
		return nil, err
	}
	return &answer, nil
}

// ListExpiredDue returns published answers whose expiry has passed, so the
// scheduler can demote them (§13.7 证据或版本失效时自动降级).
func (r *officialAnswerRepository) ListExpiredDue(
	ctx context.Context, tenantID uint64, now time.Time, limit int,
) ([]*types.OfficialAnswer, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	var rows []*types.OfficialAnswer
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND status IN ? AND expires_at IS NOT NULL AND expires_at < ?",
			tenantID,
			[]string{string(types.OfficialAnswerPublished), string(types.OfficialAnswerApproved)},
			now).
		Order("expires_at ASC").
		Limit(limit).
		Find(&rows).Error
	return rows, err
}
