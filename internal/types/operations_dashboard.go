package types

// ============================================================================
// 运营看板（KnowAxis V3.0 §16.1）。
//
// 平台此前没有任何"全局视图"：健康分要手工算、搜索质量要单独查、审核积压
// 要另开页面。§16.1 的看板回答"知识资产有多少、谁在用、哪些过期、哪些没人
// 管"，本类型就是这一次聚合的输出。
// ============================================================================

// DashboardOverview is the "整体概览" block.
type DashboardOverview struct {
	// KnowledgeTotal is the number of live entries.
	KnowledgeTotal int64 `json:"knowledge_total"`
	// KnowledgeByStatus breaks entries down by lifecycle state.
	KnowledgeByStatus map[string]int64 `json:"knowledge_by_status"`
	// KnowledgeBaseCount is how many spaces exist.
	KnowledgeBaseCount int64 `json:"knowledge_base_count"`
	// ChunkTotal is the total indexed chunk count.
	ChunkTotal int64 `json:"chunk_total"`
}

// DashboardGovernance is the "治理" block: what needs someone's attention.
type DashboardGovernance struct {
	// PendingReviews is the open approval queue.
	PendingReviews int64 `json:"pending_reviews"`
	// PendingReviewCycles is entries due for their periodic review.
	PendingReviewCycles int64 `json:"pending_review_cycles"`
	// Expired is entries past their validity.
	Expired int64 `json:"expired"`
	// Conflicts is entries flagged as contradicting another authority.
	Conflicts int64 `json:"conflicts"`
	// OpenQualityTasks is unresolved quality work.
	OpenQualityTasks int64 `json:"open_quality_tasks"`
	// OpenImpactTasks is unresolved change-request impact work.
	OpenImpactTasks int64 `json:"open_impact_tasks"`
	// OpenKnowledgeGaps is the missing-knowledge list size.
	OpenKnowledgeGaps int64 `json:"open_knowledge_gaps"`
	// PendingFieldMigrations is structural field work awaiting an operator.
	PendingFieldMigrations int64 `json:"pending_field_migrations"`
	// ACLCeilingViolations is child grants that exceeded their parent.
	ACLCeilingViolations int64 `json:"acl_ceiling_violations"`
	// PendingChangeSignals is the untriaged §7.3 signal queue.
	PendingChangeSignals int64 `json:"pending_change_signals"`
}

// DashboardDelivery is the "审核效率" block (§19.2 治理验收指标).
type DashboardDelivery struct {
	// ApprovedLast30Days counts approved tasks in the window.
	ApprovedLast30Days int64 `json:"approved_last_30_days"`
	// RejectedLast30Days counts rejected tasks in the window.
	RejectedLast30Days int64 `json:"rejected_last_30_days"`
	// OverduePending counts pending tasks past their SLA.
	OverduePending int64 `json:"overdue_pending"`
	// AverageDecisionHours is the mean submit→decide latency.
	AverageDecisionHours float64 `json:"average_decision_hours"`
	// FourEyesViolations is always 0 by construction (the service rejects
	// self-approval); surfaced so an auditor can confirm the control ran.
	FourEyesViolations int64 `json:"four_eyes_violations"`
}

// DashboardSearchQuality is the §12.5 block, embedded so the dashboard is one
// request instead of three.
type DashboardSearchQuality struct {
	// TotalQueries in the window.
	TotalQueries int64 `json:"total_queries"`
	// ZeroResultQueries in the window.
	ZeroResultQueries int64 `json:"zero_result_queries"`
	// ZeroResultRate is zero-result / total.
	ZeroResultRate float64 `json:"zero_result_rate"`
	// PermissionFilteredTotal is how many candidates policy removed.
	PermissionFilteredTotal int64 `json:"permission_filtered_total"`
	// AverageDurationMs is mean retrieval latency.
	AverageDurationMs float64 `json:"average_duration_ms"`
}

// DashboardTrendPoint is one day of the §16.1 "趋势" block.
type DashboardTrendPoint struct {
	// Day in YYYY-MM-DD (UTC).
	Day string `json:"day"`
	// KnowledgeAdded counts entries created that day.
	KnowledgeAdded int64 `json:"knowledge_added"`
	// ReviewsSubmitted counts approval tasks submitted that day.
	ReviewsSubmitted int64 `json:"reviews_submitted"`
	// Queries counts searches that day.
	Queries int64 `json:"queries"`
}

// DashboardContributor is one row of the §16.2 contribution leaderboard.
type DashboardContributor struct {
	// UserID being ranked.
	UserID string `json:"user_id"`
	// ReviewsCompleted counts approval decisions made.
	ReviewsCompleted int64 `json:"reviews_completed"`
	// Submitted counts approval tasks the user filed.
	Submitted int64 `json:"submitted"`
	// Answers counts community answers written.
	Answers int64 `json:"answers"`
	// Questions counts community questions asked.
	Questions int64 `json:"questions"`
	// Score is the weighted sum used for ordering.
	Score int64 `json:"score"`
}

// DashboardCostAllocation is one (space, scenario) cost bucket (§16.1
// "按空间/场景分摊").
type DashboardCostAllocation struct {
	// KnowledgeBaseID is the space the spend is attributed to; empty means
	// unallocated (multi-space or non-space calls).
	KnowledgeBaseID string `json:"knowledge_base_id"`
	// KnowledgeBaseName is resolved for display; empty for unallocated.
	KnowledgeBaseName string `json:"knowledge_base_name"`
	// Scene is the cost scenario (chat/summary/embedding/...).
	Scene string `json:"scene"`
	// PromptTokens / CompletionTokens / TotalTokens in the window.
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
	// CallCount is the number of model invocations in this bucket.
	CallCount int64 `json:"call_count"`
}

// DashboardCostSpace is the storage/index footprint of one space (§16.1
// "存储、索引成本"). It is measured in bytes and chunk counts, not currency:
// the platform has no per-model price catalog, so inventing a money figure
// would be wrong.
type DashboardCostSpace struct {
	// KnowledgeBaseID is the space.
	KnowledgeBaseID string `json:"knowledge_base_id"`
	// KnowledgeBaseName is resolved for display.
	KnowledgeBaseName string `json:"knowledge_base_name"`
	// StorageBytes is the sum of storage_size across the space's knowledge.
	StorageBytes int64 `json:"storage_bytes"`
	// KnowledgeCount is how many knowledge entries the space holds.
	KnowledgeCount int64 `json:"knowledge_count"`
	// ChunkCount is the vector/keyword index footprint proxy: every chunk is
	// one indexed unit, so index cost grows with it.
	ChunkCount int64 `json:"chunk_count"`
}

// DashboardCost is the §16.1 "成本" block.
type DashboardCost struct {
	// PromptTokens in the window.
	PromptTokens int64 `json:"prompt_tokens"`
	// CompletionTokens in the window.
	CompletionTokens int64 `json:"completion_tokens"`
	// TotalTokens is the sum.
	TotalTokens int64 `json:"total_tokens"`
	// CallCount is the number of model invocations recorded.
	CallCount int64 `json:"call_count"`
	// ByModel breaks the tokens down per model id.
	ByModel map[string]int64 `json:"by_model"`
	// ByScene breaks the tokens down per scenario (§16.1 按场景).
	ByScene map[string]int64 `json:"by_scene"`
	// Allocations breaks the spend down per (space, scenario) bucket.
	Allocations []DashboardCostAllocation `json:"allocations"`
	// StorageBySpace is the storage/index footprint per space.
	StorageBySpace []DashboardCostSpace `json:"storage_by_space"`
	// UnallocatedTokens is spend that cannot be attributed to one space. It
	// is reported explicitly so the per-space rows never silently fail to add
	// up to the total.
	UnallocatedTokens int64 `json:"unallocated_tokens"`
	// TotalStorageBytes is the tenant-wide storage footprint.
	TotalStorageBytes int64 `json:"total_storage_bytes"`
	// TotalChunks is the tenant-wide index footprint.
	TotalChunks int64 `json:"total_chunks"`
}

// DashboardObservability is the §17.2 "可观测" block: the runtime signals an
// operator watches, derived from the search log rather than a separate metric
// store so the numbers can never disagree with the search-quality tab.
type DashboardObservability struct {
	// DegradedQueries counts searches that returned nothing (a proxy for
	// zero-result pressure; the RAG path refuses on these).
	ZeroResultQueries int64 `json:"zero_result_queries"`
	// RefusalRate is zero-result / total queries in the window.
	RefusalRate float64 `json:"refusal_rate"`
	// PermissionBlocked is how many candidates policy removed before recall.
	PermissionBlocked int64 `json:"permission_blocked"`
	// AverageLatencyMs is mean retrieval latency.
	AverageLatencyMs float64 `json:"average_latency_ms"`
	// SlowQueries counts searches over the §17.1 2s degradation threshold.
	SlowQueries int64 `json:"slow_queries"`
	// OverdueApprovals is the审批 backlog past SLA (operator action signal).
	OverdueApprovals int64 `json:"overdue_approvals"`
}

// OperationsDashboard is the composed §16.1 payload.
type OperationsDashboard struct {
	// Overview is the headline counts.
	Overview DashboardOverview `json:"overview"`
	// Governance is the attention queue.
	Governance DashboardGovernance `json:"governance"`
	// Delivery is the approval throughput block.
	Delivery DashboardDelivery `json:"delivery"`
	// SearchQuality is the retrieval health block.
	SearchQuality DashboardSearchQuality `json:"search_quality"`
	// Trends is the per-day trend series (§16.1 趋势).
	Trends []DashboardTrendPoint `json:"trends"`
	// Contributors is the contribution leaderboard (§16.2).
	Contributors []DashboardContributor `json:"contributors"`
	// Cost is the model spend block (§16.1 成本).
	Cost DashboardCost `json:"cost"`
	// Observability is the §17.2 runtime-health block.
	Observability DashboardObservability `json:"observability"`
	// Health is the latest tenant-scope health snapshot, when one exists.
	Health *KnowledgeHealthSnapshot `json:"health,omitempty"`
	// WindowDays is the look-back window the time-based numbers cover.
	WindowDays int `json:"window_days"`
}
