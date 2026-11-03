package repository

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// knowledgeStarRepository implements interfaces.KnowledgeStarRepository
// (KnowAxis V3.0 §15.4 知识之星).
type knowledgeStarRepository struct {
	db *gorm.DB
}

// NewKnowledgeStarRepository constructs the repository.
func NewKnowledgeStarRepository(db *gorm.DB) interfaces.KnowledgeStarRepository {
	return &knowledgeStarRepository{db: db}
}

// ListCandidates ranks contributors by the points they earned in [start, end).
//
// 兑换扣分不计入评选：负分记录会把"这个月换了礼品"的人排到后面，而评选
// 衡量的是贡献。因此只累加正分，并同时统计三类主要贡献用于解释分数。
func (r *knowledgeStarRepository) ListCandidates(
	ctx context.Context, tenantID uint64, start, end time.Time, limit int,
) ([]*types.KnowledgeStarCandidate, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	type row struct {
		UserID           string
		Score            int
		KnowledgeCreated int
		ReviewsDone      int
		AnswersAdopted   int
	}
	var rows []row
	err := r.db.WithContext(ctx).
		Model(&types.UserPointsLedger{}).
		Select("user_id, "+
			"SUM(CASE WHEN points > 0 THEN points ELSE 0 END) AS score, "+
			"SUM(CASE WHEN action = ? AND points > 0 THEN 1 ELSE 0 END) AS knowledge_created, "+
			"SUM(CASE WHEN action = ? AND points > 0 THEN 1 ELSE 0 END) AS reviews_done, "+
			"SUM(CASE WHEN action = ? AND points > 0 THEN 1 ELSE 0 END) AS answers_adopted",
			string(types.PointsActionKnowledgeCreated),
			string(types.PointsActionReviewDone),
			string(types.PointsActionAnswerAdopted)).
		Where("tenant_id = ? AND created_at >= ? AND created_at < ?", tenantID, start, end).
		Group("user_id").
		Having("SUM(CASE WHEN points > 0 THEN points ELSE 0 END) > 0").
		Order("score DESC, user_id ASC").
		Limit(limit).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}

	userIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		userIDs = append(userIDs, row.UserID)
	}
	var balances []*types.UserPointBalance
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND user_id IN ?", tenantID, userIDs).
		Find(&balances).Error; err != nil {
		return nil, err
	}
	balanceByUser := make(map[string]*types.UserPointBalance, len(balances))
	for _, balance := range balances {
		balanceByUser[balance.UserID] = balance
	}

	candidates := make([]*types.KnowledgeStarCandidate, 0, len(rows))
	for _, row := range rows {
		candidate := &types.KnowledgeStarCandidate{
			UserID:           row.UserID,
			Score:            row.Score,
			KnowledgeCreated: row.KnowledgeCreated,
			ReviewsDone:      row.ReviewsDone,
			AnswersAdopted:   row.AnswersAdopted,
			Level:            string(types.PointLevelBronze),
		}
		if balance, ok := balanceByUser[row.UserID]; ok {
			candidate.Balance = balance.Balance
			if balance.Level != "" {
				candidate.Level = balance.Level
			}
		}
		candidates = append(candidates, candidate)
	}
	return candidates, nil
}

// ReplaceAwards replaces one period's award list in a transaction.
func (r *knowledgeStarRepository) ReplaceAwards(
	ctx context.Context, tenantID uint64, awards []*types.KnowledgeStarAward,
) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if len(awards) == 0 {
			return nil
		}
		periodType := awards[0].PeriodType
		period := awards[0].Period
		if err := tx.Where("tenant_id = ? AND period_type = ? AND period = ?",
			tenantID, periodType, period).
			Delete(&types.KnowledgeStarAward{}).Error; err != nil {
			return err
		}
		return tx.Create(awards).Error
	})
}

// ListAwards returns awards; an empty period means "all periods, newest
// first".
func (r *knowledgeStarRepository) ListAwards(
	ctx context.Context, tenantID uint64, periodType, period string,
	publishedOnly bool, limit int,
) ([]*types.KnowledgeStarAward, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := r.db.WithContext(ctx).Where("tenant_id = ?", tenantID)
	if periodType != "" {
		query = query.Where("period_type = ?", periodType)
	}
	if period != "" {
		query = query.Where("period = ?", period)
	}
	if publishedOnly {
		query = query.Where("published = ?", true)
	}
	var awards []*types.KnowledgeStarAward
	if err := query.
		Order("period DESC, rank ASC, awarded_at DESC").
		Limit(limit).
		Find(&awards).Error; err != nil {
		return nil, err
	}
	return awards, nil
}

// PublishAwards publishes one period's list.
func (r *knowledgeStarRepository) PublishAwards(
	ctx context.Context, tenantID uint64, periodType, period string,
) (int64, error) {
	result := r.db.WithContext(ctx).Model(&types.KnowledgeStarAward{}).
		Where("tenant_id = ? AND period_type = ? AND period = ?", tenantID, periodType, period).
		Updates(map[string]interface{}{
			"published":  true,
			"updated_at": time.Now().UTC(),
		})
	return result.RowsAffected, result.Error
}
