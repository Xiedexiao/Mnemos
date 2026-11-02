package types

import (
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// SearchChannel names where a query came from, so quality metrics can be split
// by surface (KnowAxis V3.0 §12.5).
type SearchChannel string

const (
	// SearchChannelKnowledgeQA is the workspace Q&A retrieval path.
	SearchChannelKnowledgeQA SearchChannel = "knowledge_qa"
	// SearchChannelHybrid is a direct hybrid search request.
	SearchChannelHybrid SearchChannel = "hybrid"
	// SearchChannelSymbol is the exact-number/symbol lane.
	SearchChannelSymbol SearchChannel = "symbol"
	// SearchChannelGraph is a graph traversal query.
	SearchChannelGraph SearchChannel = "graph"
)

// SearchLog is one executed query (§12.5 查询日志).
//
// It records the authorization-filtered count alongside the visible result
// count. That number is an aggregate for operations only — §2.10 forbids
// telling the reader how much they cannot see, so it must never reach the
// user-facing response.
type SearchLog struct {
	ID       string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID uint64 `json:"tenant_id" gorm:"not null;index:idx_search_logs_tenant_time,priority:1"`
	UserID   string `json:"user_id" gorm:"type:varchar(36);not null;default:''"`
	Query    string `json:"query" gorm:"type:varchar(512);not null"`
	// QueryNormalized is the lowercased, whitespace-collapsed query used to
	// cluster zero-result terms without case/space noise.
	QueryNormalized string `json:"query_normalized" gorm:"type:varchar(512);not null;default:''"`
	Channel         string `json:"channel" gorm:"type:varchar(32);not null;default:'hybrid'"`
	// KnowledgeBaseIDs scopes the query (JSON array of ids).
	KnowledgeBaseIDs JSON `json:"knowledge_base_ids" gorm:"type:json;not null;default:'[]'"`
	// ResultCount is the number of results the caller actually saw.
	ResultCount int `json:"result_count" gorm:"not null;default:0"`
	// PermissionFilteredCount is how many candidates were dropped by ACL,
	// classification or lifecycle before the result was built.
	PermissionFilteredCount int `json:"permission_filtered_count" gorm:"not null;default:0"`
	// TopScore is the best score returned, for "results exist but irrelevant".
	TopScore float64 `json:"top_score" gorm:"not null;default:0"`
	// DurationMs is the end-to-end retrieval time.
	DurationMs int  `json:"duration_ms" gorm:"not null;default:0"`
	Clicked    bool `json:"clicked" gorm:"not null;default:false"`
	// CreatedAt is indexed descending for the dashboard windows.
	CreatedAt time.Time `json:"created_at" gorm:"index:idx_search_logs_tenant_time,priority:2"`
}

// TableName pins the GORM table name.
func (SearchLog) TableName() string { return "search_logs" }

// BeforeCreate assigns an ID before persistence.
func (l *SearchLog) BeforeCreate(_ *gorm.DB) error {
	if l.ID == "" {
		l.ID = uuid.NewString()
	}
	if l.QueryNormalized == "" {
		l.QueryNormalized = NormalizeSearchQuery(l.Query)
	}
	return nil
}

// searchQueryMaxRunes bounds the stored query so a pathological paste cannot
// bloat the log table.
const searchQueryMaxRunes = 512

// NormalizeSearchQuery lowercases, collapses whitespace and truncates a query
// so "  STACK-01  报警 " and "stack-01 报警" cluster together in zero-result
// analysis. It is deliberately conservative: punctuation is kept because error
// codes and part numbers depend on it.
func NormalizeSearchQuery(query string) string {
	collapsed := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	if runes := []rune(collapsed); len(runes) > searchQueryMaxRunes {
		return string(runes[:searchQueryMaxRunes])
	}
	return collapsed
}

// TruncateSearchQuery bounds a raw query for storage.
func TruncateSearchQuery(query string) string {
	if runes := []rune(query); len(runes) > searchQueryMaxRunes {
		return string(runes[:searchQueryMaxRunes])
	}
	return query
}

// KnowledgeGapStatus is the lifecycle of a knowledge gap (§14.4).
type KnowledgeGapStatus string

const (
	// KnowledgeGapOpen needs an owner decision.
	KnowledgeGapOpen KnowledgeGapStatus = "open"
	// KnowledgeGapAcknowledged an owner accepted it and is working on it.
	KnowledgeGapAcknowledged KnowledgeGapStatus = "acknowledged"
	// KnowledgeGapClosed resolved (knowledge added or gap dismissed).
	KnowledgeGapClosed KnowledgeGapStatus = "closed"
)

// Valid reports whether the status is known.
func (s KnowledgeGapStatus) Valid() bool {
	switch s {
	case KnowledgeGapOpen, KnowledgeGapAcknowledged, KnowledgeGapClosed:
		return true
	default:
		return false
	}
}

// KnowledgeGapSource names why a gap was recorded.
type KnowledgeGapSource string

const (
	// KnowledgeGapSourceSearchZeroResult a frequent search returned nothing.
	KnowledgeGapSourceSearchZeroResult KnowledgeGapSource = "search_zero_result"
	// KnowledgeGapSourceQARefusal the answer path refused (§13.5).
	KnowledgeGapSourceQARefusal KnowledgeGapSource = "qa_refusal"
	// KnowledgeGapSourceLowSatisfaction repeated negative feedback.
	KnowledgeGapSourceLowSatisfaction KnowledgeGapSource = "low_satisfaction"
	// KnowledgeGapSourceManual created by an operator.
	KnowledgeGapSourceManual KnowledgeGapSource = "manual"
)

// KnowledgeGap is one clustered missing-knowledge signal (§14.4).
//
// Rows are aggregated by normalized query: the operator works one row per
// question instead of one row per search, which is what makes the gap list
// actionable rather than a log dump.
type KnowledgeGap struct {
	ID              string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID        uint64 `json:"tenant_id" gorm:"not null;uniqueIndex:uq_knowledge_gaps_tenant_query,priority:1"`
	QueryNormalized string `json:"query_normalized" gorm:"type:varchar(512);not null;uniqueIndex:uq_knowledge_gaps_tenant_query,priority:2"`
	Query           string `json:"query" gorm:"type:varchar(512);not null"`
	Source          string `json:"source" gorm:"type:varchar(32);not null;default:'search_zero_result'"`
	// Occurrences counts how often this question came up.
	Occurrences int       `json:"occurrences" gorm:"not null;default:1"`
	LastSeenAt  time.Time `json:"last_seen_at"`
	Status      string    `json:"status" gorm:"type:varchar(16);not null;default:'open'"`
	// AssigneeID is the owner working the gap.
	AssigneeID string `json:"assignee_id" gorm:"type:varchar(36);not null;default:''"`
	// Suggestion carries the recommended template/expert note.
	Suggestion string `json:"suggestion" gorm:"type:varchar(255);not null;default:''"`
	// ResolvedKnowledgeID links the knowledge that closed the gap.
	ResolvedKnowledgeID string    `json:"resolved_knowledge_id" gorm:"type:varchar(36);not null;default:''"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// TableName pins the GORM table name.
func (KnowledgeGap) TableName() string { return "knowledge_gaps" }

// BeforeCreate assigns an ID before persistence.
func (g *KnowledgeGap) BeforeCreate(_ *gorm.DB) error {
	if g.ID == "" {
		g.ID = uuid.NewString()
	}
	return nil
}

// SearchQualityOverview is the §12.5 dashboard read model.
type SearchQualityOverview struct {
	// TotalQueries in the window.
	TotalQueries int64 `json:"total_queries"`
	// ZeroResultQueries in the window.
	ZeroResultQueries int64 `json:"zero_result_queries"`
	// ZeroResultRate is ZeroResultQueries / TotalQueries.
	ZeroResultRate float64 `json:"zero_result_rate"`
	// PermissionFilteredTotal is how many candidates were dropped by policy.
	PermissionFilteredTotal int64 `json:"permission_filtered_total"`
	// ClickedQueries is how many queries led to a click.
	ClickedQueries int64 `json:"clicked_queries"`
	// AverageDurationMs is the mean retrieval time.
	AverageDurationMs float64 `json:"average_duration_ms"`
	// WindowDays is the look-back window these numbers cover.
	WindowDays int `json:"window_days"`
}

// FrequentQuery is one row of the hot/zero-result lists.
type FrequentQuery struct {
	Query       string `json:"query"`
	Occurrences int64  `json:"occurrences"`
}
