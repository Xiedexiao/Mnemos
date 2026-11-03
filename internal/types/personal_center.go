package types

// ============================================================================
// 个人中心（KnowAxis V3.0 §15.3）。
//
// 平台此前没有任何"以我为中心"的视图：读者看不到自己收藏了什么、提交了
// 哪些待审、有哪些评论被回复。§15.3 把这些收进一处，让知识库对个人也有
// 工作台属性，而不只是"搜一下就走"。
// ============================================================================

// PersonalCenterOverview is the "我的主页" summary.
type PersonalCenterOverview struct {
	// UserID the summary belongs to.
	UserID string `json:"user_id"`
	// FavoriteCount is how many resources the user starred.
	FavoriteCount int64 `json:"favorite_count"`
	// PendingReviewCount is how many review tasks the user submitted that are
	// still pending.
	PendingReviewCount int64 `json:"pending_review_count"`
	// LatestAnnouncementCount reserved for unread notices.
	LatestAnnouncementCount int64 `json:"latest_announcement_count"`
}

// PersonalKnowledgeItem is one entry in "我的知识".
type PersonalKnowledgeItem struct {
	// ID of the knowledge row.
	ID string `json:"id"`
	// Title for display.
	Title string `json:"title"`
	// KnowledgeBaseID it belongs to.
	KnowledgeBaseID string `json:"knowledge_base_id"`
	// LifecycleStatus at read time.
	LifecycleStatus string `json:"lifecycle_status"`
	// ParseStatus of the ingestion pipeline.
	ParseStatus string `json:"parse_status"`
	// UpdatedAt for ordering.
	UpdatedAt string `json:"updated_at"`
}

// PersonalReviewItem is one entry in "待我审 / 我提交的".
type PersonalReviewItem struct {
	// TaskID of the approval task.
	TaskID string `json:"task_id"`
	// KnowledgeID under review.
	KnowledgeID string `json:"knowledge_id"`
	// Title of the knowledge (from the frozen baseline).
	Title string `json:"title"`
	// Status of the task.
	Status string `json:"status"`
	// RiskLevel drives the review workbench ordering.
	RiskLevel string `json:"risk_level"`
	// SLADue for overdue highlighting.
	SLADue string `json:"sla_due"`
	// CreatedAt for ordering.
	CreatedAt string `json:"created_at"`
}

// PersonalFavoriteItem is one entry in "我的收藏".
type PersonalFavoriteItem struct {
	// ResourceType is kb / agent / knowledge.
	ResourceType string `json:"resource_type"`
	// ResourceID of the starred resource.
	ResourceID string `json:"resource_id"`
	// Label is a best-effort display name (empty when the resource is gone).
	Label string `json:"label"`
	// Visible reports whether the user may still see the resource. A favorite
	// whose permission was revoked is hidden by the caller (§15.3 收藏不改变权限，
	// 权限失效后自动隐藏).
	Visible bool `json:"visible"`
	// CreatedAt for ordering.
	CreatedAt string `json:"created_at"`
}

// PersonalCenter is the composed payload of the personal center.
type PersonalCenter struct {
	Overview  PersonalCenterOverview  `json:"overview"`
	Knowledge []PersonalKnowledgeItem `json:"knowledge"`
	Reviews   []PersonalReviewItem    `json:"reviews"`
	Favorites []PersonalFavoriteItem  `json:"favorites"`
}
