package types

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ============================================================================
// 积分等级 + 学习路径（KnowAxis V3.0 §15.4 / §15.5 的 v1 子集）。
//
// 四张表：user_points_ledger（加分流水，append-only）、user_point_balance
// （余额 + 等级，每人一行）、learning_paths（Admin 维护的有序知识清单）、
// learning_progress（用户在路径上的完成账）。分值表与日上限是服务层的
// 策略常量（internal/application/service/gamification_service.go），本文件
// 只承载词汇表、等级阈值与行模型。
// ============================================================================

// PointsAction 是一次加分行为的类别。词表即迁移 action 列的取值域：
// 每个动作对应分值表中一行，未收录的动作拒绝入账。
type PointsAction string

const (
	// PointsActionKnowledgeCreated 发布知识（lifecycle publish 后触发）。
	PointsActionKnowledgeCreated PointsAction = "knowledge_created"
	// PointsActionViewGiven 知识被浏览（浏览者阅读，积分记给知识归属方）。
	PointsActionViewGiven PointsAction = "view_given"
	// PointsActionLikeGiven 知识被点赞（积分记给知识归属方）。
	PointsActionLikeGiven PointsAction = "like_given"
	// PointsActionReviewDone 完成一次审核动作。
	PointsActionReviewDone PointsAction = "review_done"
	// PointsActionAnswerAdopted 回答被采纳。
	PointsActionAnswerAdopted PointsAction = "answer_adopted"
	// PointsActionDailyLogin 每日登录（source 记日期实现按天幂等）。
	PointsActionDailyLogin PointsAction = "daily_login"
	// PointsActionPathCompleted 学完一条学习路径（100% 时由路径服务触发）。
	PointsActionPathCompleted PointsAction = "path_completed"
)

// Valid reports whether the action is part of the v1 vocabulary.
func (a PointsAction) Valid() bool {
	switch a {
	case PointsActionKnowledgeCreated, PointsActionViewGiven, PointsActionLikeGiven,
		PointsActionReviewDone, PointsActionAnswerAdopted, PointsActionDailyLogin,
		PointsActionPathCompleted:
		return true
	default:
		return false
	}
}

// String renders the action for logs.
func (a PointsAction) String() string { return string(a) }

// PointLevel 是积分等级。阈值见 PointLevelForBalance；v1 只升不降
// （余额只增，无扣分），等级在每次入账时按最新余额重算。
type PointLevel string

const (
	// PointLevelBronze 青铜（默认起点）。
	PointLevelBronze PointLevel = "bronze"
	// PointLevelSilver 白银（≥50）。
	PointLevelSilver PointLevel = "silver"
	// PointLevelGold 黄金（≥200）。
	PointLevelGold PointLevel = "gold"
	// PointLevelDiamond 钻石（≥500）。
	PointLevelDiamond PointLevel = "diamond"
	// PointLevelExpert 专家（≥1000）。
	PointLevelExpert PointLevel = "expert"
)

// Valid reports whether the level is one of the five known tiers.
func (l PointLevel) Valid() bool {
	switch l {
	case PointLevelBronze, PointLevelSilver, PointLevelGold, PointLevelDiamond, PointLevelExpert:
		return true
	default:
		return false
	}
}

// String renders the level for logs.
func (l PointLevel) String() string { return string(l) }

// pointLevelThresholds is the ascending ladder (V3.0 §15.4: bronze 0 /
// silver 50 / gold 200 / diamond 500 / expert 1000). Kept unexported — the
// only sanctioned read path is PointLevelForBalance.
var pointLevelThresholds = []struct {
	Level     PointLevel
	MinPoints int
}{
	{PointLevelBronze, 0},
	{PointLevelSilver, 50},
	{PointLevelGold, 200},
	{PointLevelDiamond, 500},
	{PointLevelExpert, 1000},
}

// PointLevelForBalance maps a balance onto its tier (the highest threshold
// the balance reaches; zero and negative balances stay bronze).
func PointLevelForBalance(balance int) PointLevel {
	level := PointLevelBronze
	for _, tier := range pointLevelThresholds {
		if balance >= tier.MinPoints {
			level = tier.Level
		}
	}
	return level
}

// UserPointsLedger is one append-only award row (V3.0 §15.4).
type UserPointsLedger struct {
	// ID of the ledger row.
	ID string `json:"id" gorm:"type:varchar(36);primaryKey"`
	// TenantID owning the row.
	TenantID uint64 `json:"tenant_id" gorm:"not null"`
	// UserID of the awarded user.
	UserID string `json:"user_id" gorm:"type:varchar(36);not null"`
	// Action is the award category (PointsAction vocabulary).
	Action string `json:"action" gorm:"type:varchar(32);not null"`
	// Points credited for this single event (always > 0 in v1).
	Points int `json:"points" gorm:"not null"`
	// SourceType qualifies the award origin (e.g. "knowledge", "learning_path",
	// "day" for daily login). Empty means the event has no stable source.
	SourceType string `json:"source_type" gorm:"type:varchar(24);default:''"`
	// SourceID is the origin entity (knowledge id / path id / YYYY-MM-DD for
	// daily login). (action, source_type, source_id) drives the service-side
	// idempotency check.
	SourceID string `json:"source_id" gorm:"type:varchar(36);default:''"`
	// CreatedAt of the award.
	CreatedAt time.Time `json:"created_at"`
}

// TableName pins the GORM table name.
func (UserPointsLedger) TableName() string { return "user_points_ledger" }

// BeforeCreate assigns an ID before persistence.
func (l *UserPointsLedger) BeforeCreate(_ *gorm.DB) error {
	if l.ID == "" {
		l.ID = uuid.NewString()
	}
	return nil
}

// UserPointBalance is the per-user balance + tier row (V3.0 §15.4). The
// (tenant_id, user_id) uniqueness lives in migration 000153; the service
// upserts through it.
type UserPointBalance struct {
	// ID of the balance row.
	ID string `json:"id" gorm:"type:varchar(36);primaryKey"`
	// TenantID owning the row.
	TenantID uint64 `json:"tenant_id" gorm:"not null"`
	// UserID of the owner.
	UserID string `json:"user_id" gorm:"type:varchar(36);not null"`
	// Balance is the current point total (append-only in v1).
	Balance int `json:"balance" gorm:"not null;default:0"`
	// Level is the tier derived from Balance (PointLevelForBalance).
	Level string `json:"level" gorm:"type:varchar(16);default:'bronze'"`
	// UpdatedAt of the row (touched by GORM on Updates).
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName pins the GORM table name.
func (UserPointBalance) TableName() string { return "user_point_balance" }

// BeforeCreate assigns an ID before persistence.
func (b *UserPointBalance) BeforeCreate(_ *gorm.DB) error {
	if b.ID == "" {
		b.ID = uuid.NewString()
	}
	return nil
}

// LearningPathStep is one ordered entry of a learning path (V3.0 §15.5).
type LearningPathStep struct {
	// Order is the 1-based position within the path (unique per path;
	// enforced by the service on write).
	Order int `json:"order"`
	// Title is the step headline.
	Title string `json:"title"`
	// KnowledgeID points at the knowledge entry to study; empty means a
	// free-text step (v1 allows offline/offline-training steps).
	KnowledgeID string `json:"knowledge_id"`
	// Description explains what completing the step means.
	Description string `json:"description"`
}

// LearningPath is an Admin-curated ordered knowledge list (V3.0 §15.5).
type LearningPath struct {
	// ID of the path.
	ID string `json:"id" gorm:"type:varchar(36);primaryKey"`
	// TenantID owning the path.
	TenantID uint64 `json:"tenant_id" gorm:"not null"`
	// Name of the path (tenant-scope display name).
	Name string `json:"name" gorm:"type:varchar(128);not null"`
	// Audience tags the target 岗位/部门 (informational in v1).
	Audience string `json:"audience" gorm:"type:varchar(64);default:''"`
	// Steps is the ordered step list as JSONB: [{order, title, knowledge_id,
	// description}]. Marshalled with the local service helper, never inline
	// structs.
	Steps JSON `json:"steps" gorm:"type:jsonb;not null;default:'[]'"`
	// CreatedBy records who created the path (audit identity).
	CreatedBy string `json:"created_by" gorm:"type:varchar(36);not null"`
	// CreatedAt of the path.
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt of the path (touched by GORM on Updates).
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName pins the GORM table name.
func (LearningPath) TableName() string { return "learning_paths" }

// BeforeCreate assigns an ID before persistence.
func (p *LearningPath) BeforeCreate(_ *gorm.DB) error {
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	return nil
}

// DecodeSteps unpacks the JSONB step list; an empty column decodes to an
// empty (non-nil) slice so callers can range it directly.
func (p *LearningPath) DecodeSteps() ([]LearningPathStep, error) {
	steps := make([]LearningPathStep, 0)
	if len(p.Steps) == 0 {
		return steps, nil
	}
	if err := json.Unmarshal(p.Steps, &steps); err != nil {
		return nil, err
	}
	return steps, nil
}

// LearningPathUpdate carries the mutable fields of a path. Nil fields keep
// the current value; pointer fields distinguish "not provided" from "".
type LearningPathUpdate struct {
	// Name renames the path (nil keeps).
	Name *string `json:"name,omitempty"`
	// Audience retags the audience (nil keeps).
	Audience *string `json:"audience,omitempty"`
	// Steps replaces the step list (nil keeps).
	Steps *[]LearningPathStep `json:"steps,omitempty"`
}

// LearningProgress tracks one user's completion of one path (V3.0 §15.5).
// The (path_id, user_id) uniqueness lives in migration 000153.
type LearningProgress struct {
	// ID of the progress row.
	ID string `json:"id" gorm:"type:varchar(36);primaryKey"`
	// TenantID owning the row.
	TenantID uint64 `json:"tenant_id" gorm:"not null"`
	// PathID of the studied path.
	PathID string `json:"path_id" gorm:"type:varchar(36);not null"`
	// UserID of the learner.
	UserID string `json:"user_id" gorm:"type:varchar(36);not null"`
	// CompletedSteps is the JSONB array of completed step orders (ints).
	CompletedSteps JSON `json:"completed_steps" gorm:"type:jsonb;default:'[]'"`
	// ProgressPct is completed/total in percent (0-100, service-computed).
	ProgressPct int `json:"progress_pct" gorm:"default:0"`
	// CompletedAt is stamped once when the path reaches 100% (nil = ongoing).
	CompletedAt *time.Time `json:"completed_at,omitempty" gorm:"type:timestamptz"`
	// UpdatedAt of the row (touched by GORM on Updates).
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName pins the GORM table name.
func (LearningProgress) TableName() string { return "learning_progress" }

// BeforeCreate assigns an ID before persistence.
func (p *LearningProgress) BeforeCreate(_ *gorm.DB) error {
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	return nil
}

// DecodeCompletedSteps unpacks the completed-step orders; an empty column
// decodes to an empty (non-nil) slice.
func (p *LearningProgress) DecodeCompletedSteps() ([]int, error) {
	orders := make([]int, 0)
	if len(p.CompletedSteps) == 0 {
		return orders, nil
	}
	if err := json.Unmarshal(p.CompletedSteps, &orders); err != nil {
		return nil, err
	}
	return orders, nil
}

// PointsAwardResult reports what one AwardPoints call did. Awarded=false is
// a normal outcome (duplicate source or daily cap), not an error.
type PointsAwardResult struct {
	// Awarded is true when a ledger row was written and the balance moved.
	Awarded bool `json:"awarded"`
	// Reason explains a skipped award: "duplicate" (same action + source
	// already credited) or "daily_cap" (per-action daily limit reached).
	// Empty when Awarded is true.
	Reason string `json:"reason"`
	// Points credited on this call (0 when skipped).
	Points int `json:"points"`
	// Balance is the post-call balance row (always present on success).
	Balance *UserPointBalance `json:"balance"`
}

// PointsSummary is the GET /me/points payload: balance + tier + recent
// ledger entries.
type PointsSummary struct {
	// UserID of the queried user.
	UserID string `json:"user_id"`
	// Balance is the current point total (0 when the user has no row yet).
	Balance int `json:"balance"`
	// Level is the current tier.
	Level string `json:"level"`
	// Recent ledger entries, newest first (bounded by the query's limit).
	Recent []*UserPointsLedger `json:"recent"`
}

// LeaderboardEntry is one row of the balance leaderboard. v1 returns the raw
// user_id; display-name masking (e.g. u***d) belongs to the presentation
// layer once a user-profile service is wired — the API stays stable.
type LeaderboardEntry struct {
	// Rank is 1-based, dense, ordered by balance DESC.
	Rank int `json:"rank"`
	// UserID of the ranked user.
	UserID string `json:"user_id"`
	// Balance of the ranked user.
	Balance int `json:"balance"`
	// Level tier of the ranked user.
	Level string `json:"level"`
}
