package types

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ============================================================================
// 知识之星评选（KnowAxis V3.0 §15.4）。
//
// §15.4 要求"每月/每季度评选知识之星并公开表彰"。平台此前只有积分流水与
// 等级：能算出谁分高，却没有"某期获奖"这个可公开、可复核的记录。评选是
// 一次治理动作，不是排行榜的别名——排行榜每天在变，获奖名单是定稿。
//
// 口径：候选分数 = 该周期内积分流水之和（正分；兑换扣分发生在周期外也
// 不影响历史评选）。管理员在候选名单里选人，排名按分数降序自动生成。
// ============================================================================

// KnowledgeStarPeriodType is the award cadence.
type KnowledgeStarPeriodType string

const (
	// KnowledgeStarPeriodMonth is "YYYY-MM".
	KnowledgeStarPeriodMonth KnowledgeStarPeriodType = "month"
	// KnowledgeStarPeriodQuarter is "YYYY-Q1".."YYYY-Q4".
	KnowledgeStarPeriodQuarter KnowledgeStarPeriodType = "quarter"
)

// Valid reports whether the cadence is known.
func (p KnowledgeStarPeriodType) Valid() bool {
	switch p {
	case KnowledgeStarPeriodMonth, KnowledgeStarPeriodQuarter:
		return true
	default:
		return false
	}
}

var (
	knowledgeStarMonthPattern   = regexp.MustCompile(`^(\d{4})-(\d{1,2})$`)
	knowledgeStarQuarterPattern = regexp.MustCompile(`^(\d{4})-[Qq]([1-4])$`)
)

// ParseKnowledgeStarPeriod validates a period string and returns its UTC
// half-open range [start, end) plus the canonical spelling.
//
// 时间范围按 UTC 计算：积分流水的时间戳统一是 UTC，按服务器本地时区切
// 月份会让跨时区租户的边界各差几小时。
func ParseKnowledgeStarPeriod(
	periodType KnowledgeStarPeriodType, raw string,
) (start, end time.Time, canonical string, err error) {
	raw = strings.TrimSpace(raw)
	if !periodType.Valid() {
		return time.Time{}, time.Time{}, "", fmt.Errorf("unknown period type %q", periodType)
	}
	switch periodType {
	case KnowledgeStarPeriodMonth:
		match := knowledgeStarMonthPattern.FindStringSubmatch(raw)
		if match == nil {
			return time.Time{}, time.Time{}, "", fmt.Errorf("period must look like YYYY-MM")
		}
		year, _ := strconv.Atoi(match[1])
		month, _ := strconv.Atoi(match[2])
		if month < 1 || month > 12 {
			return time.Time{}, time.Time{}, "", fmt.Errorf("month must be 01..12")
		}
		start = time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
		end = start.AddDate(0, 1, 0)
		canonical = fmt.Sprintf("%04d-%02d", year, month)
		return start, end, canonical, nil
	case KnowledgeStarPeriodQuarter:
		match := knowledgeStarQuarterPattern.FindStringSubmatch(raw)
		if match == nil {
			return time.Time{}, time.Time{}, "", fmt.Errorf("period must look like YYYY-Q1..YYYY-Q4")
		}
		year, _ := strconv.Atoi(match[1])
		quarter, _ := strconv.Atoi(match[2])
		startMonth := time.Month((quarter-1)*3 + 1)
		start = time.Date(year, startMonth, 1, 0, 0, 0, 0, time.UTC)
		end = start.AddDate(0, 3, 0)
		canonical = fmt.Sprintf("%04d-Q%d", year, quarter)
		return start, end, canonical, nil
	}
	return time.Time{}, time.Time{}, "", fmt.Errorf("unknown period type %q", periodType)
}

// KnowledgeStarPeriodOf returns the canonical period containing now.
func KnowledgeStarPeriodOf(periodType KnowledgeStarPeriodType, now time.Time) string {
	now = now.UTC()
	switch periodType {
	case KnowledgeStarPeriodQuarter:
		quarter := int(now.Month()-1)/3 + 1
		return fmt.Sprintf("%04d-Q%d", now.Year(), quarter)
	default:
		return fmt.Sprintf("%04d-%02d", now.Year(), int(now.Month()))
	}
}

// KnowledgeStarAward is one finalised winner row.
type KnowledgeStarAward struct {
	ID       string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID uint64 `json:"tenant_id" gorm:"not null;uniqueIndex:uq_knowledge_star_period_user,priority:1"`
	// PeriodType is month/quarter.
	PeriodType string `json:"period_type" gorm:"type:varchar(16);not null;uniqueIndex:uq_knowledge_star_period_user,priority:2"`
	// Period is the canonical period string.
	Period string `json:"period" gorm:"type:varchar(16);not null;uniqueIndex:uq_knowledge_star_period_user,priority:3"`
	// UserID is the winner.
	UserID string `json:"user_id" gorm:"type:varchar(36);not null;uniqueIndex:uq_knowledge_star_period_user,priority:4"`
	// Rank is 1-based within the period (score descending).
	Rank int `json:"rank" gorm:"not null;default:0"`
	// Score is the points the user earned in the period at selection time.
	Score int `json:"score" gorm:"not null;default:0"`
	// Reason is the operator's citation, shown on the public board.
	Reason string `json:"reason" gorm:"type:varchar(512);not null;default:''"`
	// Published makes the award visible to every reader (§15.4 公开表彰).
	Published bool `json:"published" gorm:"not null;default:false"`
	// AwardedBy is the operator who finalised the list.
	AwardedBy string    `json:"awarded_by" gorm:"type:varchar(36);not null;default:''"`
	AwardedAt time.Time `json:"awarded_at"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName pins the GORM table name.
func (KnowledgeStarAward) TableName() string { return "knowledge_star_awards" }

// BeforeCreate assigns an ID before persistence.
func (a *KnowledgeStarAward) BeforeCreate(_ *gorm.DB) error {
	if a.ID == "" {
		a.ID = uuid.NewString()
	}
	if a.AwardedAt.IsZero() {
		a.AwardedAt = time.Now().UTC()
	}
	return nil
}

// KnowledgeStarCandidate is one ranked contributor for a period.
type KnowledgeStarCandidate struct {
	// UserID is the contributor.
	UserID string `json:"user_id"`
	// Score is the points earned inside the period.
	Score int `json:"score"`
	// KnowledgeCreated / ReviewsDone / AnswersAdopted explain the score.
	KnowledgeCreated int `json:"knowledge_created"`
	ReviewsDone      int `json:"reviews_done"`
	AnswersAdopted   int `json:"answers_adopted"`
	// Balance and Level are the current overall standing (context for the
	// operator, not part of the period score).
	Balance int    `json:"balance"`
	Level   string `json:"level"`
	// AlreadyAwarded is true when the user already holds an award for the
	// period.
	AlreadyAwarded bool `json:"already_awarded"`
}

// KnowledgeStarSelection is the operator's final list for one period.
type KnowledgeStarSelection struct {
	// PeriodType is month/quarter.
	PeriodType KnowledgeStarPeriodType `json:"period_type"`
	// Period is the canonical period string (YYYY-MM or YYYY-Qn).
	Period string `json:"period"`
	// UserIDs are the winners; rank is derived from each user's period score.
	UserIDs []string `json:"user_ids"`
	// Reason is the citation applied to every winner unless a per-user reason
	// is supplied later.
	Reason string `json:"reason"`
}
