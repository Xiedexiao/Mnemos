package types

import (
	"encoding/json"
	"math"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ============================================================================
// 知识健康度看板（KnowAxis V3.0 §14.6 / §16.1 v1）。
//
// knowledge_health_snapshots 是只读看板的落库面：每次计算追加一行快照，
// 看板读最新一行，历史留档供趋势回看。v1 指标全部来自 knowledges 表可得
// 的治理信号（生命周期/补全/结构/治理），usage 维度待浏览统计接入后补充。
// ============================================================================

// KnowledgeHealthScope 快照的统计口径。
type KnowledgeHealthScope string

const (
	// KnowledgeHealthScopeTenant 全租户聚合（scope_id 为空串）。
	KnowledgeHealthScopeTenant KnowledgeHealthScope = "tenant"
	// KnowledgeHealthScopeKnowledgeBase 单知识库聚合（scope_id = KB ID）。
	KnowledgeHealthScopeKnowledgeBase KnowledgeHealthScope = "knowledge_base"
)

// Valid 报告口径是否已知。
func (s KnowledgeHealthScope) Valid() bool {
	switch s {
	case KnowledgeHealthScopeTenant, KnowledgeHealthScopeKnowledgeBase:
		return true
	default:
		return false
	}
}

// String 渲染口径用于日志。
func (s KnowledgeHealthScope) String() string { return string(s) }

// KnowledgeHealthSnapshot is one persisted computation of the knowledge
// health dashboard. Snapshots are append-only: each Compute writes a fresh
// row, the dashboard reads the latest, history stays for trend review.
type KnowledgeHealthSnapshot struct {
	// ID of the snapshot.
	ID string `json:"id" gorm:"type:varchar(36);primaryKey"`
	// TenantID owning the snapshot.
	TenantID uint64 `json:"tenant_id" gorm:"not null"`
	// Scope of the aggregation: tenant | knowledge_base.
	Scope string `json:"scope" gorm:"type:varchar(16);not null"`
	// ScopeID is the KB id for knowledge_base scope; empty for tenant scope.
	ScopeID string `json:"scope_id" gorm:"type:varchar(36);not null;default:''"`
	// Metrics is the JSONB payload of KnowledgeHealthMetrics (§14.6 六维度).
	Metrics JSON `json:"metrics" gorm:"type:jsonb;not null;default:'{}'"`
	// ComputedAt is when the metrics were computed.
	ComputedAt time.Time `json:"computed_at"`
}

// TableName pins the GORM table name.
func (KnowledgeHealthSnapshot) TableName() string { return "knowledge_health_snapshots" }

// BeforeCreate assigns an ID before persistence.
func (s *KnowledgeHealthSnapshot) BeforeCreate(_ *gorm.DB) error {
	if s.ID == "" {
		s.ID = uuid.NewString()
	}
	return nil
}

// ScopeType returns the typed scope of the snapshot.
func (s *KnowledgeHealthSnapshot) ScopeType() KnowledgeHealthScope {
	return KnowledgeHealthScope(s.Scope)
}

// DecodeMetrics unmarshals the stored JSONB payload into the typed metrics.
// An empty payload decodes to the zero-valued metrics instead of failing, so
// a degraded snapshot still renders an (empty) dashboard.
func (s *KnowledgeHealthSnapshot) DecodeMetrics() (*KnowledgeHealthMetrics, error) {
	metrics := &KnowledgeHealthMetrics{}
	if len(s.Metrics) == 0 {
		return metrics, nil
	}
	if err := json.Unmarshal(s.Metrics, metrics); err != nil {
		return nil, err
	}
	return metrics, nil
}

// KnowledgeHealthRawStats 是仓储层从 knowledges 表采集的原始计数。
// 占比（rate）与健康分由 service 层派生；仓储只做计数，保持各层可测。
type KnowledgeHealthRawStats struct {
	// Total 参与统计的未删除知识总数（单 KB 或全租户口径）。
	Total int64 `json:"total"`
	// LifecycleCounts 按 lifecycle_status 分组的计数（键为状态字符串）。
	LifecycleCounts map[string]int64 `json:"lifecycle_counts"`
	// WithCustomMetadata custom_metadata 非空（非 NULL/{}/[]/null）的条数。
	WithCustomMetadata int64 `json:"with_custom_metadata"`
	// WithDescription description 去空白后非空的条数。
	WithDescription int64 `json:"with_description"`
	// DuplicateFileCount 处于"同 KB 同 file_hash（非空）且组内 >1 条"组中的
	// 知识条数。
	DuplicateFileCount int64 `json:"duplicate_file_count"`
	// DuplicateGroupCount 上述重复组的组数。
	DuplicateGroupCount int64 `json:"duplicate_group_count"`
	// NoReviewCycle 未配置复审周期（review_cycle_days=0）的条数。
	NoReviewCycle int64 `json:"no_review_cycle"`
	// BeyondInternal classification 不同于 internal（含空串默认值）的条数：
	// public（可见面更宽）与 secret/confidential（更敏感）都是治理关注对象。
	BeyondInternal int64 `json:"beyond_internal"`
	// SoftDeleted 软删存量（deleted_at 非空的行数，等待清理决策）。
	SoftDeleted int64 `json:"soft_deleted"`
}

// KnowledgeFreshnessMetrics 新鲜度维度（§14.6）：生命周期状态分布 + 命名占比。
type KnowledgeFreshnessMetrics struct {
	// Counts 各 lifecycle_status 的计数（含未单列 rate 的状态，如 draft）。
	Counts map[string]int64 `json:"counts"`
	// ValidRate valid 占比。
	ValidRate float64 `json:"valid_rate"`
	// PendingReviewRate pending_review 占比。
	PendingReviewRate float64 `json:"pending_review_rate"`
	// ExpiredRate expired 占比。
	ExpiredRate float64 `json:"expired_rate"`
	// StaleRate stale 占比。
	StaleRate float64 `json:"stale_rate"`
}

// KnowledgeCompletenessMetrics 补全维度：结构化补全率的两个 SQL 信号。
type KnowledgeCompletenessMetrics struct {
	// CustomMetadataRate custom_metadata 非空占比。
	CustomMetadataRate float64 `json:"custom_metadata_rate"`
	// DescriptionRate description 非空占比。
	DescriptionRate float64 `json:"description_rate"`
}

// CompletenessScore 归一 [0,1]：两项补全率的均值，作为健康分输入。
func (m KnowledgeCompletenessMetrics) CompletenessScore() float64 {
	return (clampUnit(m.CustomMetadataRate) + clampUnit(m.DescriptionRate)) / 2
}

// KnowledgeUsageMetrics 使用度维度。
//
// TODO(KnowAxis V3.0 §14.6): v1 从 knowledges 表可得的信号有限，浏览/引用/
// 搜索命中统计尚未接入看板信号源；接入后在此补充 view_30d / citation_30d /
// search_hit_30d 等字段（当前结构体占位，序列化为 {}）。
type KnowledgeUsageMetrics struct{}

// KnowledgeStructureMetrics 结构维度：同 KB 重复文件信号。
type KnowledgeStructureMetrics struct {
	// DuplicateFileCount 处于重复组内的知识条数。
	DuplicateFileCount int64 `json:"duplicate_file_count"`
	// DuplicateGroupCount 重复组数（同 KB 内 file_hash 相同且 >1 条的组）。
	DuplicateGroupCount int64 `json:"duplicate_group_count"`
	// DuplicateRate 重复条数 / 总数。
	DuplicateRate float64 `json:"duplicate_rate"`
}

// KnowledgeGovernanceMetrics 治理维度。
type KnowledgeGovernanceMetrics struct {
	// NoReviewCycleRate 未配置复审周期（review_cycle_days=0）占比。
	NoReviewCycleRate float64 `json:"no_review_cycle_rate"`
	// BeyondInternalRate classification 不同于 internal（更宽或更严）占比。
	BeyondInternalRate float64 `json:"beyond_internal_rate"`
	// SoftDeletedCount 软删存量（等待清理决策的行数）。
	SoftDeletedCount int64 `json:"soft_deleted_count"`
}

// GovernanceCoverage 归一 [0,1]：1 - 无复审周期占比，作为健康分输入。
// 软删存量与 beyond_internal 是观察信号，不进 v1 得分。
func (m KnowledgeGovernanceMetrics) GovernanceCoverage() float64 {
	return clampUnit(1 - m.NoReviewCycleRate)
}

// KnowledgeHealthMetrics 看板一次计算的全部指标（§14.6 六维度 v1）。
type KnowledgeHealthMetrics struct {
	// Total 总知识数（与 KnowledgeHealthRawStats.Total 一致，前端单包读取）。
	Total int64 `json:"total"`
	// Freshness 新鲜度。
	Freshness KnowledgeFreshnessMetrics `json:"freshness"`
	// Completeness 补全度。
	Completeness KnowledgeCompletenessMetrics `json:"completeness"`
	// Usage 使用度（v1 占位，见 KnowledgeUsageMetrics 的 TODO）。
	Usage KnowledgeUsageMetrics `json:"usage"`
	// Structure 结构。
	Structure KnowledgeStructureMetrics `json:"structure"`
	// Governance 治理。
	Governance KnowledgeGovernanceMetrics `json:"governance"`
	// Score 0-100 健康分（ComputeKnowledgeHealthScore 的结果）。
	Score float64 `json:"score"`
}

// 健康分 v1 权重：新鲜度 40 + 补全 30 + 治理 30（权重和恰为 100，结果天然
// 落在 0-100）。
const (
	weightFreshnessValid = 40.0
	weightCompleteness   = 30.0
	weightGovernance     = 30.0
)

// clampUnit 把比率裁剪到 [0,1]，防御越界输入污染健康分。
func clampUnit(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// ComputeKnowledgeHealthScore v1 加权健康分（纯函数，可测）：
//
//	valid_rate*40 + completeness*30 + governance*30，归一到 0-100。
//
// 三个输入都应已归一到 [0,1]（越界会被裁剪）：valid_rate 取新鲜度 valid
// 占比，completeness 取补全均值，governance 取复审覆盖率。结果保留两位
// 小数。total=0 时各比率为 0 → 得 0 分，不做空库特殊分，由看板以
// total=0 解释为"暂无知识"。
func ComputeKnowledgeHealthScore(validRate, completenessRate, governanceRate float64) float64 {
	score := clampUnit(validRate)*weightFreshnessValid +
		clampUnit(completenessRate)*weightCompleteness +
		clampUnit(governanceRate)*weightGovernance
	return math.Round(score*100) / 100
}
