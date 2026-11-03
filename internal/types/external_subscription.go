// Package types: 外部订阅采集（KnowAxis V3.0 §7.3）。
//
// §7.3 的信号目录里，"外部订阅"是唯一不能靠自己产生的一类：法规、国标、
// 行业标准、监管口径的更新发生在平台之外。平台此前已经有信号队列和
// change_signals 表，但没有采集端——这一类信号只能靠人手工上报。
//
// 这里补上采集端的模型：
//
//   - ExternalSubscriptionFeed 描述一个订阅源（URL + 领域标签 + 检查周期）；
//   - ExternalSubscriptionEntry 记录已见过的条目，指纹用于去重，保证同一
//     条标准不会每轮检查都产生一张变更单；
//   - SyncResult / SweepResult 把一次采集的结果如实报出来，包括"没有匹配
//     到知识"这种需要人确认覆盖缺口的情况。
//
// 领域匹配走知识标签（tag）而不是全文猜测：标签是平台上唯一显式的领域
// 声明，猜关键词会把不相干的文档卷进合规复审。
package types

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ExternalFeedFormat is the wire format of a subscription feed.
type ExternalFeedFormat string

const (
	// ExternalFeedRSS is an RSS 2.0 feed.
	ExternalFeedRSS ExternalFeedFormat = "rss"
	// ExternalFeedAtom is an Atom feed.
	ExternalFeedAtom ExternalFeedFormat = "atom"
	// ExternalFeedJSON is a JSON Feed (jsonfeed.org).
	ExternalFeedJSON ExternalFeedFormat = "json"
	// ExternalFeedAuto lets the parser detect the format.
	ExternalFeedAuto ExternalFeedFormat = "auto"
)

// Valid reports whether the format is known.
func (f ExternalFeedFormat) Valid() bool {
	switch f {
	case ExternalFeedRSS, ExternalFeedAtom, ExternalFeedJSON, ExternalFeedAuto:
		return true
	default:
		return false
	}
}

// 订阅检查周期的边界。
const (
	// DefaultExternalFeedIntervalHours 是未配置时的检查间隔：监管类站点
	// 一天内不会更新多次，24h 足够且不会给源站压力。
	DefaultExternalFeedIntervalHours = 24
	// MinExternalFeedIntervalHours 防止把源站当成被轮询的 API。
	MinExternalFeedIntervalHours = 1
	// MaxExternalFeedIntervalHours 是上限（30 天）。
	MaxExternalFeedIntervalHours = 720
	// ExternalSubscriptionMaxEntriesPerSync 是单次采集处理的条目上限。
	// 首次订阅时源站可能已有多年历史；一次全量灌入会瞬间刷满信号队列。
	ExternalSubscriptionMaxEntriesPerSync = 200
)

// ExternalSubscriptionFeed is one regulation/standards feed subscribed by a
// tenant.
type ExternalSubscriptionFeed struct {
	ID       string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID uint64 `json:"tenant_id" gorm:"not null;index:idx_external_feeds_tenant,priority:1"`
	// Name is the operator-facing label, e.g. "国标全文公开系统".
	Name string `json:"name" gorm:"type:varchar(255);not null"`
	// FeedURL is the RSS/Atom/JSON Feed endpoint.
	FeedURL string `json:"feed_url" gorm:"type:varchar(2048);not null"`
	// Format is the wire format; auto lets the parser decide.
	Format string `json:"format" gorm:"type:varchar(16);not null;default:'auto'"`
	// DomainTags holds knowledge tag names this feed covers. An entry from
	// this feed is matched onto every knowledge item carrying one of these
	// tags. Empty means "no domain mapping yet": entries still produce a
	// domain-level signal so the coverage gap is visible.
	DomainTags JSON `json:"domain_tags" gorm:"type:json;not null;default:'[]'"`
	// KnowledgeBaseIDs narrows the match to these knowledge bases. Empty means
	// every knowledge base in the tenant.
	KnowledgeBaseIDs JSON `json:"knowledge_base_ids" gorm:"type:json;not null;default:'[]'"`
	// Enabled controls whether the scheduler checks this feed.
	Enabled bool `json:"enabled" gorm:"not null;default:true"`
	// CheckIntervalHours is how often the scheduler polls the feed.
	CheckIntervalHours int `json:"check_interval_hours" gorm:"not null;default:24"`
	// LastCheckedAt is the last attempt (successful or not).
	LastCheckedAt *time.Time `json:"last_checked_at"`
	// LastSuccessAt is the last attempt that parsed at least one page.
	LastSuccessAt *time.Time `json:"last_success_at"`
	// LastError is the last failure, cleared by a successful sync.
	LastError string `json:"last_error" gorm:"type:text;not null;default:''"`
	// AuthHeaderEnc is an optional "Name: Value" request header (e.g. a token
	// for a paid standards API), AES-GCM encrypted at rest. Never serialized.
	AuthHeaderEnc string `json:"-" gorm:"column:auth_header_enc;type:text;not null;default:''"`
	// HasAuthHeader tells the UI whether a secret header is stored without
	// disclosing it.
	HasAuthHeader bool `json:"has_auth_header" gorm:"-"`
	// CreatedBy is the operator who subscribed the feed.
	CreatedBy string    `json:"created_by" gorm:"type:varchar(36);not null;default:''"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName pins the GORM table name.
func (ExternalSubscriptionFeed) TableName() string { return "external_subscription_feeds" }

// BeforeCreate assigns an ID before persistence.
func (f *ExternalSubscriptionFeed) BeforeCreate(_ *gorm.DB) error {
	if f.ID == "" {
		f.ID = uuid.NewString()
	}
	if f.Format == "" {
		f.Format = string(ExternalFeedAuto)
	}
	if len(f.DomainTags) == 0 {
		f.DomainTags = JSON("[]")
	}
	if len(f.KnowledgeBaseIDs) == 0 {
		f.KnowledgeBaseIDs = JSON("[]")
	}
	if f.CheckIntervalHours <= 0 {
		f.CheckIntervalHours = DefaultExternalFeedIntervalHours
	}
	return nil
}

// ExternalSubscriptionEntry records one entry already seen in a feed.
//
// 指纹而不是条目 ID 是去重键：监管站点经常重新发布同一条标准（新 URL、
// 同一正文），用 URL 去重会重复建单，用正文指纹才准确。
type ExternalSubscriptionEntry struct {
	ID       string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID uint64 `json:"tenant_id" gorm:"not null;index:idx_external_entries_feed,priority:1"`
	FeedID   string `json:"feed_id" gorm:"type:varchar(36);not null;index:idx_external_entries_feed,priority:2"`
	// ExternalID is the feed item's guid/id when present.
	ExternalID string `json:"external_id" gorm:"type:varchar(255);not null;default:''"`
	Title      string `json:"title" gorm:"type:varchar(512);not null"`
	URL        string `json:"url" gorm:"type:varchar(2048);not null;default:''"`
	// PublishedAt is the feed's timestamp for this entry, when present.
	PublishedAt *time.Time `json:"published_at"`
	// Fingerprint is the content identity used for dedupe.
	Fingerprint string `json:"fingerprint" gorm:"type:varchar(64);not null;default:''"`
	// MatchedKnowledgeCount is how many knowledge items the entry matched.
	MatchedKnowledgeCount int `json:"matched_knowledge_count" gorm:"not null;default:0"`
	// SignalCount is how many change signals the entry produced.
	SignalCount int       `json:"signal_count" gorm:"not null;default:0"`
	CreatedAt   time.Time `json:"created_at"`
}

// TableName pins the GORM table name.
func (ExternalSubscriptionEntry) TableName() string {
	return "external_subscription_entries"
}

// BeforeCreate assigns an ID before persistence.
func (e *ExternalSubscriptionEntry) BeforeCreate(_ *gorm.DB) error {
	if e.ID == "" {
		e.ID = uuid.NewString()
	}
	return nil
}

// ExternalSubscriptionFeedInput is the create/update payload.
type ExternalSubscriptionFeedInput struct {
	Name               string   `json:"name"`
	FeedURL            string   `json:"feed_url"`
	Format             string   `json:"format"`
	DomainTags         []string `json:"domain_tags"`
	KnowledgeBaseIDs   []string `json:"knowledge_base_ids"`
	Enabled            *bool    `json:"enabled"`
	CheckIntervalHours int      `json:"check_interval_hours"`
	// AuthHeader is an optional "Name: Value" header sent when fetching the
	// feed. Empty leaves the stored header unchanged on update; "-" clears it.
	AuthHeader string `json:"auth_header,omitempty"`
}

// ExternalSubscriptionSyncResult reports one feed check.
type ExternalSubscriptionSyncResult struct {
	FeedID string `json:"feed_id"`
	// Fetched is the number of entries the feed returned.
	Fetched int `json:"fetched"`
	// NewEntries is the number of entries not seen before.
	NewEntries int `json:"new_entries"`
	// MatchedKnowledge is the number of knowledge items matched across entries.
	MatchedKnowledge int `json:"matched_knowledge"`
	// Signals is the number of change signals created (dedupe may suppress).
	Signals int `json:"signals"`
	// UnmatchedEntries is how many new entries matched no knowledge. A
	// non-zero value is not an error: it may mean the domain tags are wrong.
	UnmatchedEntries int       `json:"unmatched_entries"`
	Truncated        bool      `json:"truncated"`
	SyncedAt         time.Time `json:"synced_at"`
}

// ExternalSubscriptionSweepResult reports one scheduler pass.
type ExternalSubscriptionSweepResult struct {
	FeedsChecked int       `json:"feeds_checked"`
	FeedsFailed  int       `json:"feeds_failed"`
	NewEntries   int       `json:"new_entries"`
	Signals      int       `json:"signals"`
	Errors       []string  `json:"errors,omitempty"`
	CheckedAt    time.Time `json:"checked_at"`
}

// NormalizeExternalFeedFormat fills the default and validates the format.
func NormalizeExternalFeedFormat(raw string) (ExternalFeedFormat, error) {
	format := ExternalFeedFormat(strings.ToLower(strings.TrimSpace(raw)))
	if format == "" {
		return ExternalFeedAuto, nil
	}
	if !format.Valid() {
		return "", fmt.Errorf("unknown feed format %q (want rss, atom, json or auto)", raw)
	}
	return format, nil
}

// NormalizeExternalSubscriptionURL validates that a feed URL is an absolute
// http(s) URL. SSRF protection happens at request time; this only rejects
// values that cannot be fetched at all.
func NormalizeExternalSubscriptionURL(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fmt.Errorf("feed_url is required")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("feed_url must be an absolute URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("feed_url must use http or https")
	}
	return nil
}

// NormalizeExternalSubscriptionInput validates and canonicalizes a feed input.
func NormalizeExternalSubscriptionInput(
	input ExternalSubscriptionFeedInput, create bool,
) (ExternalSubscriptionFeedInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	if create && input.Name == "" {
		return input, fmt.Errorf("name is required")
	}
	input.FeedURL = strings.TrimSpace(input.FeedURL)
	if input.FeedURL != "" {
		if err := NormalizeExternalSubscriptionURL(input.FeedURL); err != nil {
			return input, err
		}
	}
	format, err := NormalizeExternalFeedFormat(input.Format)
	if err != nil {
		return input, err
	}
	input.Format = string(format)
	input.DomainTags = NormalizeTagNames(input.DomainTags)
	input.KnowledgeBaseIDs = trimDedupStrings(input.KnowledgeBaseIDs)
	if input.CheckIntervalHours == 0 {
		input.CheckIntervalHours = DefaultExternalFeedIntervalHours
	}
	if input.CheckIntervalHours < MinExternalFeedIntervalHours ||
		input.CheckIntervalHours > MaxExternalFeedIntervalHours {
		return input, fmt.Errorf("check_interval_hours must be between %d and %d",
			MinExternalFeedIntervalHours, MaxExternalFeedIntervalHours)
	}
	return input, nil
}

// NormalizeTagNames trims and dedupes tag names, preserving order and case.
func NormalizeTagNames(names []string) []string {
	out := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}
		// 标签匹配是大小写不敏感的，去重也按小写判断，避免同一领域写两遍。
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

// trimDedupStrings trims and dedupes non-empty strings preserving order.
func trimDedupStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
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

// ExternalFeedDomainTags decodes the domain tag list, tolerating a bad payload
// (a corrupt column must not break the whole signal sweep).
func (f *ExternalSubscriptionFeed) ExternalFeedDomainTags() []string {
	decoded := decodeStringList(f.DomainTags)
	if decoded == nil {
		return nil
	}
	return NormalizeTagNames(decoded)
}

// ExternalFeedKnowledgeBaseIDs decodes the knowledge-base scope.
func (f *ExternalSubscriptionFeed) ExternalFeedKnowledgeBaseIDs() []string {
	return decodeStringList(f.KnowledgeBaseIDs)
}

// Due reports whether the scheduler should check the feed now.
func (f *ExternalSubscriptionFeed) Due(now time.Time) bool {
	if f == nil || !f.Enabled {
		return false
	}
	if f.LastCheckedAt == nil {
		return true
	}
	interval := time.Duration(f.CheckIntervalHours) * time.Hour
	if interval <= 0 {
		interval = time.Duration(DefaultExternalFeedIntervalHours) * time.Hour
	}
	return !now.Before(f.LastCheckedAt.Add(interval))
}

// decodeStringList parses a JSON string array; nil/empty/malformed yields nil.
func decodeStringList(raw JSON) []string {
	if len(raw) == 0 {
		return nil
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil
	}
	return trimDedupStrings(values)
}

// ExternalSubscriptionMatchedKnowledge is one knowledge item matched to a feed
// by domain tag.
type ExternalSubscriptionMatchedKnowledge struct {
	// KnowledgeID is the matched document.
	KnowledgeID string
	// KnowledgeBaseID is its knowledge base.
	KnowledgeBaseID string
	// Title is used in the signal summary so the triage queue is readable.
	Title string
	// TagName is the domain tag that caused the match (audit trail).
	TagName string
}
