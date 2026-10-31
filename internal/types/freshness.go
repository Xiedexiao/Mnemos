package types

import (
	"math"
	"sort"
	"time"
)

// Freshness scoring (KnowAxis V3.0 §7.9 v1).
//
// The score answers "how trustworthy is this entry's currency, right now"
// for one knowledge row. It is a pure weighted sum of six signals; two of
// them are platform capabilities that V1 cannot observe yet, so they
// always contribute 0 and are commented as such at their signal functions
// below. Because of that the V1 ceiling of Total is
// FreshnessV1MaxScore (70), not 100 — a perfectly maintained entry
// scores 70, and the grade bands are calibrated against that ceiling.
//
// The function is pure: no clock, no IO, no globals. Tests pass their own
// `now`. Callers (handler / scheduler / queue) own the clock.

// Freshness signal weights (§7.9). They sum to 100; with the two V1-blind
// signals pinned at 0 the reachable maximum is 70.
const (
	// FreshnessWeightValidity 显式有效期 30%：expires_at 是否已被越过。
	FreshnessWeightValidity = 30
	// FreshnessWeightSource 源系统状态 20%。V1 无连接器：无法观测上游
	// 系统里源文档是否仍在/已改动，此信号恒为 0。连接器接入后由
	// datasource sync 写入的 last_sync 信号替换。
	FreshnessWeightSource = 20
	// FreshnessWeightVersion 版本当前性 15%：current_version > 1 说明
	// 内容经历过治理化的更新发布（有版本快照记录），加分。
	FreshnessWeightVersion = 15
	// FreshnessWeightReview 复审周期 15%：距上次复审（无复审记录则退回
	// created_at）的时长相对 review_cycle_days 的超期程度。
	FreshnessWeightReview = 15
	// FreshnessWeightConflict 冲突 10%：lifecycle 处于 conflict 或 stale
	// 都意味着"存在相互矛盾/过期证据"，该信号归零。
	FreshnessWeightConflict = 10
	// FreshnessWeightFeedback 反馈 10%。V1 无 quality_tasks 联动：用户
	// 反馈（答错/过时举报）尚未回写为评分信号，恒为 0。后续与
	// quality_tasks 打通后在信号函数内接入。
	FreshnessWeightFeedback = 10
)

// FreshnessV1MaxScore 是 V1 可达的分数上限：源系统状态与反馈两个信号
// 尚无数据源，恒为 0。Grade 阈值按该上限校准（见 FreshnessGrade* 常量）。
const FreshnessV1MaxScore = FreshnessWeightValidity + FreshnessWeightVersion +
	FreshnessWeightReview + FreshnessWeightConflict

// Grade bands. Calibrated against FreshnessV1MaxScore = 70:
//   - good     ≥ 80% of the reachable ceiling (56)
//   - aging    ≥ 60% (42)
//   - stale    ≥ 40% (28)
//   - critical < 40%
//
// On top of the banded grade two deterministic overrides apply (documented
// in FromKnowledge): an entry past its expires_at is critical, and an entry
// whose lifecycle carries contradicting evidence (conflict/stale) grades at
// most stale — neither may back an authoritative answer, so the grade must
// not read "good" even when the weighted sum is high.
const (
	freshnessGoodThreshold     = 56
	freshnessAgingThreshold    = 42
	freshnessStaleThreshold    = 28
	freshnessVersionBaseFactor = 0.5 // current_version == 1 的基础得分比例
)

// FreshnessGrade 是新鲜度档位（good/aging/stale/critical）。
type FreshnessGrade string

const (
	// FreshnessGradeGood 新鲜：可作权威答案。
	FreshnessGradeGood FreshnessGrade = "good"
	// FreshnessGradeAging 开始老化：临近复审/过期阈值，建议关注。
	FreshnessGradeAging FreshnessGrade = "aging"
	// FreshnessGradeStale 陈旧：超期明显或存在矛盾证据，不作为权威答案。
	FreshnessGradeStale FreshnessGrade = "stale"
	// FreshnessGradeCritical 危急：已过期/严重超期，默认不回答。
	FreshnessGradeCritical FreshnessGrade = "critical"
)

// rank 越大档位越差，用于覆盖规则的"取更差档"。
func (g FreshnessGrade) rank() int {
	switch g {
	case FreshnessGradeGood:
		return 0
	case FreshnessGradeAging:
		return 1
	case FreshnessGradeStale:
		return 2
	case FreshnessGradeCritical:
		return 3
	default:
		return 3
	}
}

func worseGrade(a, b FreshnessGrade) FreshnessGrade {
	if a.rank() >= b.rank() {
		return a
	}
	return b
}

// FreshnessScore 是单条知识的新鲜度评分结果（§7.9）。
type FreshnessScore struct {
	// Total 加权和（0-100；V1 可达上限 70，见 FreshnessV1MaxScore）。
	Total int `json:"total"`
	// Grade 档位：good / aging / stale / critical。
	Grade FreshnessGrade `json:"grade"`
	// TopSignals 按"丢分最多"排序的条目级可行动信号（最多 3 个）。
	// 平台级缺口（源系统状态、反馈在 V1 恒为 0）不列入：它们不是
	// 条目属性、对所有条目相同，列出来只会淹没真正的行动信号。
	TopSignals []string `json:"top_signals"`
}

// 新鲜度信号 token（条目级、可行动）。出现在 TopSignals 里表示该信号
// 丢分，token 值本身即修复方向。
const (
	// FreshnessSignalValidityExpired 显式有效期已过（读取时已按 expired 降级）。
	FreshnessSignalValidityExpired = "explicit_validity_expired"
	// FreshnessSignalReviewOverdue 距上次复审已超出 review_cycle_days。
	FreshnessSignalReviewOverdue = "review_cycle_overdue"
	// FreshnessSignalReviewNeverPerformed 从未有复审记录（last_reviewed_at 为空）。
	FreshnessSignalReviewNeverPerformed = "review_never_performed"
	// FreshnessSignalSingleVersion 仅发布过 v1，从未经治理流程刷新过内容。
	FreshnessSignalSingleVersion = "single_version_not_refreshed"
	// FreshnessSignalLifecycleConflict 生命周期处于 conflict。
	FreshnessSignalLifecycleConflict = "lifecycle_conflict"
	// FreshnessSignalLifecycleStale 生命周期处于 stale（存在过期证据）。
	FreshnessSignalLifecycleStale = "lifecycle_stale"
)

// FromKnowledge 计算一条知识在 now 时刻的新鲜度评分。纯函数：同一输入
// 恒得同一输出，便于单测。k 为 nil 时返回 critical 空分。
func FromKnowledge(k *Knowledge, now time.Time) FreshnessScore {
	if k == nil {
		return FreshnessScore{Total: 0, Grade: FreshnessGradeCritical, TopSignals: []string{}}
	}

	type signal struct {
		token string
		loss  float64 // weight * (1 - fraction)
	}

	expired := k.ExpiresAt != nil && now.After(*k.ExpiresAt)

	// 1) 显式有效期 30%：expires_at 被越过 → 0（与 ResolveLifecycle 的
	//    读取时降级同一判定：now.After，边界时刻不算过期）。
	validityFraction := 1.0
	if expired {
		validityFraction = 0.0
	}
	validityLoss := float64(FreshnessWeightValidity) * (1 - validityFraction)

	// 2) 源系统状态 20%：V1 无连接器，信号不可观测，恒为 0（见
	//    FreshnessWeightSource 注释）。不产生条目级 token。

	// 3) 版本当前性 15%：current_version > 1 → 经历过治理化更新，满分；
	//    否则按基础比例计分（发布过但从未刷新）。
	versionFraction := 1.0
	if k.CurrentVersion <= 1 {
		versionFraction = freshnessVersionBaseFactor
	}
	versionLoss := float64(FreshnessWeightVersion) * (1 - versionFraction)

	// 4) 复审周期 15%：复审周期未配置（<=0）视为无复审义务，满分。
	//    配置了周期时以 last_reviewed_at（缺省退回 created_at）为基线，
	//    一个周期内满分；超期后线性衰减，超期一倍（2x cycle）归零。
	reviewFraction := 1.0
	reviewOverdue := false
	if k.ReviewCycleDays > 0 {
		baseline := now
		if k.LastReviewedAt != nil {
			baseline = *k.LastReviewedAt
		} else {
			baseline = k.CreatedAt
		}
		cycle := time.Duration(k.ReviewCycleDays) * 24 * time.Hour
		if cycle > 0 {
			over := now.Sub(baseline) - cycle
			if over > 0 {
				reviewOverdue = true
				overRatio := over.Seconds() / cycle.Seconds()
				reviewFraction = math.Max(0, 1-overRatio)
			}
		}
	}
	reviewLoss := float64(FreshnessWeightReview) * (1 - reviewFraction)

	// 5) 冲突 10%：lifecycle 处于 conflict 或 stale 都意味着存在矛盾/
	//    过期证据，信号归零；其余状态满分。
	conflictFraction := 1.0
	status := ParseKnowledgeLifecycleStatus(k.LifecycleStatus)
	lifecycleConflict := status == KnowledgeLifecycleConflict
	lifecycleStale := status == KnowledgeLifecycleStale
	if lifecycleConflict || lifecycleStale {
		conflictFraction = 0.0
	}
	conflictLoss := float64(FreshnessWeightConflict) * (1 - conflictFraction)

	// 6) 反馈 10%：V1 无 quality_tasks 联动，恒为 0（见
	//    FreshnessWeightFeedback 注释）。不产生条目级 token。

	total := int(math.Round(float64(FreshnessWeightValidity)*validityFraction +
		float64(FreshnessWeightSource)*0 + // V1 无连接器
		float64(FreshnessWeightVersion)*versionFraction +
		float64(FreshnessWeightReview)*reviewFraction +
		float64(FreshnessWeightConflict)*conflictFraction +
		float64(FreshnessWeightFeedback)*0)) // V1 无反馈联动

	// 档位 + 覆盖规则：
	//   - 已过显式有效期 → critical（读取时已降级 expired，档位不能说 aging）
	//   - conflict/stale → 至多 stale（不可作权威答案的条目不得评 good）
	grade := FreshnessGradeGood
	switch {
	case total >= freshnessGoodThreshold:
		grade = FreshnessGradeGood
	case total >= freshnessAgingThreshold:
		grade = FreshnessGradeAging
	case total >= freshnessStaleThreshold:
		grade = FreshnessGradeStale
	default:
		grade = FreshnessGradeCritical
	}
	if expired {
		grade = FreshnessGradeCritical
	}
	if lifecycleConflict || lifecycleStale {
		grade = worseGrade(grade, FreshnessGradeStale)
	}

	// TopSignals：条目级可行动信号，按丢分降序取前 3。平台级缺口
	// （源系统状态/反馈）不列入，理由见字段注释。
	signals := []signal{
		{token: FreshnessSignalValidityExpired, loss: validityLoss},
		{token: FreshnessSignalReviewOverdue, loss: reviewLoss},
		{token: FreshnessSignalSingleVersion, loss: versionLoss},
		{token: FreshnessSignalLifecycleConflict, loss: conflictLoss},
	}
	if lifecycleStale && !lifecycleConflict {
		// stale 与 conflict 互斥出现，token 也要对症。
		signals[3].token = FreshnessSignalLifecycleStale
	}
	sort.SliceStable(signals, func(i, j int) bool { return signals[i].loss > signals[j].loss })
	topSignals := make([]string, 0, 3)
	for _, s := range signals {
		if len(topSignals) == 3 {
			break
		}
		if s.loss <= 0 {
			continue
		}
		topSignals = append(topSignals, s.token)
	}
	// 从未复审过是比"复审超期"更强的行动提示，单独列出（不占权重，
	// 只是 review 信号的细分）；TopSignals 总数仍封顶 3。
	if reviewOverdue && k.LastReviewedAt == nil && len(topSignals) < 3 {
		topSignals = append(topSignals, FreshnessSignalReviewNeverPerformed)
	}

	return FreshnessScore{
		Total:      total,
		Grade:      grade,
		TopSignals: topSignals,
	}
}

// FreshnessReviewQueueItem 是 GET /review/queue 的一行：待复审条目 +
// 当前新鲜度评分 + 入队原因（机器可读，供 UI 徽章）。
type FreshnessReviewQueueItem struct {
	Knowledge *Knowledge     `json:"knowledge"`
	Score     FreshnessScore `json:"score"`
	// Reasons 入队原因，取值：
	//   - "already_pending_review_cycle"（已处待复审状态）
	//   - "expires_at_reached"（显式有效期已过，显式落库尚未执行）
	//   - "review_cycle_due"（距上次复审超出周期）
	//   - "review_never_performed"（从未复审过，review_cycle_due 的细分）
	Reasons []string `json:"reasons"`
}

// FreshnessSweepResult 是一次复审调度清扫的汇总。
type FreshnessSweepResult struct {
	// ReviewFlagged 本次被置为 pending_review_cycle 的条数。
	ReviewFlagged int `json:"review_flagged"`
	// ExpiredLanded 本次被显式落库为 expired 的条数。
	ExpiredLanded int `json:"expired_landed"`
	// Failures 单条转换失败数（失败容错继续，不计入终止条件）。
	Failures int `json:"failures"`
	// StartedAt / Duration 清扫起止与耗时。
	StartedAt time.Time     `json:"started_at"`
	Duration  time.Duration `json:"duration"`
}
