package types

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ============================================================================
// 保存的视图（KnowAxis V3.0 §12.2 "组合筛选；筛选条件可保存为我的视图"）。
//
// 平台此前只能把筛选条件放在 URL 里，切走再回来就丢了；一个"我负责的设备
// 的待复审条目"要每次重新点五六个条件。保存的视图把这组条件存成一行。
//
// 五个决定：
//  1. 视图是**个人**的：作用域 (tenant, user)，不是租户共享。团队共享视图
//     会带来"这个视图是谁维护的、改了影响谁"的治理问题，而 §12.2 要的是
//     个人效率。共享视图确实有用，但那是一个独立的协作功能，不该混进来。
//  2. 名称在 (tenant, user) 内唯一：两个同名视图会让人无法判断点哪个。
//  3. 只保存**筛选与排序**，不保存分页：记住"第 3 页"没有意义，第 3 页的
//     内容每次都不一样。
//  4. 条件以 JSON 存储而不是拆成十几个列：筛选维度还会增长（每加一个维度
//     就要一次迁移），而视图条件从不需要被 SQL 单独查询。代价是无法按条件
//     内容做聚合——本功能也不需要。
//  5. 保存时**校验**条件，读取时**再校验一次**：一个视图存了三年后某个枚举
//     值可能已被废弃，读取时应当明确报错而不是静默给出空列表。
// ============================================================================

const (
	// MaxSavedViewsPerUser caps one user's saved views.
	//
	// 上限是为了让"我的视图"保持可扫描：几十个视图的列表已经比手动点筛选
	// 更慢，那它就失去了意义。
	MaxSavedViewsPerUser = 50
	// MaxSavedViewNameRunes bounds the display name.
	MaxSavedViewNameRunes = 60
	// MaxSavedViewTagCount bounds the tag list inside one view.
	MaxSavedViewTagCount = 50
	// MaxSavedViewKeywordRunes bounds the keyword.
	MaxSavedViewKeywordRunes = 200
)

// SavedKnowledgeView is one user's saved filter set on a knowledge base.
type SavedKnowledgeView struct {
	ID       string `json:"id" gorm:"type:varchar(36);primaryKey" extensions:"x-agent=view_id"`
	TenantID uint64 `json:"tenant_id" gorm:"not null;uniqueIndex:uq_saved_view_user_name,priority:1"`
	UserID   string `json:"user_id" gorm:"type:varchar(36);not null;uniqueIndex:uq_saved_view_user_name,priority:2"`
	// Name is the display label, unique per user.
	Name string `json:"name" gorm:"type:varchar(120);not null;uniqueIndex:uq_saved_view_user_name,priority:3" extensions:"x-agent"`
	// KnowledgeBaseID scopes the view. Empty means "whichever space the
	// reader opens it in" — a saved view that only makes sense in one space
	// should say so.
	KnowledgeBaseID string `json:"knowledge_base_id" gorm:"type:varchar(36);not null;default:''" extensions:"x-agent=knowledge_base_id"`
	// Filter is the frozen filter set (see SavedViewFilter). Stored as a JSON
	// column, so the struct carries the raw form and the typed view is derived.
	Filter JSON `json:"filter" gorm:"column:filter;type:json;not null"`
	// IsDefault marks the view to apply when the space is opened.
	// At most one default per (user, knowledge base); enforced in the service
	// because a partial unique index is not portable across the dialects.
	IsDefault bool      `json:"is_default" gorm:"not null;default:false" extensions:"x-agent"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName pins the GORM table name.
func (SavedKnowledgeView) TableName() string { return "saved_knowledge_views" }

// BeforeCreate assigns an ID before persistence.
func (v *SavedKnowledgeView) BeforeCreate(_ *gorm.DB) error {
	if v.ID == "" {
		v.ID = uuid.NewString()
	}
	return nil
}

// SavedViewFilterOf decodes a stored row's filter into its typed form.
//
// 坏 JSON 按空条件处理而不是报错：一个损坏的视图应当退化为"无条件列表"，
// 而不是让整个视图列表打不开。
func (v *SavedKnowledgeView) SavedViewFilterOf() SavedViewFilter {
	var filter SavedViewFilter
	if v == nil || len(v.Filter) == 0 {
		return filter
	}
	if err := json.Unmarshal(v.Filter, &filter); err != nil {
		return SavedViewFilter{}
	}
	return filter
}

// SetFilter encodes the typed filter into the JSON column.
func (v *SavedKnowledgeView) SetFilter(filter SavedViewFilter) error {
	raw, err := json.Marshal(filter)
	if err != nil {
		return err
	}
	v.Filter = JSON(raw)
	return nil
}

// SavedViewFilter is the frozen filter set a view replays.
//
// 字段与 KnowledgeListFilter 的"用户可设置"子集一一对应。刻意**不含**
// ExcludeKnowledgeIDs、MaxClassification：这两项由服务端按调用者权限填充，
// 存进视图就等于把一个时刻的权限结论固化下来——权限变了，视图会继续用旧的
// 拒绝集，既可能越权也可能过度遮蔽。
type SavedViewFilter struct {
	// Keyword is a title/filename substring.
	Keyword string `json:"keyword,omitempty"`
	// TagIDs match any of the given tags (OR).
	TagIDs []string `json:"tag_ids,omitempty"`
	// FileType filters the file type ("pdf", "manual", "url", ...).
	FileType string `json:"file_type,omitempty"`
	// ParseStatus filters the ingestion state.
	ParseStatus string `json:"parse_status,omitempty"`
	// Source filters the ingestion channel.
	Source string `json:"source,omitempty"`
	// UpdatedFrom / UpdatedTo are RFC3339 bounds, empty for unbounded.
	UpdatedFrom string `json:"updated_from,omitempty"`
	UpdatedTo   string `json:"updated_to,omitempty"`
	// FolderPath / FolderScope reproduce the sidebar folder selection.
	FolderPath  string `json:"folder_path,omitempty"`
	FolderScope string `json:"folder_scope,omitempty"`
	// LifecycleStatuses restricts to these governance states.
	LifecycleStatuses []string `json:"lifecycle_statuses,omitempty"`
	// SortBy / SortOrder reproduce the list ordering.
	SortBy    string `json:"sort_by,omitempty"`
	SortOrder string `json:"sort_order,omitempty"`
}

// SavedViewInput is the create/update payload.
type SavedViewInput struct {
	Name            string          `json:"name"`
	KnowledgeBaseID string          `json:"knowledge_base_id"`
	Filter          SavedViewFilter `json:"filter"`
	// IsDefault promotes this view to the space default. Promoted here rather
	// than through a separate endpoint because "make it my default" is only
	// meaningful together with the view it points at.
	IsDefault bool `json:"is_default"`
}

// ValidateSavedViewName normalizes and validates a view name.
func ValidateSavedViewName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", fmt.Errorf("view name is required")
	}
	if runes := []rune(trimmed); len(runes) > MaxSavedViewNameRunes {
		return "", fmt.Errorf("view name must be at most %d characters", MaxSavedViewNameRunes)
	}
	return trimmed, nil
}

// ValidateSavedViewFilter checks every dimension against its whitelist.
//
// 校验放在这里而不是交给数据库：这些枚举写错时查询会**静默返回空列表**，
// 用户会以为"这个视图下没有内容"。宁可保存时报错。
func ValidateSavedViewFilter(filter SavedViewFilter) error {
	if runes := []rune(strings.TrimSpace(filter.Keyword)); len(runes) > MaxSavedViewKeywordRunes {
		return fmt.Errorf("keyword must be at most %d characters", MaxSavedViewKeywordRunes)
	}
	if len(filter.TagIDs) > MaxSavedViewTagCount {
		return fmt.Errorf("at most %d tags may be saved", MaxSavedViewTagCount)
	}
	for _, tagID := range filter.TagIDs {
		if strings.TrimSpace(tagID) == "" {
			return fmt.Errorf("tag ids must not be blank")
		}
	}
	if filter.FolderScope != "" {
		scope := KnowledgeFolderScope(filter.FolderScope)
		switch scope {
		case FolderScopeAny, FolderScopeExact, FolderScopeSubtree:
		default:
			return fmt.Errorf("unknown folder scope %q", filter.FolderScope)
		}
		// 目录条件不带路径时等于没有条件：明确拒绝，而不是静默忽略。
		if scope != FolderScopeAny && strings.TrimSpace(filter.FolderPath) == "" {
			return fmt.Errorf("folder path is required when a folder scope is set")
		}
	}
	for _, status := range filter.LifecycleStatuses {
		if !KnowledgeLifecycleStatus(status).Valid() {
			return fmt.Errorf("unknown lifecycle status %q", status)
		}
	}
	if filter.SortBy != "" && !KnowledgeListSortField(filter.SortBy).Valid() {
		return fmt.Errorf("unknown sort field %q", filter.SortBy)
	}
	if filter.SortOrder != "" && !KnowledgeListSortOrder(filter.SortOrder).Valid() {
		return fmt.Errorf("unknown sort order %q", filter.SortOrder)
	}
	if filter.UpdatedFrom != "" {
		if _, err := time.Parse(time.RFC3339, filter.UpdatedFrom); err != nil {
			return fmt.Errorf("updated_from must be RFC3339: %w", err)
		}
	}
	if filter.UpdatedTo != "" {
		if _, err := time.Parse(time.RFC3339, filter.UpdatedTo); err != nil {
			return fmt.Errorf("updated_to must be RFC3339: %w", err)
		}
	}
	return nil
}

// Normalize trims and dedupes the filter's free-form fields.
func (f *SavedViewFilter) Normalize() {
	f.Keyword = strings.TrimSpace(f.Keyword)
	f.FileType = strings.TrimSpace(f.FileType)
	f.ParseStatus = strings.TrimSpace(f.ParseStatus)
	f.Source = strings.TrimSpace(f.Source)
	f.UpdatedFrom = strings.TrimSpace(f.UpdatedFrom)
	f.UpdatedTo = strings.TrimSpace(f.UpdatedTo)
	f.FolderPath = strings.TrimSpace(f.FolderPath)
	if len(f.TagIDs) > 0 {
		seen := make(map[string]struct{}, len(f.TagIDs))
		cleaned := make([]string, 0, len(f.TagIDs))
		for _, tagID := range f.TagIDs {
			trimmed := strings.TrimSpace(tagID)
			if trimmed == "" {
				continue
			}
			if _, duplicate := seen[trimmed]; duplicate {
				continue
			}
			seen[trimmed] = struct{}{}
			cleaned = append(cleaned, trimmed)
		}
		f.TagIDs = cleaned
	}
	if len(f.LifecycleStatuses) > 0 {
		seen := make(map[string]struct{}, len(f.LifecycleStatuses))
		cleaned := make([]string, 0, len(f.LifecycleStatuses))
		for _, status := range f.LifecycleStatuses {
			trimmed := strings.TrimSpace(status)
			if trimmed == "" {
				continue
			}
			if _, duplicate := seen[trimmed]; duplicate {
				continue
			}
			seen[trimmed] = struct{}{}
			cleaned = append(cleaned, trimmed)
		}
		f.LifecycleStatuses = cleaned
	}
}

// ToListFilter converts a saved view into the list filter the repository uses.
//
// 只有用户可设置的维度被还原；ExcludeKnowledgeIDs / MaxClassification 保持
// 零值，由调用方（列表接口）按**当前**权限填充。这是视图"不会把旧权限固化"
// 的实现点。
func (f SavedViewFilter) ToListFilter() KnowledgeListFilter {
	filter := KnowledgeListFilter{
		Keyword:           strings.TrimSpace(f.Keyword),
		TagIDs:            append([]string(nil), f.TagIDs...),
		FileType:          strings.TrimSpace(f.FileType),
		ParseStatus:       strings.TrimSpace(f.ParseStatus),
		Source:            strings.TrimSpace(f.Source),
		FolderPath:        f.FolderPath,
		FolderScope:       KnowledgeFolderScope(f.FolderScope),
		LifecycleStatuses: append([]string(nil), f.LifecycleStatuses...),
		SortBy:            KnowledgeListSortField(f.SortBy),
		SortOrder:         KnowledgeListSortOrder(f.SortOrder),
	}
	if from, err := time.Parse(time.RFC3339, f.UpdatedFrom); err == nil {
		filter.UpdatedFrom = from
	}
	if to, err := time.Parse(time.RFC3339, f.UpdatedTo); err == nil {
		filter.UpdatedTo = to
	}
	return filter
}

// SavedViewList is the list response, with the default flagged.
type SavedViewList struct {
	Views []*SavedKnowledgeView `json:"views"`
	// DefaultViewID is the view to auto-apply, "" when none is set.
	DefaultViewID string `json:"default_view_id,omitempty"`
}
