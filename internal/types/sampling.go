package types

import (
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ============================================================================
// 抽检（KnowAxis V3.0 §6.6 "抽检与质量：100% / 10% / 2%"）。
//
// 平台此前没有抽检：审批通过即视为质量合格，没有任何"事后复核已发布内容"
// 的机制。抽检与审批的区别是刻意的——审批看的是"能不能发"，抽检看的是
// "发出去之后是不是真的对"，两者不能互相替代。
//
// 三个决定：
//  1. 抽检比例由**风险等级**决定，不由人工挑：高风险的每一篇都抽，中风险
//     10%，低风险 2%。人工挑出来的样本永远偏向"看起来可疑的"，而抽检的
//     价值恰恰在于随机发现"看起来没问题"的问题。
//  2. 选样是确定性的（按 (knowledgeID, 周期) 哈希取模）：重跑同一个周期
//     得到同一批样本，复盘时能重现；换成随机数则每次都不一样，"这批样本
//     是谁选的"就无从考证。
//  3. 抽检结论独立存储，不写回审批任务：审批回执是不可变凭证，事后改它的
//     结论会让"当初批的是什么"失去意义。
// ============================================================================

// SamplingRatio is the inspection rate for one risk level.
type SamplingRatio int

const (
	// SamplingRatioFull 100%：高风险内容全部抽检。
	SamplingRatioFull SamplingRatio = 100
	// SamplingRatioTen 10%：中风险内容。
	SamplingRatioTen SamplingRatio = 10
	// SamplingRatioTwo 2%：低风险内容。
	SamplingRatioTwo SamplingRatio = 2
)

// SamplingRatioForRisk maps a risk level onto its inspection rate (§6.6).
//
// 未知/空风险按中风险处理：把一篇没标风险的文档当成低风险只抽 2%，等于
// 默认放过它。
func SamplingRatioForRisk(risk string) SamplingRatio {
	switch ApprovalRiskLevel(strings.ToLower(strings.TrimSpace(risk))) {
	case ApprovalRiskHigh:
		return SamplingRatioFull
	case ApprovalRiskLow:
		return SamplingRatioTwo
	default:
		return SamplingRatioTen
	}
}

// SamplingDecisionStatus is the outcome of one inspection.
type SamplingDecisionStatus string

const (
	// SamplingPending 待抽检。
	SamplingPending SamplingDecisionStatus = "pending"
	// SamplingPassed 抽检通过。
	SamplingPassed SamplingDecisionStatus = "passed"
	// SamplingFailed 抽检不通过（需要复盘与纠错）。
	SamplingFailed SamplingDecisionStatus = "failed"
	// SamplingWaived 本期豁免（有理由）。
	SamplingWaived SamplingDecisionStatus = "waived"
)

// Valid reports whether the status is known.
func (s SamplingDecisionStatus) Valid() bool {
	switch s {
	case SamplingPending, SamplingPassed, SamplingFailed, SamplingWaived:
		return true
	default:
		return false
	}
}

// SamplingInspection is one sampled document awaiting review.
type SamplingInspection struct {
	ID              string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID        uint64 `json:"tenant_id" gorm:"not null;uniqueIndex:uq_sampling_period_doc,priority:1"`
	KnowledgeID     string `json:"knowledge_id" gorm:"type:varchar(36);not null;uniqueIndex:uq_sampling_period_doc,priority:2"`
	KnowledgeBaseID string `json:"knowledge_base_id" gorm:"type:varchar(36);not null;default:''"`
	// Period is the sampling window label (e.g. "2026-11").
	Period string `json:"period" gorm:"type:varchar(16);not null;uniqueIndex:uq_sampling_period_doc,priority:3"`
	// RiskLevel at sampling time.
	RiskLevel string `json:"risk_level" gorm:"type:varchar(16);not null;default:'mid'"`
	// Ratio applied when this row was drawn.
	Ratio int `json:"ratio" gorm:"not null;default:10"`
	// Title is a display label.
	Title string `json:"title" gorm:"type:varchar(255);not null;default:''"`
	// Status is the inspection outcome.
	Status string `json:"status" gorm:"type:varchar(16);not null;default:'pending'"`
	// ReviewedBy / ReviewedAt record who checked it.
	ReviewedBy string     `json:"reviewed_by" gorm:"type:varchar(36);not null;default:''"`
	ReviewedAt *time.Time `json:"reviewed_at"`
	// Note carries the reviewer's finding.
	Note string `json:"note" gorm:"type:varchar(1024);not null;default:''"`
	// FollowUpTaskID links the corrective task when the inspection failed.
	FollowUpTaskID string    `json:"follow_up_task_id" gorm:"type:varchar(36);not null;default:''"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// TableName pins the GORM table name.
func (SamplingInspection) TableName() string { return "sampling_inspections" }

// BeforeCreate assigns an ID before persistence.
func (i *SamplingInspection) BeforeCreate(_ *gorm.DB) error {
	if i.ID == "" {
		i.ID = uuid.NewString()
	}
	return nil
}

// ShouldSample decides deterministically whether one document is drawn.
//
// 用 FNV-1a 而不是 math/rand：哈希是纯函数，同一 (文档, 周期) 永远得到同一
// 结果。这让"这次抽到的样本和上次不一样"不会成为争议点——要么两次都抽中，
// 要么两次都不抽中。FNV 的分布对取模足够均匀，且比 SHA 快得多（抽检可能
// 一次扫过几千篇）。
func ShouldSample(knowledgeID, period string, ratio SamplingRatio) bool {
	if ratio >= SamplingRatioFull {
		return true
	}
	if ratio <= 0 {
		return false
	}
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(knowledgeID))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(period))
	return int(hash.Sum32()%100) < int(ratio)
}

// SamplingPeriod labels the current sampling window.
func SamplingPeriod(now time.Time) string {
	return now.UTC().Format("2006-01")
}

// SamplingRunResult reports one sampling pass.
type SamplingRunResult struct {
	// Period the pass covered.
	Period string `json:"period"`
	// Candidates scanned.
	Candidates int `json:"candidates"`
	// Sampled is how many inspections were created (or already existed).
	Sampled int `json:"sampled"`
	// ByRatio breaks the sample down per rate.
	ByRatio map[int]int `json:"by_ratio"`
	// SkippedExisting counts documents already drawn for this period.
	SkippedExisting int       `json:"skipped_existing"`
	RanAt           time.Time `json:"ran_at"`
}

// SamplingStats aggregates inspection outcomes (§6.6 审核质量指标).
type SamplingStats struct {
	// Period covered; empty means all.
	Period string `json:"period"`
	// Total inspections in scope.
	Total int64 `json:"total"`
	// Pending / Passed / Failed / Waived counts.
	Pending int64 `json:"pending"`
	Passed  int64 `json:"passed"`
	Failed  int64 `json:"failed"`
	Waived  int64 `json:"waived"`
	// PassRate is passed / (passed + failed). Waived and pending are excluded:
	// counting them either inflates or deflates the rate without measuring
	// anything.
	PassRate float64 `json:"pass_rate"`
	// ByRisk breaks the counts down per risk level.
	ByRisk map[string]SamplingStatsBreakdown `json:"by_risk"`
}

// SamplingStatsBreakdown is the per-risk-level slice.
type SamplingStatsBreakdown struct {
	Total    int64   `json:"total"`
	Passed   int64   `json:"passed"`
	Failed   int64   `json:"failed"`
	PassRate float64 `json:"pass_rate"`
}

// SamplingDecisionInput is the reviewer's verdict.
type SamplingDecisionInput struct {
	// InspectionID is the row under review.
	InspectionID string `json:"inspection_id"`
	// Status must be passed / failed / waived.
	Status string `json:"status"`
	// Note explains the finding; required for failed and waived.
	Note string `json:"note"`
}

// ValidateSamplingDecision guards the verdict shape.
//
// failed 与 waived 必须写理由：抽检的意义是"发现问题并留下记录"，一个没有
// 说明的"不通过"事后无法复盘，一个没有理由的"豁免"等于默认放过。
func ValidateSamplingDecision(input SamplingDecisionInput) error {
	status := SamplingDecisionStatus(strings.TrimSpace(input.Status))
	switch status {
	case SamplingPassed:
		return nil
	case SamplingFailed, SamplingWaived:
		if strings.TrimSpace(input.Note) == "" {
			return fmt.Errorf("%s requires a note explaining the finding", status)
		}
		return nil
	default:
		return fmt.Errorf("status must be passed, failed or waived")
	}
}

// SamplingCandidate is one publishable document considered for sampling.
type SamplingCandidate struct {
	KnowledgeID     string
	KnowledgeBaseID string
	Title           string
	RiskLevel       string
}
