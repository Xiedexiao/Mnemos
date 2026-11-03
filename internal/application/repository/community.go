package repository

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// ErrCommunityQuestionNotFound is returned when a question lookup misses.
var ErrCommunityQuestionNotFound = errors.New("community question not found")

// ErrCommunityAnswerNotFound is returned when an answer lookup misses.
var ErrCommunityAnswerNotFound = errors.New("community answer not found")

// communityRepository implements interfaces.CommunityRepository
// (KnowAxis V3.0 §15.6).
type communityRepository struct {
	db *gorm.DB
}

// NewCommunityRepository constructs the community repository.
func NewCommunityRepository(db *gorm.DB) interfaces.CommunityRepository {
	return &communityRepository{db: db}
}

// CreateQuestion inserts one question.
func (r *communityRepository) CreateQuestion(
	ctx context.Context, question *types.CommunityQuestion,
) error {
	return r.db.WithContext(ctx).Create(question).Error
}

// GetQuestion returns one question within the tenant.
func (r *communityRepository) GetQuestion(
	ctx context.Context, tenantID uint64, questionID string,
) (*types.CommunityQuestion, error) {
	var question types.CommunityQuestion
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, questionID).
		First(&question).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrCommunityQuestionNotFound
	}
	if err != nil {
		return nil, err
	}
	return &question, nil
}

// ListQuestions returns questions of one tenant, optionally by status.
func (r *communityRepository) ListQuestions(
	ctx context.Context, tenantID uint64, status types.CommunityQuestionStatus, limit int,
) ([]*types.CommunityQuestion, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := r.db.WithContext(ctx).Where("tenant_id = ?", tenantID)
	if status != "" {
		query = query.Where("status = ?", string(status))
	}
	var rows []*types.CommunityQuestion
	err := query.Order("created_at DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

// UpdateQuestion persists the whole row.
func (r *communityRepository) UpdateQuestion(
	ctx context.Context, question *types.CommunityQuestion,
) error {
	return r.db.WithContext(ctx).Save(question).Error
}

// CreateAnswer inserts one answer.
func (r *communityRepository) CreateAnswer(
	ctx context.Context, answer *types.CommunityAnswer,
) error {
	return r.db.WithContext(ctx).Create(answer).Error
}

// GetAnswer returns one answer within the tenant.
func (r *communityRepository) GetAnswer(
	ctx context.Context, tenantID uint64, answerID string,
) (*types.CommunityAnswer, error) {
	var answer types.CommunityAnswer
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, answerID).
		First(&answer).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrCommunityAnswerNotFound
	}
	if err != nil {
		return nil, err
	}
	return &answer, nil
}

// ListAnswers returns a question's answers, accepted first then by votes.
func (r *communityRepository) ListAnswers(
	ctx context.Context, tenantID uint64, questionID string,
) ([]*types.CommunityAnswer, error) {
	var rows []*types.CommunityAnswer
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND question_id = ?", tenantID, questionID).
		Order("accepted DESC, votes DESC, created_at ASC").
		Find(&rows).Error
	return rows, err
}

// VoteAnswer increments an answer's helpful count.
func (r *communityRepository) VoteAnswer(
	ctx context.Context, tenantID uint64, answerID string,
) (*types.CommunityAnswer, error) {
	if err := r.db.WithContext(ctx).
		Model(&types.CommunityAnswer{}).
		Where("tenant_id = ? AND id = ?", tenantID, answerID).
		Update("votes", gorm.Expr("votes + 1")).Error; err != nil {
		return nil, err
	}
	return r.GetAnswer(ctx, tenantID, answerID)
}

// AcceptAnswer marks one answer accepted and unmarks any previous one.
//
// 同一问题只能有一个采纳答案：先清掉旧标记再置新的，避免两次采纳留下两条
// accepted 行让"最佳答案"变得不确定。
func (r *communityRepository) AcceptAnswer(
	ctx context.Context, tenantID uint64, questionID, answerID string,
) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&types.CommunityAnswer{}).
			Where("tenant_id = ? AND question_id = ? AND accepted = ?", tenantID, questionID, true).
			Update("accepted", false).Error; err != nil {
			return err
		}
		return tx.Model(&types.CommunityAnswer{}).
			Where("tenant_id = ? AND question_id = ? AND id = ?", tenantID, questionID, answerID).
			Update("accepted", true).Error
	})
}

// CreateReport inserts one moderation report.
func (r *communityRepository) CreateReport(
	ctx context.Context, report *types.CommunityReport,
) error {
	return r.db.WithContext(ctx).Create(report).Error
}

// ListReports returns the moderation queue.
func (r *communityRepository) ListReports(
	ctx context.Context, tenantID uint64, status types.CommunityReportStatus, limit int,
) ([]*types.CommunityReport, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := r.db.WithContext(ctx).Where("tenant_id = ?", tenantID)
	if status != "" {
		query = query.Where("status = ?", string(status))
	}
	var rows []*types.CommunityReport
	err := query.Order("created_at ASC").Limit(limit).Find(&rows).Error
	return rows, err
}

// ResolveReport records a moderator decision.
func (r *communityRepository) ResolveReport(
	ctx context.Context, tenantID uint64, reportID string,
	status types.CommunityReportStatus, actorID string,
) (*types.CommunityReport, error) {
	if err := r.db.WithContext(ctx).
		Model(&types.CommunityReport{}).
		Where("tenant_id = ? AND id = ?", tenantID, reportID).
		Updates(map[string]interface{}{
			"status":     string(status),
			"handled_by": actorID,
		}).Error; err != nil {
		return nil, err
	}
	var report types.CommunityReport
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, reportID).
		First(&report).Error; err != nil {
		return nil, err
	}
	return &report, nil
}

// CountAnswers counts answers to one question, for the status transition.
func (r *communityRepository) CountAnswers(
	ctx context.Context, tenantID uint64, questionID string,
) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&types.CommunityAnswer{}).
		Where("tenant_id = ? AND question_id = ?", tenantID, questionID).
		Count(&count).Error
	return count, err
}
