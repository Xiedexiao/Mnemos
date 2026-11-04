package types

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ============================================================================
// Token 预算与模型降级链（KnowAxis V3.0 §16.5 成本控制 / §16.1 模型治理）。
//
// 平台已经有 token 记账（model_usage_daily 按租户/日/模型/场景/空间累计），
// 但没有任何**控制**：花超了没人知道，主力模型不可用时整条问答一起失败。
//
// 三个决定：
//  1. 预算只告警、不阻断。达到阈值就发信号，但不拒绝调用——拒答一次问答的
//     代价通常高于多花的那点 token，而"预算"在没有计费口径（不同模型单价
//     不同）的前提下也不足以支撑硬性拦截。真正需要硬限制的租户应当用模型
//     配额，那是另一个功能。
//  2. 告警分三档（80% 提醒 / 100% 告警 / 120% 严重），且**只在跨档时触发**。
//     每次调用都发一条"已用 83%"等于没有告警：人会在第二天开始忽略它。
//  3. 降级链是**有序候选**，不是"随便换一个"。顺序由配置给出：主力失败时
//     先试同级别的备用模型，再试更便宜的。降级必须留痕（哪次调用降级了、
//     从哪个模型换到了哪个），否则成本异常时会找不到原因。
// ============================================================================

// TokenBudgetPeriod is the window a budget applies to.
type TokenBudgetPeriod string

const (
	// TokenBudgetDaily resets every UTC day.
	TokenBudgetDaily TokenBudgetPeriod = "daily"
	// TokenBudgetMonthly resets on the first of each UTC month.
	TokenBudgetMonthly TokenBudgetPeriod = "monthly"
)

// Valid reports whether the period is known.
func (p TokenBudgetPeriod) Valid() bool {
	return p == TokenBudgetDaily || p == TokenBudgetMonthly
}

// TokenBudget is one tenant's spend ceiling for a period.
type TokenBudget struct {
	// TenantID owning the budget.
	TenantID uint64 `json:"tenant_id"`
	// Period the ceiling applies to.
	Period TokenBudgetPeriod `json:"period"`
	// LimitTokens is the ceiling in total tokens (prompt + completion).
	LimitTokens int64 `json:"limit_tokens"`
	// WarnPercent is the first notification threshold (default 80).
	WarnPercent int `json:"warn_percent"`
	// UpdatedAt records the last change.
	UpdatedAt time.Time `json:"updated_at"`
}

const (
	// DefaultTokenBudgetWarnPercent is where the first notice fires.
	DefaultTokenBudgetWarnPercent = 80
	// TokenBudgetWarnPercent is the "still fine but look at it" band.
	TokenBudgetWarnPercent = 80
	// TokenBudgetExceededPercent is the "budget is spent" band.
	TokenBudgetExceededPercent = 100
	// TokenBudgetCriticalPercent is the "significantly over" band.
	TokenBudgetCriticalPercent = 120
)

// TokenBudgetStatus is the §16.5 alert state.
type TokenBudgetStatus string

const (
	// TokenBudgetOK is under the warn threshold.
	TokenBudgetOK TokenBudgetStatus = "ok"
	// TokenBudgetWarn crossed the warn threshold.
	TokenBudgetWarn TokenBudgetStatus = "warn"
	// TokenBudgetExceeded spent the whole budget.
	TokenBudgetExceeded TokenBudgetStatus = "exceeded"
	// TokenBudgetCritical is well over budget.
	TokenBudgetCritical TokenBudgetStatus = "critical"
)

// Valid reports whether the status is known.
func (s TokenBudgetStatus) Valid() bool {
	switch s {
	case TokenBudgetOK, TokenBudgetWarn, TokenBudgetExceeded, TokenBudgetCritical:
		return true
	default:
		return false
	}
}

// severityRank orders the bands so "did we cross into a worse one" is decidable.
func (s TokenBudgetStatus) severityRank() int {
	switch s {
	case TokenBudgetCritical:
		return 3
	case TokenBudgetExceeded:
		return 2
	case TokenBudgetWarn:
		return 1
	default:
		return 0
	}
}

// TokenBudgetAssessment is one evaluation of spend against a ceiling.
type TokenBudgetAssessment struct {
	// TenantID / Period echo the budget.
	TenantID uint64            `json:"tenant_id"`
	Period   TokenBudgetPeriod `json:"period"`
	// LimitTokens / UsedTokens are the raw numbers.
	LimitTokens int64 `json:"limit_tokens"`
	UsedTokens  int64 `json:"used_tokens"`
	// UsedPercent is UsedTokens / LimitTokens × 100, 0 when no limit is set.
	UsedPercent float64 `json:"used_percent"`
	// Status is the current band.
	Status TokenBudgetStatus `json:"status"`
	// ShouldNotify is true only when this assessment crossed into a worse band
	// than previousStatus. Repeated calls inside a band stay silent.
	ShouldNotify bool `json:"should_notify"`
	// PreviousStatus is what the caller last reported (empty on first check).
	PreviousStatus TokenBudgetStatus `json:"previous_status,omitempty"`
	// Note explains a missing limit or a caveat.
	Note string `json:"note,omitempty"`
}

// NormalizeTokenBudget fills defaults and validates the ceiling.
func NormalizeTokenBudget(budget TokenBudget) (TokenBudget, error) {
	if !budget.Period.Valid() {
		return budget, fmt.Errorf("period must be daily or monthly")
	}
	if budget.LimitTokens < 0 {
		return budget, fmt.Errorf("limit_tokens must not be negative")
	}
	if budget.WarnPercent <= 0 || budget.WarnPercent > 100 {
		budget.WarnPercent = DefaultTokenBudgetWarnPercent
	}
	return budget, nil
}

// AssessTokenBudget compares spend against a ceiling.
//
// previousStatus 让"只在跨档时通知"成为可能：同一个档位内反复调用保持安静。
// 传入空串表示这是本次窗口的第一次检查，任何非 ok 档位都会通知。
func AssessTokenBudget(
	budget TokenBudget, usedTokens int64, previousStatus TokenBudgetStatus,
) TokenBudgetAssessment {
	assessment := TokenBudgetAssessment{
		TenantID:       budget.TenantID,
		Period:         budget.Period,
		LimitTokens:    budget.LimitTokens,
		UsedTokens:     usedTokens,
		PreviousStatus: previousStatus,
	}
	if budget.LimitTokens <= 0 {
		// 没配预算：状态是 ok，但要说明"这不是没超支，而是没设上限"。
		assessment.Status = TokenBudgetOK
		assessment.Note = "未设置预算上限，不做用量判断"
		return assessment
	}
	assessment.UsedPercent = float64(usedTokens) / float64(budget.LimitTokens) * 100

	warnAt := float64(budget.WarnPercent)
	if warnAt <= 0 || warnAt > 100 {
		warnAt = DefaultTokenBudgetWarnPercent
	}
	switch {
	case assessment.UsedPercent >= TokenBudgetCriticalPercent:
		assessment.Status = TokenBudgetCritical
	case assessment.UsedPercent >= TokenBudgetExceededPercent:
		assessment.Status = TokenBudgetExceeded
	case assessment.UsedPercent >= warnAt:
		assessment.Status = TokenBudgetWarn
	default:
		assessment.Status = TokenBudgetOK
	}
	// 只在跨进更差的一档时通知；档位相同或回落都不发。
	if assessment.Status.severityRank() > previousStatus.severityRank() {
		assessment.ShouldNotify = true
	}
	return assessment
}

// TokenBudgetWindowStart returns the start of the budget's period at `now`.
//
// 用 UTC 边界：记账表的 day 列就是 UTC 日历日，两处口径不一致会让"今天用了
// 多少"在跨时区时对不上。
func TokenBudgetWindowStart(period TokenBudgetPeriod, now time.Time) time.Time {
	utc := now.UTC()
	switch period {
	case TokenBudgetMonthly:
		return time.Date(utc.Year(), utc.Month(), 1, 0, 0, 0, 0, time.UTC)
	default:
		return time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
	}
}

// ============================================================================
// 模型降级链
// ============================================================================

const (
	// MaxModelFallbackChain bounds one chain.
	//
	// 链越长，一次调用在最坏情况下的延迟越长（每级都要等超时）。4 级已经
	// 覆盖"主力 → 同级备用 → 便宜备用 → 兜底"，再长就不是降级而是重试风暴。
	MaxModelFallbackChain = 4
)

// ModelFallbackChain is an ordered list of models to try for one scene.
//
// 顺序即优先级：第一个是主力，其后依次是降级候选。空链表示"没有配置降级"，
// 此时行为与改动前完全一致（只用主力，失败即失败）。
type ModelFallbackChain struct {
	// TenantID owning the chain.
	TenantID uint64 `json:"tenant_id"`
	// Scene the chain applies to (chat / summary / embedding / ...).
	Scene string `json:"scene"`
	// ModelIDs in priority order. serializer:json keeps the column readable
	// as a JSON array while the Go side stays a plain slice.
	ModelIDs []string `json:"model_ids" gorm:"serializer:json;type:json;not null;default:'[]'"`
	// Description explains the intent (shown in the admin console).
	Description string    `json:"description"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// NormalizeModelFallbackChain trims, dedupes and bounds a chain.
//
// 去重是必须的：链里出现同一个模型两次，第一次失败后第二次会立刻用同样的
// 参数再撞一次同样的错误，只是把延迟翻倍。空 ID 同理直接丢弃。
func NormalizeModelFallbackChain(chain ModelFallbackChain) (ModelFallbackChain, error) {
	scene := strings.TrimSpace(chain.Scene)
	if scene == "" {
		return chain, fmt.Errorf("scene is required")
	}
	seen := make(map[string]struct{}, len(chain.ModelIDs))
	cleaned := make([]string, 0, len(chain.ModelIDs))
	for _, id := range chain.ModelIDs {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" {
			continue
		}
		if _, duplicate := seen[trimmed]; duplicate {
			continue
		}
		seen[trimmed] = struct{}{}
		cleaned = append(cleaned, trimmed)
	}
	if len(cleaned) > MaxModelFallbackChain {
		cleaned = cleaned[:MaxModelFallbackChain]
	}
	if len(cleaned) == 0 {
		return chain, fmt.Errorf("at least one model is required")
	}
	chain.Scene = scene
	chain.ModelIDs = cleaned
	chain.Description = strings.TrimSpace(chain.Description)
	return chain, nil
}

// ModelFallbackAttempt is one level's outcome. Kept for the audit trail.
type ModelFallbackAttempt struct {
	// ModelID tried at this level.
	ModelID string `json:"model_id"`
	// Level is the 0-based position in the chain.
	Level int `json:"level"`
	// Err is the failure reason ("" when the attempt succeeded).
	Err string `json:"error,omitempty"`
}

// ModelFallbackOutcome reports which model served the call and what was tried.
type ModelFallbackOutcome struct {
	// ModelID that succeeded ("" when every level failed).
	ModelID string `json:"model_id,omitempty"`
	// Attempts lists the levels in order.
	Attempts []ModelFallbackAttempt `json:"attempts"`
	// Degraded is true when the serving model was not the primary.
	Degraded bool `json:"degraded"`
	// Exhausted is true when every level failed.
	Exhausted bool `json:"exhausted"`
}

// PrimaryModelID returns the chain's first entry ("" for an empty chain).
func (c ModelFallbackChain) PrimaryModelID() string {
	if len(c.ModelIDs) == 0 {
		return ""
	}
	return c.ModelIDs[0]
}

// CandidatesFor returns the models to try, starting at the primary.
//
// 没有配置链时返回单一候选（调用方传入的主模型）：这样"未配置降级"的部署
// 行为与改动前一致，不需要在调用点分叉。
func (c ModelFallbackChain) CandidatesFor(primaryModelID string) []string {
	if len(c.ModelIDs) == 0 {
		if strings.TrimSpace(primaryModelID) == "" {
			return nil
		}
		return []string{strings.TrimSpace(primaryModelID)}
	}
	return append([]string(nil), c.ModelIDs...)
}

// DescribeFallbackOutcome renders one line for the audit details.
func DescribeFallbackOutcome(outcome ModelFallbackOutcome) string {
	if outcome.Exhausted {
		return fmt.Sprintf("every model failed (%d tried)", len(outcome.Attempts))
	}
	if !outcome.Degraded {
		return "primary model served the call"
	}
	return fmt.Sprintf("degraded to %s after %d failed attempt(s)",
		outcome.ModelID, len(outcome.Attempts)-1)
}

// ============================================================================
// 通知台账
// ============================================================================

// TokenBudgetNotice records the last budget band reported for one window.
//
// 一行 = (租户, 周期, 窗口起点)。它是"只在跨档时通知"的实现载体：没有这张
// 表，进程重启后无法知道上次报到了哪一档，于是每轮检查都会重新通知。
type TokenBudgetNotice struct {
	ID       string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID uint64 `json:"tenant_id" gorm:"not null;uniqueIndex:uq_token_budget_notices,priority:1"`
	// Period is daily or monthly.
	Period string `json:"period" gorm:"type:varchar(16);not null;uniqueIndex:uq_token_budget_notices,priority:2"`
	// WindowStart is the UTC start of the window the notice belongs to.
	WindowStart time.Time `json:"window_start" gorm:"not null;uniqueIndex:uq_token_budget_notices,priority:3"`
	// Status is the band that was reported.
	Status string `json:"status" gorm:"type:varchar(16);not null"`
	// NotifiedAt is when it was reported.
	NotifiedAt time.Time `json:"notified_at"`
}

// TableName pins the GORM table name.
func (TokenBudgetNotice) TableName() string { return "token_budget_notices" }

// BeforeCreate assigns an ID before persistence.
func (n *TokenBudgetNotice) BeforeCreate(_ *gorm.DB) error {
	if n.ID == "" {
		n.ID = uuid.NewString()
	}
	return nil
}
