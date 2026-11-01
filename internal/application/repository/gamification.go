package repository

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// gamificationRepository implements interfaces.GamificationRepository across
// the four gamification tables (V3.0 §15.4 / §15.5). One repository, one
// vertical: the award policy stays in the service, this layer only moves rows.
type gamificationRepository struct {
	db *gorm.DB
}

// NewGamificationRepository constructs the gamification repository.
func NewGamificationRepository(db *gorm.DB) interfaces.GamificationRepository {
	return &gamificationRepository{db: db}
}

// ---- 积分流水 ----

// InsertLedgerEntry appends one award row.
func (r *gamificationRepository) InsertLedgerEntry(ctx context.Context, entry *types.UserPointsLedger) error {
	return r.db.WithContext(ctx).Create(entry).Error
}

// FindLedgerEntry resolves the (user, action, source) row behind the
// service-side idempotency check. Rows with empty source are never matched
// here (the service guards that before calling).
func (r *gamificationRepository) FindLedgerEntry(
	ctx context.Context, tenantID uint64, userID string,
	action types.PointsAction, sourceType, sourceID string,
) (*types.UserPointsLedger, error) {
	var entry types.UserPointsLedger
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND user_id = ? AND action = ? AND source_type = ? AND source_id = ?",
			tenantID, userID, string(action), sourceType, sourceID).
		First(&entry).Error; err != nil {
		return nil, err
	}
	return &entry, nil
}

// SumPointsSince totals one user's awarded points for one action since the
// given instant (inclusive) — the daily-cap aggregate. A user with no rows
// sums to 0.
func (r *gamificationRepository) SumPointsSince(
	ctx context.Context, tenantID uint64, userID string,
	action types.PointsAction, since time.Time,
) (int, error) {
	var total int
	err := r.db.WithContext(ctx).
		Model(&types.UserPointsLedger{}).
		Select("COALESCE(SUM(points), 0)").
		Where("tenant_id = ? AND user_id = ? AND action = ? AND created_at >= ?",
			tenantID, userID, string(action), since).
		Scan(&total).Error
	return total, err
}

// ListLedger returns the user's award history, newest first. A positive
// limit caps the rows; limit <= 0 returns everything (GORM's Limit(0)
// means "no cap", so the cap is applied conditionally).
func (r *gamificationRepository) ListLedger(
	ctx context.Context, tenantID uint64, userID string, limit int,
) ([]*types.UserPointsLedger, error) {
	query := r.db.WithContext(ctx).
		Where("tenant_id = ? AND user_id = ?", tenantID, userID).
		Order("created_at DESC")
	if limit > 0 {
		query = query.Limit(limit)
	}
	var entries []*types.UserPointsLedger
	err := query.Find(&entries).Error
	return entries, err
}

// ---- 余额 + 等级 ----

// GetBalance resolves the user's balance row (gorm.ErrRecordNotFound when
// the user has never been awarded).
func (r *gamificationRepository) GetBalance(
	ctx context.Context, tenantID uint64, userID string,
) (*types.UserPointBalance, error) {
	var balance types.UserPointBalance
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND user_id = ?", tenantID, userID).
		First(&balance).Error; err != nil {
		return nil, err
	}
	return &balance, nil
}

// UpsertBalance writes the balance row through the migration's
// UNIQUE(tenant_id, user_id): the service loaded (or zeroed) the row, added
// the points and recomputed the level; this call persists either branch.
// OnConflict DoUpdates keys on the constraint so a concurrent first award
// takes the update path instead of erroring.
func (r *gamificationRepository) UpsertBalance(ctx context.Context, balance *types.UserPointBalance) error {
	return r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "tenant_id"}, {Name: "user_id"}},
			DoUpdates: clause.Assignments(map[string]interface{}{
				"balance":    balance.Balance,
				"level":      balance.Level,
				"updated_at": balance.UpdatedAt,
			}),
		}).
		Create(balance).Error
}

// ListBalances returns the tenant's balance rows, balance DESC (leaderboard
// source; ties break by earliest update so earlier earners rank first).
// A positive limit caps the rows; limit <= 0 returns everything.
func (r *gamificationRepository) ListBalances(
	ctx context.Context, tenantID uint64, limit int,
) ([]*types.UserPointBalance, error) {
	query := r.db.WithContext(ctx).
		Where("tenant_id = ?", tenantID).
		Order("balance DESC, updated_at ASC")
	if limit > 0 {
		query = query.Limit(limit)
	}
	var balances []*types.UserPointBalance
	err := query.Find(&balances).Error
	return balances, err
}

// ---- 学习路径 ----

// CreatePath inserts one path.
func (r *gamificationRepository) CreatePath(ctx context.Context, path *types.LearningPath) error {
	return r.db.WithContext(ctx).Create(path).Error
}

// UpdatePath persists the full mutable state of a loaded path (the service
// loads → mutates → saves, so updated_at stays GORM-touched).
func (r *gamificationRepository) UpdatePath(ctx context.Context, path *types.LearningPath) error {
	return r.db.WithContext(ctx).Save(path).Error
}

// DeletePath physically removes one path. Zero rows affected surfaces
// gorm.ErrRecordNotFound (translated to a 404 by the service).
func (r *gamificationRepository) DeletePath(ctx context.Context, tenantID uint64, pathID string) error {
	result := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, pathID).
		Delete(&types.LearningPath{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// GetPath resolves one path by ID within the tenant.
func (r *gamificationRepository) GetPath(
	ctx context.Context, tenantID uint64, pathID string,
) (*types.LearningPath, error) {
	var path types.LearningPath
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, pathID).
		First(&path).Error; err != nil {
		return nil, err
	}
	return &path, nil
}

// ListPaths returns the tenant's paths, newest first.
func (r *gamificationRepository) ListPaths(ctx context.Context, tenantID uint64) ([]*types.LearningPath, error) {
	var paths []*types.LearningPath
	err := r.db.WithContext(ctx).
		Where("tenant_id = ?", tenantID).
		Order("created_at DESC").
		Find(&paths).Error
	return paths, err
}

// ---- 学习进度 ----

// CreateProgress inserts one progress row (UNIQUE(path_id, user_id) in the
// migration is the backstop for the service's get-then-create race).
func (r *gamificationRepository) CreateProgress(ctx context.Context, progress *types.LearningProgress) error {
	return r.db.WithContext(ctx).Create(progress).Error
}

// SaveProgress persists the full mutable state of a loaded progress row.
func (r *gamificationRepository) SaveProgress(ctx context.Context, progress *types.LearningProgress) error {
	return r.db.WithContext(ctx).Save(progress).Error
}

// GetProgress resolves the (path, user) progress row.
func (r *gamificationRepository) GetProgress(
	ctx context.Context, tenantID uint64, pathID, userID string,
) (*types.LearningProgress, error) {
	var progress types.LearningProgress
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND path_id = ? AND user_id = ?", tenantID, pathID, userID).
		First(&progress).Error; err != nil {
		return nil, err
	}
	return &progress, nil
}

// DeleteProgressByPath removes every progress row of one path (path delete
// side-effect). Zero rows affected is not an error.
func (r *gamificationRepository) DeleteProgressByPath(ctx context.Context, tenantID uint64, pathID string) error {
	return r.db.WithContext(ctx).
		Where("tenant_id = ? AND path_id = ?", tenantID, pathID).
		Delete(&types.LearningProgress{}).Error
}
