package types

import (
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ============================================================================
// 站内推荐与浏览历史（KnowAxis V3.0 §15.3 阅读 / §16.1 空间与热门）。
//
// §15.3/§16.1 要求"猜你想学、最新动态、热门知识、相关知识"，此前这些全部
// 没有：读者在平台里除了搜索没有第二条发现路径，浏览历史只存在前端
// localStorage（换设备即丢，也无法用于热度统计）。
//
// 三张表/一件事：
//   - KnowledgeViewEvent 记录一次阅读，既支撑"热门"计数，也支撑"猜你想学"
//     的偏好来源，同时就是浏览历史（后端版，跨设备）；
//   - RecommendationItem 是一次推荐的外部形状，带"为什么推荐"的原因；
//   - 推荐本身不建表：它是按需计算的投影，不落库就不会出现"推荐结果过期
//     但用户还能看到"的问题，也不会有一张随时间膨胀的表。
//
// 关键约束：所有推荐都必须先过权限（ACL + 密级 + 生命周期），推荐位是
// 最容易泄露"存在什么"的通道，§2.10 对它的要求与列表页相同。
// ============================================================================

// RecommendationReason explains why one item is being recommended.
type RecommendationReason string

const (
	// RecommendReasonRelated 来自知识关联（依据/替代/补充/前置/冲突等）。
	RecommendReasonRelated RecommendationReason = "related_relation"
	// RecommendReasonSameTag 与当前知识共享标签。
	RecommendReasonSameTag RecommendationReason = "same_tag"
	// RecommendReasonSameSpace 同一空间内的其他知识。
	RecommendReasonSameSpace RecommendationReason = "same_space"
	// RecommendReasonInterest 与用户最近阅读/收藏的主题相关（猜你想学）。
	RecommendReasonInterest RecommendationReason = "interest"
	// RecommendReasonLatest 最近更新（最新动态）。
	RecommendReasonLatest RecommendationReason = "latest"
	// RecommendReasonHot 近期阅读最多（热门知识）。
	RecommendReasonHot RecommendationReason = "hot"
)

// Valid reports whether the reason is known.
func (r RecommendationReason) Valid() bool {
	switch r {
	case RecommendReasonRelated, RecommendReasonSameTag, RecommendReasonSameSpace,
		RecommendReasonInterest, RecommendReasonLatest, RecommendReasonHot:
		return true
	default:
		return false
	}
}

// KnowledgeViewEvent is one reading event (§15.3 浏览历史 + §16.1 热门).
//
// 同一用户对同一篇知识在一个短窗口内只记一行（见服务层去重），否则刷新
// 页面就能把热度刷上去。
type KnowledgeViewEvent struct {
	ID              string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID        uint64 `json:"tenant_id" gorm:"not null;index:idx_knowledge_views_hot,priority:1"`
	UserID          string `json:"user_id" gorm:"type:varchar(36);not null;index:idx_knowledge_views_history,priority:2"`
	KnowledgeID     string `json:"knowledge_id" gorm:"type:varchar(36);not null;index:idx_knowledge_views_hot,priority:2"`
	KnowledgeBaseID string `json:"knowledge_base_id" gorm:"type:varchar(36);not null;default:''"`
	// CreatedAt is indexed descending for both the hot window and history.
	CreatedAt time.Time `json:"created_at" gorm:"index:idx_knowledge_views_hot,priority:3,sort:desc;index:idx_knowledge_views_history,priority:3,sort:desc"`
}

// TableName pins the GORM table name.
func (KnowledgeViewEvent) TableName() string { return "knowledge_view_events" }

// BeforeCreate assigns an ID before persistence.
func (e *KnowledgeViewEvent) BeforeCreate(_ *gorm.DB) error {
	if e.ID == "" {
		e.ID = uuid.NewString()
	}
	return nil
}

// RecommendationItem is one recommended document.
type RecommendationItem struct {
	// KnowledgeID / KnowledgeBaseID identify the document.
	KnowledgeID     string `json:"knowledge_id"`
	KnowledgeBaseID string `json:"knowledge_base_id"`
	// KnowledgeBaseName is resolved for display.
	KnowledgeBaseName string `json:"knowledge_base_name,omitempty"`
	// Title / Summary are display fields.
	Title   string `json:"title"`
	Summary string `json:"summary,omitempty"`
	// LifecycleStatus / Classification let the UI badge the entry without a
	// second request.
	LifecycleStatus string `json:"lifecycle_status"`
	Classification  string `json:"classification"`
	// Reason explains the placement (shown as the card caption).
	Reason RecommendationReason `json:"reason"`
	// MatchedTag names the tag that caused a same-tag / interest match.
	MatchedTag string `json:"matched_tag,omitempty"`
	// Score ranks the item inside one recommendation list.
	Score float64 `json:"score"`
	// UpdatedAt is the document's last change.
	UpdatedAt time.Time `json:"updated_at"`
}

// RecommendationBoard is the §15.3 reading-page payload: four independent
// lists computed from the same document.
type RecommendationBoard struct {
	// KnowledgeID the board describes (empty for the personal home board).
	KnowledgeID string `json:"knowledge_id,omitempty"`
	// Related is "相关知识".
	Related []RecommendationItem `json:"related"`
	// ForYou is "猜你想学".
	ForYou []RecommendationItem `json:"for_you"`
	// Latest is "最新动态".
	Latest []RecommendationItem `json:"latest"`
	// Hot is "热门知识".
	Hot []RecommendationItem `json:"hot"`
}

// ViewHistoryItem is one entry of the backend browse history.
type ViewHistoryItem struct {
	KnowledgeID     string `json:"knowledge_id"`
	KnowledgeBaseID string `json:"knowledge_base_id"`
	Title           string `json:"title"`
	// Visible is false when the entry can no longer be opened (permission
	// revoked or delisted). The row still appears so the reader is not
	// surprised by a gap, but the title is blanked by the handler.
	Visible  bool      `json:"visible"`
	ViewedAt time.Time `json:"viewed_at"`
}

// RecommendationWeights tunes the ranking without touching the queries.
type RecommendationWeights struct {
	// RelationEdge 是"有关联关系"的权重。
	RelationEdge float64
	// SharedTag 是每共享一个标签的权重。
	SharedTag float64
	// SameSpace 是同空间但无关联时的基础权重。
	SameSpace float64
	// InterestTag 是命中用户兴趣标签的权重。
	InterestTag float64
	// HotPerView 是窗口内每次阅读的权重。
	HotPerView float64
	// FreshnessHalfLifeHours 越新越靠前的半衰期。
	FreshnessHalfLifeHours float64
}

// DefaultRecommendationWeights is the v1 ranking.
func DefaultRecommendationWeights() RecommendationWeights {
	return RecommendationWeights{
		RelationEdge:           10,
		SharedTag:              2,
		SameSpace:              1,
		InterestTag:            3,
		HotPerView:             1,
		FreshnessHalfLifeHours: 168, // 一周
	}
}

// RecommendationLimits bounds one list.
const (
	// DefaultRecommendationLimit is the per-list default.
	DefaultRecommendationLimit = 5
	// MaxRecommendationLimit caps one list.
	MaxRecommendationLimit = 20
	// RecommendationViewDedupeWindow 内同一用户对同一篇知识只记一次阅读。
	RecommendationViewDedupeWindow = 30 * time.Minute
	// RecommendationInterestWindow 是"猜你想学"参考的阅读/收藏回看窗口。
	RecommendationInterestWindow = 30 * 24 * time.Hour
	// RecommendationHotWindowDays 是"热门"的默认统计窗口。
	RecommendationHotWindowDays = 30
)

// NormalizeRecommendationLimit clamps a requested list size.
func NormalizeRecommendationLimit(limit int) int {
	if limit <= 0 {
		return DefaultRecommendationLimit
	}
	if limit > MaxRecommendationLimit {
		return MaxRecommendationLimit
	}
	return limit
}

// NormalizeKnowledgeIDs trims, dedupes and caps a candidate id list.
func NormalizeKnowledgeIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}
