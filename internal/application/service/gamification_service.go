package service

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"
	"unicode/utf8"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Gamification audit actions (V3.0 §15.4/§15.5). Point awards themselves are
// NOT audited separately — the append-only ledger is the award trail; the
// audit stream covers the path/progress lifecycle that has no ledger of its
// own.
const (
	auditActionGamificationPathCreated   = "gamification.path_created"
	auditActionGamificationPathUpdated   = "gamification.path_updated"
	auditActionGamificationPathDeleted   = "gamification.path_deleted"
	auditActionGamificationProgressStart = "gamification.progress_started"
	auditActionGamificationStepCompleted = "gamification.progress_step_completed"
	auditActionGamificationPathCompleted = "gamification.path_completed"
)

// Award source types (ledger source_type column). They qualify what produced
// the award so idempotency keys never collide across behaviours.
const (
	awardSourceKnowledge    = "knowledge"
	awardSourceLearningPath = "learning_path"
	awardSourceDay          = "day"
)

// maxGamificationPathNameLen mirrors the migration's varchar(128).
const maxGamificationPathNameLen = 128

// maxGamificationAudienceLen mirrors the migration's varchar(64).
const maxGamificationAudienceLen = 64

// maxGamificationPathSteps bounds one path's step list (v1 sanity cap).
const maxGamificationPathSteps = 100

// gamificationRecentLimitCap bounds the /me/points recent-ledger window.
const gamificationRecentLimitCap = 100

// gamificationLeaderboardCap bounds one leaderboard query.
const gamificationLeaderboardCap = 100

// pointsActionValues is the award table (V3.0 §15.4): 知识创建+10 / 审核+3 /
// 被点赞+1 / 回答采纳+5 are the spec-given values; view_given / daily_login /
// path_completed are v1 defaults for the rest of the vocabulary.
var pointsActionValues = map[types.PointsAction]int{
	types.PointsActionKnowledgeCreated: 10,
	types.PointsActionReviewDone:       3,
	types.PointsActionLikeGiven:        1,
	types.PointsActionAnswerAdopted:    5,
	types.PointsActionViewGiven:        1,
	types.PointsActionDailyLogin:       2,
	types.PointsActionPathCompleted:    20,
}

// pointsActionDailyCaps is the anti-farming limit: the most points one user
// can earn from ONE action per calendar day (server-local midnight resets).
// Once the day's sum reaches the cap, further awards of that action are
// skipped (not partially credited) until tomorrow.
var pointsActionDailyCaps = map[types.PointsAction]int{
	types.PointsActionKnowledgeCreated: 50,
	types.PointsActionReviewDone:       30,
	types.PointsActionLikeGiven:        10,
	types.PointsActionAnswerAdopted:    30,
	types.PointsActionViewGiven:        10,
	types.PointsActionDailyLogin:       2, // one login already hits the cap
	types.PointsActionPathCompleted:    60,
}

// gamificationService implements interfaces.GamificationService.
type gamificationService struct {
	repo  interfaces.GamificationRepository
	audit interfaces.AuditLogService
	now   func() time.Time
}

// NewGamificationService constructs the gamification service.
func NewGamificationService(
	repo interfaces.GamificationRepository,
	audit interfaces.AuditLogService,
) interfaces.GamificationService {
	return &gamificationService{repo: repo, audit: audit, now: time.Now}
}

// gamificationAuditJSON marshals an audit detail payload, falling back to an
// empty object on failure so logging never blocks a gamification write. Kept
// local to this vertical and deliberately not named like the
// glossary/lifecycle/feedback helpers (same keep-verticals-independent
// rationale).
func gamificationAuditJSON(v interface{}) types.JSON {
	data, err := json.Marshal(v)
	if err != nil {
		return types.JSON([]byte("{}"))
	}
	return types.JSON(data)
}

// gamificationStepsJSON marshals the step list into the JSONB payload,
// falling back to an empty array on failure so the NOT NULL column always
// holds a JSON array. Local to this vertical.
func gamificationStepsJSON(steps []types.LearningPathStep) types.JSON {
	data, err := json.Marshal(steps)
	if err != nil {
		return types.JSON([]byte("[]"))
	}
	return types.JSON(data)
}

// ============================================================================
// 积分（V3.0 §15.4）
// ============================================================================

// AwardPoints credits one action for one user.
//
// Order of checks: unknown action (caller bug, 400) → source idempotency
// ("同 source 同 action 不重复记分"; the UNIQUE semantics live in this service
// lookup, not in a DB constraint, so events without a stable source just
// skip the check) → per-action daily cap. Only then is the ledger row
// written and the balance upserted with the level recomputed from the new
// total (等级自动晋升; v1 has no deductions so levels never drop).
//
// v1 caveat: the balance read-modify-write is not transactional — two
// concurrent awards for the same user can lose one increment. Acceptable for
// a gamification counter; wrap in a transaction when the first deduction
// use-case lands.
func (s *gamificationService) AwardPoints(
	ctx context.Context, tenantID uint64, userID string,
	action types.PointsAction, sourceType, sourceID string,
) (*types.PointsAwardResult, error) {
	if !action.Valid() {
		return nil, apperrors.NewBadRequestError("unknown points action: " + string(action))
	}
	if userID == "" {
		return nil, apperrors.NewBadRequestError("user_id is required")
	}
	points, ok := pointsActionValues[action]
	if !ok || points <= 0 {
		return nil, apperrors.NewBadRequestError("points action has no value configured: " + string(action))
	}
	if sourceType == "" || sourceID == "" {
		// 无稳定来源的事件（防刷只靠日上限）；归一空串保证查重键一致。
		sourceType = ""
		sourceID = ""
	}

	skip := func(reason string) (*types.PointsAwardResult, error) {
		balance, err := s.currentBalance(ctx, tenantID, userID)
		if err != nil {
			return nil, err
		}
		return &types.PointsAwardResult{Awarded: false, Reason: reason, Balance: balance}, nil
	}

	// 幂等：同 (action, source_type, source_id) 只记一次分。
	if sourceID != "" {
		if _, err := s.repo.FindLedgerEntry(ctx, tenantID, userID, action, sourceType, sourceID); err == nil {
			return skip("duplicate")
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}

	// 日上限防刷：当天该动作的累计已达上限则跳过（不部分入账）。
	if dailyCap, capped := pointsActionDailyCaps[action]; capped {
		now := s.now()
		dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		sum, err := s.repo.SumPointsSince(ctx, tenantID, userID, action, dayStart)
		if err != nil {
			return nil, err
		}
		if sum >= dailyCap {
			return skip("daily_cap")
		}
	}

	now := s.now()
	entry := &types.UserPointsLedger{
		TenantID:   tenantID,
		UserID:     userID,
		Action:     string(action),
		Points:     points,
		SourceType: sourceType,
		SourceID:   sourceID,
		CreatedAt:  now,
	}
	if err := s.repo.InsertLedgerEntry(ctx, entry); err != nil {
		return nil, err
	}

	balance, err := s.applyPoints(ctx, tenantID, userID, points, now)
	if err != nil {
		return nil, err
	}
	return &types.PointsAwardResult{Awarded: true, Points: points, Balance: balance}, nil
}

// GetPointsSummary returns balance + level + the most recent ledger entries.
// A user who has never been awarded reads as a zero bronze balance without
// creating a row (no write on read).
func (s *gamificationService) GetPointsSummary(
	ctx context.Context, tenantID uint64, userID string, recentLimit int,
) (*types.PointsSummary, error) {
	if userID == "" {
		return nil, apperrors.NewBadRequestError("user_id is required")
	}
	if recentLimit <= 0 {
		recentLimit = 20
	}
	if recentLimit > gamificationRecentLimitCap {
		recentLimit = gamificationRecentLimitCap
	}
	balance, err := s.currentBalance(ctx, tenantID, userID)
	if err != nil {
		return nil, err
	}
	recent, err := s.repo.ListLedger(ctx, tenantID, userID, recentLimit)
	if err != nil {
		return nil, err
	}
	return &types.PointsSummary{
		UserID:  userID,
		Balance: balance.Balance,
		Level:   balance.Level,
		Recent:  recent,
	}, nil
}

// Leaderboard returns the tenant's top users by balance DESC with dense
// ranks. v1 returns raw user_id — masking (e.g. 昵称/工号脱敏) belongs to the
// presentation layer once a user-profile service is wired; see
// types.LeaderboardEntry.
func (s *gamificationService) Leaderboard(
	ctx context.Context, tenantID uint64, limit int,
) ([]*types.LeaderboardEntry, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > gamificationLeaderboardCap {
		limit = gamificationLeaderboardCap
	}
	balances, err := s.repo.ListBalances(ctx, tenantID, limit)
	if err != nil {
		return nil, err
	}
	entries := make([]*types.LeaderboardEntry, 0, len(balances))
	for i, row := range balances {
		entries = append(entries, &types.LeaderboardEntry{
			Rank:    i + 1,
			UserID:  row.UserID,
			Balance: row.Balance,
			Level:   row.Level,
		})
	}
	return entries, nil
}

// applyPoints moves the balance and recomputes the level (read-modify-write
// through the UNIQUE(tenant_id, user_id) upsert; see the AwardPoints caveat).
func (s *gamificationService) applyPoints(
	ctx context.Context, tenantID uint64, userID string, points int, now time.Time,
) (*types.UserPointBalance, error) {
	balance, err := s.repo.GetBalance(ctx, tenantID, userID)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		balance = &types.UserPointBalance{
			TenantID:  tenantID,
			UserID:    userID,
			Balance:   0,
			Level:     string(types.PointLevelBronze),
			UpdatedAt: now,
		}
	}
	balance.Balance += points
	balance.Level = string(types.PointLevelForBalance(balance.Balance))
	balance.UpdatedAt = now
	if err := s.repo.UpsertBalance(ctx, balance); err != nil {
		return nil, err
	}
	return balance, nil
}

// currentBalance reads the balance, synthesizing zero-bronze for users
// without a row (callers get a usable value without a write on read).
func (s *gamificationService) currentBalance(
	ctx context.Context, tenantID uint64, userID string,
) (*types.UserPointBalance, error) {
	balance, err := s.repo.GetBalance(ctx, tenantID, userID)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		return &types.UserPointBalance{
			TenantID: tenantID,
			UserID:   userID,
			Balance:  0,
			Level:    string(types.PointLevelBronze),
		}, nil
	}
	return balance, nil
}

// ============================================================================
// 学习路径（V3.0 §15.5）
// ============================================================================

// CreatePath creates one path. Steps are validated (1-based unique orders,
// titled, bounded) and normalized to order-ascending before persisting.
func (s *gamificationService) CreatePath(
	ctx context.Context, path *types.LearningPath,
) (*types.LearningPath, error) {
	if path == nil {
		return nil, apperrors.NewBadRequestError("path body is required")
	}
	if err := validateGamificationPathFields(path.Name, path.Audience); err != nil {
		return nil, err
	}
	incoming, err := path.DecodeSteps()
	if err != nil {
		return nil, apperrors.NewBadRequestError("path steps are malformed")
	}
	steps, err := normalizeGamificationSteps(incoming)
	if err != nil {
		return nil, err
	}
	path.ID = uuid.NewString()
	path.Steps = gamificationStepsJSON(steps)
	if err := s.repo.CreatePath(ctx, path); err != nil {
		return nil, err
	}
	s.logAudit(ctx, path.TenantID, path.CreatedBy, auditActionGamificationPathCreated,
		path.ID, map[string]interface{}{
			"name":       path.Name,
			"audience":   path.Audience,
			"step_count": len(steps),
		})
	return path, nil
}

// UpdatePath patches the mutable fields of one path. Step replacement
// re-runs the same validation/normalization as create.
func (s *gamificationService) UpdatePath(
	ctx context.Context, tenantID uint64, pathID string,
	input *types.LearningPathUpdate, actorID string,
) (*types.LearningPath, error) {
	if input == nil {
		return nil, apperrors.NewBadRequestError("update body is required")
	}
	path, err := s.loadPath(ctx, tenantID, pathID)
	if err != nil {
		return nil, err
	}
	if input.Name != nil {
		path.Name = *input.Name
	}
	if input.Audience != nil {
		path.Audience = *input.Audience
	}
	if input.Steps != nil {
		steps, err := normalizeGamificationSteps(*input.Steps)
		if err != nil {
			return nil, err
		}
		path.Steps = gamificationStepsJSON(steps)
	}
	if err := validateGamificationPathFields(path.Name, path.Audience); err != nil {
		return nil, err
	}
	if err := s.repo.UpdatePath(ctx, path); err != nil {
		return nil, err
	}
	s.logAudit(ctx, tenantID, actorID, auditActionGamificationPathUpdated,
		pathID, map[string]interface{}{"name": path.Name})
	return path, nil
}

// DeletePath removes one path and its progress rows (v1: hard delete —
// gamification artefacts are not governed content; the audit event keeps the
// trail).
func (s *gamificationService) DeletePath(ctx context.Context, tenantID uint64, pathID, actorID string) error {
	path, err := s.loadPath(ctx, tenantID, pathID)
	if err != nil {
		return err
	}
	if err := s.repo.DeletePath(ctx, tenantID, pathID); err != nil {
		return err
	}
	// 进度行随路径删除（悬挂进度没有意义）。
	if err := s.repo.DeleteProgressByPath(ctx, tenantID, pathID); err != nil {
		// 路径已删：孤儿进度只记日志，不让删除失败。
		logger.Warnf(ctx, "failed to delete progress rows for path %s: %v", pathID, err)
	}
	s.logAudit(ctx, tenantID, actorID, auditActionGamificationPathDeleted,
		pathID, map[string]interface{}{"name": path.Name})
	return nil
}

// ListPaths lists the tenant's paths, newest first.
func (s *gamificationService) ListPaths(ctx context.Context, tenantID uint64) ([]*types.LearningPath, error) {
	return s.repo.ListPaths(ctx, tenantID)
}

// GetPath resolves one path by ID within the tenant.
func (s *gamificationService) GetPath(ctx context.Context, tenantID uint64, pathID string) (*types.LearningPath, error) {
	return s.loadPath(ctx, tenantID, pathID)
}

// AssignProgress starts (or resumes) a user on a path — idempotent: an
// existing progress row is returned unchanged. The first start is audited.
func (s *gamificationService) AssignProgress(
	ctx context.Context, tenantID uint64, pathID, userID string,
) (*types.LearningProgress, error) {
	if userID == "" {
		return nil, apperrors.NewBadRequestError("user_id is required")
	}
	if _, err := s.loadPath(ctx, tenantID, pathID); err != nil {
		return nil, err
	}
	existing, err := s.repo.GetProgress(ctx, tenantID, pathID, userID)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	progress := &types.LearningProgress{
		TenantID:       tenantID,
		PathID:         pathID,
		UserID:         userID,
		CompletedSteps: types.JSON([]byte("[]")),
		ProgressPct:    0,
	}
	if err := s.repo.CreateProgress(ctx, progress); err != nil {
		return nil, err
	}
	s.logAudit(ctx, tenantID, userID, auditActionGamificationProgressStart,
		pathID, map[string]interface{}{"path_id": pathID, "user_id": userID})
	return progress, nil
}

// CompleteStep records one completed step by its order. Completing an
// already-completed step is a no-op (idempotent); reaching 100% stamps
// completed_at once and awards PointsActionPathCompleted
// (idempotent per path via the ledger source key).
func (s *gamificationService) CompleteStep(
	ctx context.Context, tenantID uint64, pathID, userID string, stepOrder int,
) (*types.LearningProgress, error) {
	if userID == "" {
		return nil, apperrors.NewBadRequestError("user_id is required")
	}
	if stepOrder < 1 {
		return nil, apperrors.NewBadRequestError("step_order must be >= 1")
	}
	path, err := s.loadPath(ctx, tenantID, pathID)
	if err != nil {
		return nil, err
	}
	steps, err := path.DecodeSteps()
	if err != nil {
		return nil, apperrors.NewBadRequestError("path steps are malformed")
	}
	found := false
	for _, step := range steps {
		if step.Order == stepOrder {
			found = true
			break
		}
	}
	if !found {
		return nil, apperrors.NewBadRequestError("step is not part of this path")
	}

	progress, err := s.repo.GetProgress(ctx, tenantID, pathID, userID)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		// 未 start 就 complete：按需开账（AssignProgress 的幂等语义保持）。
		progress = &types.LearningProgress{
			TenantID:       tenantID,
			PathID:         pathID,
			UserID:         userID,
			CompletedSteps: types.JSON([]byte("[]")),
			ProgressPct:    0,
		}
	}
	completed, err := progress.DecodeCompletedSteps()
	if err != nil {
		return nil, apperrors.NewBadRequestError("learning progress is malformed")
	}
	for _, order := range completed {
		if order == stepOrder {
			return progress, nil
		}
	}
	completed = append(completed, stepOrder)
	sort.Ints(completed)
	progress.CompletedSteps = gamificationStepsOrdersJSON(completed)
	progress.ProgressPct = 0
	if total := len(steps); total > 0 {
		progress.ProgressPct = len(completed) * 100 / total
	}

	finished := len(steps) > 0 && len(completed) == len(steps)
	// justFinished 仅在本次调用首次达 100% 时为真（路径缩步导致"已是
	// 完成态"的重复调用不会重复落 completed_at/重复加分）。
	justFinished := finished && progress.CompletedAt == nil
	now := s.now()
	if justFinished {
		progress.CompletedAt = &now
	}
	if err := s.repo.SaveProgress(ctx, progress); err != nil {
		return nil, err
	}

	s.logAudit(ctx, tenantID, userID, auditActionGamificationStepCompleted,
		pathID, map[string]interface{}{
			"step_order":   stepOrder,
			"progress_pct": progress.ProgressPct,
		})
	if justFinished {
		// 恰在本次调用达 100%：落 completed_at 审计并加分（加分失败不回滚
		// 进度，只记日志——与其它垂直的 best-effort 策略一致；账本幂等键
		// 为 (path_completed, learning_path, path_id)，重复触发不会重复加分）。
		s.logAudit(ctx, tenantID, userID, auditActionGamificationPathCompleted,
			pathID, map[string]interface{}{"path_id": pathID, "user_id": userID})
		if _, err := s.AwardPoints(ctx, tenantID, userID, types.PointsActionPathCompleted,
			awardSourceLearningPath, pathID); err != nil {
			logger.Warnf(ctx, "failed to award path_completed points for path %s user %s: %v",
				pathID, userID, err)
		}
	}
	return progress, nil
}

// gamificationStepsOrdersJSON marshals the completed-step orders into the
// JSONB payload, falling back to an empty array on failure. Local to this
// vertical.
func gamificationStepsOrdersJSON(orders []int) types.JSON {
	data, err := json.Marshal(orders)
	if err != nil {
		return types.JSON([]byte("[]"))
	}
	return types.JSON(data)
}

// validateGamificationPathFields checks the name/audience lengths against
// the migration's varchar bounds (rune-counted, same policy as glossary).
func validateGamificationPathFields(name, audience string) error {
	if name == "" {
		return apperrors.NewBadRequestError("name is required")
	}
	if utf8.RuneCountInString(name) > maxGamificationPathNameLen {
		return apperrors.NewBadRequestError("name is too long (max 128 chars)")
	}
	if utf8.RuneCountInString(audience) > maxGamificationAudienceLen {
		return apperrors.NewBadRequestError("audience is too long (max 64 chars)")
	}
	return nil
}

// normalizeGamificationSteps validates the incoming step list and returns
// the order-ascending normalized copy. Rules: 1-based orders, unique within
// the path, non-empty titles, bounded count.
func normalizeGamificationSteps(incoming []types.LearningPathStep) ([]types.LearningPathStep, error) {
	if len(incoming) == 0 {
		return []types.LearningPathStep{}, nil
	}
	if len(incoming) > maxGamificationPathSteps {
		return nil, apperrors.NewBadRequestError("too many steps (max 100)")
	}
	seen := make(map[int]struct{}, len(incoming))
	steps := make([]types.LearningPathStep, 0, len(incoming))
	for _, step := range incoming {
		if step.Order < 1 {
			return nil, apperrors.NewBadRequestError("step order must be >= 1")
		}
		if _, dup := seen[step.Order]; dup {
			return nil, apperrors.NewBadRequestError("duplicate step order")
		}
		seen[step.Order] = struct{}{}
		if step.Title == "" {
			return nil, apperrors.NewBadRequestError("step title is required")
		}
		steps = append(steps, step)
	}
	sort.Slice(steps, func(i, j int) bool { return steps[i].Order < steps[j].Order })
	return steps, nil
}

// loadPath resolves a tenant-scoped path, translating record-not-found into
// the domain error the handler layer maps to 404.
func (s *gamificationService) loadPath(ctx context.Context, tenantID uint64, pathID string) (*types.LearningPath, error) {
	path, err := s.repo.GetPath(ctx, tenantID, pathID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperrors.NewNotFoundError("learning path not found")
		}
		return nil, err
	}
	return path, nil
}

// logAudit writes one gamification audit event; audit failures never block
// the business write (same policy as the other verticals).
func (s *gamificationService) logAudit(
	ctx context.Context, tenantID uint64, actorID, action, pathID string,
	details map[string]interface{},
) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Log(ctx, &types.AuditLog{
		TenantID:    tenantID,
		ActorUserID: actorID,
		Action:      types.AuditAction(action),
		ScopeType:   "gamification",
		TargetType:  "learning_path",
		TargetID:    pathID,
		Outcome:     "success",
		Details:     gamificationAuditJSON(details),
	})
}
