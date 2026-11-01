package interfaces

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// ============================================================================
// 积分等级 + 学习路径（KnowAxis V3.0 §15.4 / §15.5，v1 垂直）。
// 与 comment_recycle.go 同款风格：多仓储/服务收敛在一个接口文件。
// ============================================================================

// GamificationRepository persists the four gamification tables
// (user_points_ledger / user_point_balance / learning_paths /
// learning_progress) and answers the leaderboard + daily-cap aggregates.
type GamificationRepository interface {
	// ---- 积分流水 ----

	// InsertLedgerEntry appends one award row (ID assigned by the model hook).
	InsertLedgerEntry(ctx context.Context, entry *types.UserPointsLedger) error
	// FindLedgerEntry resolves the (user, action, source) award row — the
	// service-side idempotency check ("同 source 同 action 不重复记分").
	// Returns gorm.ErrRecordNotFound when absent.
	FindLedgerEntry(
		ctx context.Context, tenantID uint64, userID string,
		action types.PointsAction, sourceType, sourceID string,
	) (*types.UserPointsLedger, error)
	// SumPointsSince totals one user's awarded points for one action since
	// (inclusive) the given instant — the daily-cap aggregate.
	SumPointsSince(
		ctx context.Context, tenantID uint64, userID string,
		action types.PointsAction, since time.Time,
	) (int, error)
	// ListLedger returns the user's award history, newest first; a positive
	// limit caps the rows (limit <= 0 returns everything — the service
	// passes a clamped value).
	ListLedger(ctx context.Context, tenantID uint64, userID string, limit int) ([]*types.UserPointsLedger, error)

	// ---- 余额 + 等级 ----

	// GetBalance resolves the user's balance row. Returns
	// gorm.ErrRecordNotFound when the user has never been awarded (the
	// service synthesizes a zero balance instead of writing on read).
	GetBalance(ctx context.Context, tenantID uint64, userID string) (*types.UserPointBalance, error)
	// UpsertBalance writes the balance row through the migration's
	// UNIQUE(tenant_id, user_id): insert on first award, update otherwise.
	// The read-modify-write lives in the service; v1 accepts the narrow
	// lost-update window on concurrent awards (documented there).
	UpsertBalance(ctx context.Context, balance *types.UserPointBalance) error
	// ListBalances returns the tenant's balance rows ordered by balance DESC
	// (leaderboard source), capped at limit.
	ListBalances(ctx context.Context, tenantID uint64, limit int) ([]*types.UserPointBalance, error)

	// ---- 学习路径 ----

	// CreatePath inserts one path (ID assigned by the model hook).
	CreatePath(ctx context.Context, path *types.LearningPath) error
	// UpdatePath persists the full mutable state of a loaded path.
	UpdatePath(ctx context.Context, path *types.LearningPath) error
	// DeletePath physically removes one path (v1: hard delete; the progress
	// rows are removed by DeleteProgressByPath in the same service call).
	// Zero rows affected surfaces gorm.ErrRecordNotFound.
	DeletePath(ctx context.Context, tenantID uint64, pathID string) error
	// GetPath resolves one path by ID within the tenant. Returns
	// gorm.ErrRecordNotFound when absent.
	GetPath(ctx context.Context, tenantID uint64, pathID string) (*types.LearningPath, error)
	// ListPaths returns the tenant's paths, newest first.
	ListPaths(ctx context.Context, tenantID uint64) ([]*types.LearningPath, error)

	// ---- 学习进度 ----

	// CreateProgress inserts one progress row (start of a path).
	CreateProgress(ctx context.Context, progress *types.LearningProgress) error
	// SaveProgress persists the full mutable state of a loaded progress row
	// (step appended / pct recomputed / completed_at stamped).
	SaveProgress(ctx context.Context, progress *types.LearningProgress) error
	// GetProgress resolves the (path, user) progress row. Returns
	// gorm.ErrRecordNotFound when the user has not started the path.
	GetProgress(ctx context.Context, tenantID uint64, pathID, userID string) (*types.LearningProgress, error)
	// DeleteProgressByPath removes every progress row of one path (path
	// delete side-effect). Zero rows affected is not an error.
	DeleteProgressByPath(ctx context.Context, tenantID uint64, pathID string) error
}

// GamificationService manages points/levels and learning paths
// (V3.0 §15.4 / §15.5). The award policy (values, daily caps, idempotency)
// and the progress math live here so every future caller (lifecycle publish,
// review, answers, daily login) shares one rule set.
type GamificationService interface {
	// ---- 积分（§15.4）----

	// AwardPoints credits one action for one user, best-effort friendly:
	//   - idempotent: the same (action, source_type, source_id) is credited
	//     once — a repeated call reports Awarded=false, Reason="duplicate";
	//   - rate limited: the per-action daily cap skips further awards with
	//     Reason="daily_cap";
	//   - level auto-promotion: the balance row's level is recomputed from
	//     the new total (bronze 0 / silver 50 / gold 200 / diamond 500 /
	//     expert 1000).
	// The returned PointsAwardResult always carries the current balance when
	// err is nil. Pass sourceType/sourceID as "" only for events without a
	// stable source (idempotency then degrades to the daily cap).
	AwardPoints(
		ctx context.Context, tenantID uint64, userID string,
		action types.PointsAction, sourceType, sourceID string,
	) (*types.PointsAwardResult, error)
	// GetPointsSummary returns balance + level + the user's most recent
	// ledger entries (recentLimit clamped to 1..100).
	GetPointsSummary(ctx context.Context, tenantID uint64, userID string, recentLimit int) (*types.PointsSummary, error)
	// Leaderboard returns the tenant's top users by balance DESC with dense
	// ranks starting at 1 (limit clamped to 1..100).
	Leaderboard(ctx context.Context, tenantID uint64, limit int) ([]*types.LeaderboardEntry, error)

	// ---- 学习路径（§15.5）----

	// CreatePath creates one path (audit: gamification.path_created).
	// Steps are validated (1-based unique orders, titled) and normalized to
	// order-ascending.
	CreatePath(ctx context.Context, path *types.LearningPath) (*types.LearningPath, error)
	// UpdatePath patches name/audience/steps; nil fields keep the current
	// value (audit: gamification.path_updated).
	UpdatePath(
		ctx context.Context, tenantID uint64, pathID string,
		input *types.LearningPathUpdate, actorID string,
	) (*types.LearningPath, error)
	// DeletePath removes one path and its progress rows
	// (audit: gamification.path_deleted).
	DeletePath(ctx context.Context, tenantID uint64, pathID, actorID string) error
	// ListPaths lists the tenant's paths, newest first.
	ListPaths(ctx context.Context, tenantID uint64) ([]*types.LearningPath, error)
	// GetPath resolves one path by ID within the tenant.
	GetPath(ctx context.Context, tenantID uint64, pathID string) (*types.LearningPath, error)

	// AssignProgress starts (or resumes) a user on a path — idempotent:
	// an existing progress row is returned unchanged
	// (audit: gamification.progress_started on first start).
	AssignProgress(ctx context.Context, tenantID uint64, pathID, userID string) (*types.LearningProgress, error)
	// CompleteStep records one completed step by its order:
	// progress_pct = completed/total*100; reaching 100% stamps completed_at
	// and awards PointsActionPathCompleted (+20, idempotent per path)
	// (audit: gamification.progress_step_completed, and
	// gamification.path_completed on the 100% transition).
	CompleteStep(ctx context.Context, tenantID uint64, pathID, userID string, stepOrder int) (*types.LearningProgress, error)
}
