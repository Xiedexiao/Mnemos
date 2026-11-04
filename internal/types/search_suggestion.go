package types

import (
	"sort"
	"strings"
)

// ============================================================================
// 检索建议（KnowAxis V3.0 §12.1 "搜索建议：实时联想、历史搜索、热门搜索；
// 建议只来自用户可见集合"）。
//
// 平台此前三个建议来源都在前端 localStorage：刷新换设备就丢、换人共用同一
// 浏览器会串味、热门榜更是根本没有。服务端化要解决的不只是持久化，还有
// §12.6 的四条泄露规则——本地存储天然不会泄露，服务端接口稍不注意就会。
//
// 四个决定：
//  1. 三条来源分开返回（历史 / 热门 / 联想），不合并成一个"建议列表"。
//     读者点开搜索框时想知道"我刚才搜了什么"和"大家在看什么"，混在一起
//     就无法解释某一条为什么出现。前端按需渲染。
//  2. 联想候选来自**调用者可见集合**：标题补全在服务层按 ACL 过滤后才返回。
//     直接查标题表再返回，等于提供了一个"猜标题就能探测存在性"的通道。
//  3. 热门词按调用者权限**重算**，不是读一个全局榜单再过滤：全局榜单的
//     排序位次本身就泄露了"还有多少人在查只有高层能看到的东西"。
//  4. 建议不写审计、不做每用户统计。建议列表是高频接口，逐次审计只会淹没
//     审计日志；而"谁搜了什么"已经由 search_logs 记录。
// ============================================================================

const (
	// DefaultSuggestionLimit caps one suggestion bucket.
	DefaultSuggestionLimit = 8
	// MaxSuggestionLimit caps it hard.
	MaxSuggestionLimit = 30
	// DefaultHotQueryDays is the hot-list look-back.
	DefaultHotQueryDays = 30
	// SuggestionMinPrefixRunes is the shortest prefix worth suggesting on.
	//
	// 一个字符的联想等于把词汇表倒出来：中文单字能拼出成千上万条标题，
	// 既没有用，也把"有哪些标题"这件事变得更容易批量探测。
	SuggestionMinPrefixRunes = 2
)

// SuggestionKind names where one suggestion came from.
type SuggestionKind string

const (
	// SuggestionKindHistory 历史搜索：调用者自己搜过的词。
	SuggestionKindHistory SuggestionKind = "history"
	// SuggestionKindHot 热门搜索：本租户窗口内被搜得最多的词。
	SuggestionKindHot SuggestionKind = "hot"
	// SuggestionKindTitle 实时联想：标题里含当前前缀的可见文档。
	SuggestionKindTitle SuggestionKind = "title"
	// SuggestionKindGlossary 术语联想：标准名/别名命中当前前缀。
	SuggestionKindGlossary SuggestionKind = "glossary"
)

// Valid reports whether the kind is known.
func (k SuggestionKind) Valid() bool {
	switch k {
	case SuggestionKindHistory, SuggestionKindHot,
		SuggestionKindTitle, SuggestionKindGlossary:
		return true
	default:
		return false
	}
}

// SearchSuggestion is one suggested query or target.
type SearchSuggestion struct {
	// Text is what gets inserted into the search box.
	Text string `json:"text"`
	// Kind is the source bucket (see SuggestionKind).
	Kind SuggestionKind `json:"kind"`
	// Label is an optional display label when Text alone is ambiguous.
	Label string `json:"label,omitempty"`
	// KnowledgeID is set for title suggestions, so the UI can jump straight
	// to the document instead of re-searching for its title.
	KnowledgeID string `json:"knowledge_id,omitempty"`
	// KnowledgeBaseID scopes a title suggestion.
	KnowledgeBaseID string `json:"knowledge_base_id,omitempty"`
	// Count is the occurrence count for hot entries; 0 elsewhere.
	Count int64 `json:"count,omitempty"`
}

// SuggestionBundle is the §12.1 read model for one caller.
type SuggestionBundle struct {
	// History is the caller's own recent queries, newest first.
	History []SearchSuggestion `json:"history"`
	// Hot is the tenant's most frequent queries in the window.
	Hot []SearchSuggestion `json:"hot"`
	// Titles are visible documents whose title matches the prefix.
	Titles []SearchSuggestion `json:"titles"`
	// GlossaryTerms are controlled-vocabulary hits for the prefix.
	GlossaryTerms []SearchSuggestion `json:"glossary_terms"`
	// Prefix echoes the normalized query the suggestions answer.
	Prefix string `json:"prefix"`
	// HotWindowDays is the look-back behind Hot.
	HotWindowDays int `json:"hot_window_days"`
}

// ClampSuggestionLimit bounds the per-bucket limit.
func ClampSuggestionLimit(limit int) int {
	if limit <= 0 {
		return DefaultSuggestionLimit
	}
	if limit > MaxSuggestionLimit {
		return MaxSuggestionLimit
	}
	return limit
}

// ClampHotQueryDays bounds the hot-list window.
func ClampHotQueryDays(days int) int {
	if days <= 0 {
		return DefaultHotQueryDays
	}
	if days > 365 {
		return 365
	}
	return days
}

// NormalizeSuggestionPrefix trims and lowercases for matching.
//
// 与 NormalizeSearchQuery 不同，这里**不截断**：前缀很长意味着用户已经
// 输入得很具体，截断只会让匹配落空。长度上限在前缀检查里处理。
func NormalizeSuggestionPrefix(prefix string) string {
	return strings.Join(strings.Fields(strings.ToLower(prefix)), " ")
}

// SuggestionPrefixUsable reports whether the prefix is long enough to suggest.
func SuggestionPrefixUsable(prefix string) bool {
	return len([]rune(strings.TrimSpace(prefix))) >= SuggestionMinPrefixRunes
}

// DedupeSuggestions removes duplicates by text, keeping the first occurrence.
//
// 热门词与历史词经常重叠。按 Text 去重（保留先出现的那条）而不是按
// (Text,Kind)：同一个词出现在两个桶里，对读者毫无信息量。跨桶去重时先
// 放历史再放热门——"我刚搜过"比"大家常搜"更贴近当下意图。
func DedupeSuggestions(groups ...[]SearchSuggestion) []SearchSuggestion {
	seen := make(map[string]struct{}, 16)
	out := make([]SearchSuggestion, 0, 16)
	for _, group := range groups {
		for _, item := range group {
			key := strings.ToLower(strings.TrimSpace(item.Text))
			if key == "" {
				continue
			}
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, item)
		}
	}
	return out
}

// SortHotSuggestions orders hot entries by count desc then text asc.
func SortHotSuggestions(items []SearchSuggestion) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Count != items[j].Count {
			return items[i].Count > items[j].Count
		}
		return items[i].Text < items[j].Text
	})
}
