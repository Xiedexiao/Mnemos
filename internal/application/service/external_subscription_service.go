package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"

	"github.com/Tencent/WeKnora/internal/application/repository"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/Tencent/WeKnora/internal/utils"
)

// 外部订阅采集（KnowAxis V3.0 §7.3）。
//
// 采集链路的每一步都能单独解释：
//
//	抓取 → 解析 → 逐条算指纹（已见过就跳过）→ 按领域标签匹配知识 →
//	每条匹配知识落一个 external_subscription 信号 → 记录本次结果
//
// 关键取舍：
//   - 同一标准被源站重新发布（新链接、同正文）不应重复建单，所以去重键是
//     正文指纹而不是条目 ID/URL。
//   - 匹配不到知识不是错误，但要落一条 domain 级信号：这通常意味着领域
//     标签配错了，或者该领域确实还没有知识（覆盖缺口），两者都需要人看。
//   - 单次同步处理条目数有上限：首次订阅一个多年历史的法规库不该瞬间把
//     信号队列刷满。

// 外部订阅运行参数。
const (
	// externalFeedRequestTimeout bounds one feed fetch.
	externalFeedRequestTimeout = 20 * time.Second
	// externalFeedMaxSize caps one response so a broken endpoint cannot
	// exhaust memory.
	externalFeedMaxSize = 8 * 1024 * 1024
	// externalFeedSyncTimeout bounds one feed check inside a sweep.
	externalFeedSyncTimeout = 2 * time.Minute
	// externalSubscriptionActor is the audit actor for scheduler-driven checks.
	externalSubscriptionActor = "system"
	// externalSignalPayloadSummaryLimit keeps one entry summary from bloating
	// every signal's payload.
	externalSignalPayloadSummaryLimit = 1000
)

// externalSubscriptionService implements
// interfaces.ExternalSubscriptionService.
type externalSubscriptionService struct {
	repo     interfaces.ExternalSubscriptionRepository
	signals  interfaces.ChangeSignalService
	httpClnt *http.Client
	now      func() time.Time
}

// NewExternalSubscriptionService constructs the service.
func NewExternalSubscriptionService(
	repo interfaces.ExternalSubscriptionRepository,
	signals interfaces.ChangeSignalService,
) interfaces.ExternalSubscriptionService {
	cfg := utils.DefaultSSRFSafeHTTPClientConfig()
	cfg.Timeout = externalFeedRequestTimeout
	return &externalSubscriptionService{
		repo:     repo,
		signals:  signals,
		httpClnt: utils.NewSSRFSafeHTTPClient(cfg),
		now:      time.Now,
	}
}

// externalFeedClient returns the HTTP client, with a bounded timeout.
//
// 走 SSRF-safe 客户端：订阅 URL 是管理员填的，但"管理员填的"不等于"可以
// 访问内网"——被钓鱼的管理员账号一样能把平台变成内网探测器。
func (s *externalSubscriptionService) externalFeedClient() *http.Client {
	if s.httpClnt != nil {
		return s.httpClnt
	}
	cfg := utils.DefaultSSRFSafeHTTPClientConfig()
	cfg.Timeout = externalFeedRequestTimeout
	return utils.NewSSRFSafeHTTPClient(cfg)
}

// CreateFeed subscribes a feed.
func (s *externalSubscriptionService) CreateFeed(
	ctx context.Context, tenantID uint64, actorID string,
	input types.ExternalSubscriptionFeedInput,
) (*types.ExternalSubscriptionFeed, error) {
	if tenantID == 0 {
		return nil, apperrors.NewUnauthorizedError("tenant context is required")
	}
	if strings.TrimSpace(input.FeedURL) == "" {
		return nil, apperrors.NewBadRequestError("feed_url is required")
	}
	normalized, err := types.NormalizeExternalSubscriptionInput(input, true)
	if err != nil {
		return nil, apperrors.NewBadRequestError(err.Error())
	}
	encrypted, err := encryptExternalAuthHeader(normalized.AuthHeader)
	if err != nil {
		return nil, apperrors.NewBadRequestError(err.Error())
	}
	feed := &types.ExternalSubscriptionFeed{
		TenantID:           tenantID,
		Name:               normalized.Name,
		FeedURL:            normalized.FeedURL,
		Format:             normalized.Format,
		DomainTags:         mustJSONList(normalized.DomainTags),
		KnowledgeBaseIDs:   mustJSONList(normalized.KnowledgeBaseIDs),
		Enabled:            true,
		CheckIntervalHours: normalized.CheckIntervalHours,
		AuthHeaderEnc:      encrypted,
		CreatedBy:          actorID,
	}
	if input.Enabled != nil {
		feed.Enabled = *input.Enabled
	}
	if err := s.repo.CreateFeed(ctx, feed); err != nil {
		return nil, err
	}
	feed.HasAuthHeader = feed.AuthHeaderEnc != ""
	return feed, nil
}

// ListFeeds returns the tenant's subscriptions.
func (s *externalSubscriptionService) ListFeeds(
	ctx context.Context, tenantID uint64, enabledOnly bool,
) ([]*types.ExternalSubscriptionFeed, error) {
	feeds, err := s.repo.ListFeeds(ctx, tenantID, enabledOnly)
	if err != nil {
		return nil, err
	}
	for _, feed := range feeds {
		feed.HasAuthHeader = feed.AuthHeaderEnc != ""
	}
	return feeds, nil
}

// UpdateFeed edits a subscription.
//
// 空字段表示"不改"，而不是"清空"：订阅配置是长表单，只提交改动的字段更
// 不容易误删。要清空领域标签/范围就显式传 []；要清空认证头传 "-"。
func (s *externalSubscriptionService) UpdateFeed(
	ctx context.Context, tenantID uint64, feedID string,
	input types.ExternalSubscriptionFeedInput,
) (*types.ExternalSubscriptionFeed, error) {
	feed, err := s.repo.GetFeed(ctx, tenantID, feedID)
	if err != nil {
		return nil, mapExternalFeedError(err)
	}
	if strings.TrimSpace(input.Name) != "" {
		feed.Name = strings.TrimSpace(input.Name)
	}
	if strings.TrimSpace(input.FeedURL) != "" {
		feed.FeedURL = strings.TrimSpace(input.FeedURL)
	}
	if strings.TrimSpace(input.Format) != "" {
		format, formatErr := types.NormalizeExternalFeedFormat(input.Format)
		if formatErr != nil {
			return nil, apperrors.NewBadRequestError(formatErr.Error())
		}
		feed.Format = string(format)
	}
	if input.DomainTags != nil {
		feed.DomainTags = mustJSONList(types.NormalizeTagNames(input.DomainTags))
	}
	if input.KnowledgeBaseIDs != nil {
		feed.KnowledgeBaseIDs = mustJSONList(trimDedupeStrings(input.KnowledgeBaseIDs))
	}
	if input.Enabled != nil {
		feed.Enabled = *input.Enabled
	}
	if input.CheckIntervalHours != 0 {
		if input.CheckIntervalHours < types.MinExternalFeedIntervalHours ||
			input.CheckIntervalHours > types.MaxExternalFeedIntervalHours {
			return nil, apperrors.NewBadRequestError(fmt.Sprintf(
				"check_interval_hours must be between %d and %d",
				types.MinExternalFeedIntervalHours, types.MaxExternalFeedIntervalHours))
		}
		feed.CheckIntervalHours = input.CheckIntervalHours
	}
	switch {
	case strings.TrimSpace(input.AuthHeader) == "-":
		feed.AuthHeaderEnc = ""
	case strings.TrimSpace(input.AuthHeader) != "":
		encrypted, encErr := encryptExternalAuthHeader(input.AuthHeader)
		if encErr != nil {
			return nil, apperrors.NewBadRequestError(encErr.Error())
		}
		feed.AuthHeaderEnc = encrypted
	}
	if err := types.NormalizeExternalSubscriptionURL(feed.FeedURL); err != nil {
		return nil, apperrors.NewBadRequestError(err.Error())
	}
	if err := s.repo.UpdateFeed(ctx, feed); err != nil {
		return nil, mapExternalFeedError(err)
	}
	feed.HasAuthHeader = feed.AuthHeaderEnc != ""
	return feed, nil
}

// DeleteFeed unsubscribes and clears the entry history.
func (s *externalSubscriptionService) DeleteFeed(
	ctx context.Context, tenantID uint64, feedID string,
) error {
	if err := s.repo.DeleteFeed(ctx, tenantID, feedID); err != nil {
		return mapExternalFeedError(err)
	}
	return nil
}

// ListEntries returns the newest seen entries.
func (s *externalSubscriptionService) ListEntries(
	ctx context.Context, tenantID uint64, feedID string, limit int,
) ([]*types.ExternalSubscriptionEntry, error) {
	return s.repo.ListEntries(ctx, tenantID, feedID, limit)
}

// SyncFeed checks one feed and records the resulting signals.
func (s *externalSubscriptionService) SyncFeed(
	ctx context.Context, tenantID uint64, feedID string,
) (*types.ExternalSubscriptionSyncResult, error) {
	feed, err := s.repo.GetFeed(ctx, tenantID, feedID)
	if err != nil {
		return nil, mapExternalFeedError(err)
	}
	result, syncErr := s.syncFeed(ctx, feed)
	checkedAt := s.now().UTC()
	if markErr := s.repo.MarkFeedChecked(ctx, tenantID, feed.ID, checkedAt, syncErr); markErr != nil {
		logger.Warnf(ctx, "[external-subscription] mark feed %s checked: %v", feed.ID, markErr)
	}
	if syncErr != nil {
		return nil, syncErr
	}
	result.SyncedAt = checkedAt
	return result, nil
}

// SyncDue checks every due feed.
func (s *externalSubscriptionService) SyncDue(
	ctx context.Context,
) (*types.ExternalSubscriptionSweepResult, error) {
	now := s.now().UTC()
	feeds, err := s.repo.ListDueFeeds(ctx, now, 0)
	if err != nil {
		return nil, err
	}
	result := &types.ExternalSubscriptionSweepResult{CheckedAt: now}
	for _, feed := range feeds {
		result.FeedsChecked++
		feedCtx, cancel := context.WithTimeout(ctx, externalFeedSyncTimeout)
		syncResult, syncErr := s.syncFeed(feedCtx, feed)
		cancel()
		if syncErr != nil {
			result.FeedsFailed++
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", feed.ID, syncErr))
			if markErr := s.repo.MarkFeedChecked(ctx, feed.TenantID, feed.ID, now, syncErr); markErr != nil {
				logger.Warnf(ctx, "[external-subscription] mark feed %s checked: %v", feed.ID, markErr)
			}
			continue
		}
		result.NewEntries += syncResult.NewEntries
		result.Signals += syncResult.Signals
		if markErr := s.repo.MarkFeedChecked(ctx, feed.TenantID, feed.ID, now, nil); markErr != nil {
			logger.Warnf(ctx, "[external-subscription] mark feed %s checked: %v", feed.ID, markErr)
		}
	}
	return result, nil
}

// syncFeed performs one check. The caller records the check outcome.
func (s *externalSubscriptionService) syncFeed(
	ctx context.Context, feed *types.ExternalSubscriptionFeed,
) (*types.ExternalSubscriptionSyncResult, error) {
	parsed, err := s.fetchExternalFeed(ctx, feed)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", feed.Name, err)
	}
	result := &types.ExternalSubscriptionSyncResult{FeedID: feed.ID, Fetched: len(parsed.Items)}
	if len(parsed.Items) > types.ExternalSubscriptionMaxEntriesPerSync {
		result.Truncated = true
	}
	items := parsed.Items
	if len(items) > types.ExternalSubscriptionMaxEntriesPerSync {
		items = items[:types.ExternalSubscriptionMaxEntriesPerSync]
	}

	domainTags := feed.ExternalFeedDomainTags()
	kbIDs := feed.ExternalFeedKnowledgeBaseIDs()

	for _, item := range items {
		title := strings.TrimSpace(item.Title)
		if title == "" {
			title = strings.TrimSpace(item.Link)
		}
		if title == "" {
			continue
		}
		entry := buildExternalEntry(feed, item, title)
		created, recordErr := s.repo.RecordEntry(ctx, entry)
		if recordErr != nil {
			return nil, fmt.Errorf("record entry %q: %w", title, recordErr)
		}
		if !created {
			continue
		}
		result.NewEntries++

		matched, matchErr := s.repo.MatchKnowledgeByTags(ctx, feed.TenantID, domainTags, kbIDs)
		if matchErr != nil {
			// 回滚去重记录，下一轮重试，而不是把这条永久标记为已处理。
			if delErr := s.repo.DeleteEntry(ctx, entry.ID); delErr != nil {
				logger.Warnf(ctx, "[external-subscription] rollback entry %s: %v", entry.ID, delErr)
			}
			return nil, fmt.Errorf("match knowledge for %q: %w", title, matchErr)
		}
		result.MatchedKnowledge += len(matched)

		signalCount, signalErr := s.recordExternalSignals(ctx, feed, entry, matched, domainTags)
		if signalErr != nil {
			if delErr := s.repo.DeleteEntry(ctx, entry.ID); delErr != nil {
				logger.Warnf(ctx, "[external-subscription] rollback entry %s: %v", entry.ID, delErr)
			}
			return nil, fmt.Errorf("record signals for %q: %w", title, signalErr)
		}
		result.Signals += signalCount
		if len(matched) == 0 {
			result.UnmatchedEntries++
		}
		if err := s.repo.UpdateEntryOutcome(ctx, entry.ID, len(matched), signalCount); err != nil {
			logger.Warnf(ctx, "[external-subscription] update entry %s: %v", entry.ID, err)
		}
	}
	return result, nil
}

// recordExternalSignals turns one entry into signals.
//
// 每条匹配到的知识一个信号：合规复审是按文档举证的动作，合成一个"影响
// N 篇"的信号没法逐篇关闭。没有匹配时落一条领域级信号，把覆盖缺口显式
// 暴露出来，而不是静默丢弃。
func (s *externalSubscriptionService) recordExternalSignals(
	ctx context.Context,
	feed *types.ExternalSubscriptionFeed,
	entry *types.ExternalSubscriptionEntry,
	matched []types.ExternalSubscriptionMatchedKnowledge,
	domainTags []string,
) (int, error) {
	summary := fmt.Sprintf("【%s】%s", feed.Name, entry.Title)
	payload := externalSignalPayload(feed, entry, domainTags, "")
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}

	count := 0
	if len(matched) == 0 {
		if _, collectErr := s.signals.Collect(ctx, &types.ChangeSignal{
			TenantID:      feed.TenantID,
			SignalType:    string(types.ChangeSignalExternalSubscription),
			SourceID:      externalSignalSourceID(feed.ID, entry),
			Summary:       summary + "（未匹配到领域知识，请确认领域标签或覆盖缺口）",
			Confidence:    "low",
			DefaultAction: string(types.ChangeSignalActionNotify),
			Payload:       types.JSON(payloadJSON),
		}); collectErr != nil {
			return 0, collectErr
		}
		return 1, nil
	}

	for _, knowledge := range matched {
		knowledgePayload := externalSignalPayload(feed, entry, domainTags, knowledge.TagName)
		raw, marshalErr := json.Marshal(knowledgePayload)
		if marshalErr != nil {
			return count, marshalErr
		}
		if _, collectErr := s.signals.Collect(ctx, &types.ChangeSignal{
			TenantID:        feed.TenantID,
			SignalType:      string(types.ChangeSignalExternalSubscription),
			SourceID:        externalSignalSourceID(feed.ID, entry),
			KnowledgeID:     knowledge.KnowledgeID,
			KnowledgeBaseID: knowledge.KnowledgeBaseID,
			Summary:         summary,
			Confidence:      "high",
			Payload:         types.JSON(raw),
		}); collectErr != nil {
			return count, collectErr
		}
		count++
	}
	return count, nil
}

// fetchExternalFeed downloads and parses one feed.
func (s *externalSubscriptionService) fetchExternalFeed(
	ctx context.Context, feed *types.ExternalSubscriptionFeed,
) (*gofeed.Feed, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, feed.FeedURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/feed+json, application/xml, text/xml, */*")
	request.Header.Set("User-Agent", "KnowAxis-ExternalSubscription/1.0")
	if err := s.applyAuthHeader(request, feed); err != nil {
		return nil, err
	}
	client := s.externalFeedClient()
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("feed returned http %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, externalFeedMaxSize))
	if err != nil {
		return nil, err
	}
	parsed, err := gofeed.NewParser().ParseString(string(body))
	if err != nil {
		return nil, fmt.Errorf("parse feed: %w", err)
	}
	return parsed, nil
}

// applyAuthHeader attaches the stored secret header.
func (s *externalSubscriptionService) applyAuthHeader(
	request *http.Request, feed *types.ExternalSubscriptionFeed,
) error {
	if feed.AuthHeaderEnc == "" {
		return nil
	}
	decrypted, err := utils.DecryptStoredSecret(feed.AuthHeaderEnc)
	if err != nil {
		return fmt.Errorf("decrypt auth header: %w", err)
	}
	name, value, parseErr := splitExternalAuthHeader(decrypted)
	if parseErr != nil {
		return parseErr
	}
	request.Header.Set(name, value)
	return nil
}

// buildExternalEntry maps one feed item onto a dedupe record.
func buildExternalEntry(
	feed *types.ExternalSubscriptionFeed, item *gofeed.Item, title string,
) *types.ExternalSubscriptionEntry {
	externalID := strings.TrimSpace(item.GUID)
	if externalID == "" {
		externalID = strings.TrimSpace(item.Link)
	}
	body := item.Content
	if body == "" {
		body = item.Description
	}
	fingerprint := externalEntryFingerprint(title, body, item.Link)
	if externalID == "" {
		externalID = fingerprint[:32]
	}
	entry := &types.ExternalSubscriptionEntry{
		TenantID:    feed.TenantID,
		FeedID:      feed.ID,
		ExternalID:  externalID,
		Title:       truncateRunes(title, 512),
		URL:         strings.TrimSpace(item.Link),
		Fingerprint: fingerprint,
	}
	if published := externalEntryPublished(item); published != nil {
		entry.PublishedAt = published
	}
	return entry
}

// externalEntryPublished prefers the feed's updated time over its published
// time: a standards body that revises an item usually bumps updated.
func externalEntryPublished(item *gofeed.Item) *time.Time {
	if item == nil {
		return nil
	}
	if item.UpdatedParsed != nil {
		value := item.UpdatedParsed.UTC()
		return &value
	}
	if item.PublishedParsed != nil {
		value := item.PublishedParsed.UTC()
		return &value
	}
	return nil
}

// externalEntryFingerprint is the content identity used for dedupe.
//
// 只算标题与正文，不算 URL/GUID：监管站点经常给同一条标准换一个链接重新
// 发布，按链接去重会重复建单。正文为空时退回标题+链接，避免把两个同名的
// 空正文条目当成一条。
func externalEntryFingerprint(title, body, link string) string {
	title = strings.TrimSpace(title)
	body = strings.TrimSpace(body)
	canonical := title + "\x00" + body
	if body == "" {
		canonical = title + "\x00" + strings.TrimSpace(link)
	}
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

// externalSignalSourceID identifies the feed entry in the signal queue.
func externalSignalSourceID(feedID string, entry *types.ExternalSubscriptionEntry) string {
	return "extsub:" + feedID + ":" + entry.ExternalID
}

// externalSignalPayload is the audit trail attached to every signal.
func externalSignalPayload(
	feed *types.ExternalSubscriptionFeed,
	entry *types.ExternalSubscriptionEntry,
	domainTags []string,
	matchedTag string,
) map[string]interface{} {
	payload := map[string]interface{}{
		"feed_id":      feed.ID,
		"feed_name":    feed.Name,
		"feed_url":     feed.FeedURL,
		"entry_title":  entry.Title,
		"entry_url":    entry.URL,
		"external_id":  entry.ExternalID,
		"fingerprint":  entry.Fingerprint,
		"domain_tags":  domainTags,
		"matched_tag":  matchedTag,
		"collected_by": "external_subscription",
	}
	if entry.PublishedAt != nil {
		payload["published_at"] = entry.PublishedAt.UTC().Format(time.RFC3339)
	}
	return payload
}

// encryptExternalAuthHeader validates and encrypts an optional header.
func encryptExternalAuthHeader(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	if _, _, err := splitExternalAuthHeader(trimmed); err != nil {
		return "", err
	}
	// 没有密钥时宁可拒绝，也不要把 token 明文落库：订阅源认证头通常是长期
	// 有效的 API key，明文入库等于把它写进备份与日志。
	key := utils.GetAESKey()
	if key == nil {
		return "", fmt.Errorf(
			"SYSTEM_AES_KEY is not configured; refusing to store auth_header in the clear")
	}
	return utils.EncryptAESGCM(trimmed, key)
}

// splitExternalAuthHeader parses "Name: Value".
func splitExternalAuthHeader(raw string) (name, value string, err error) {
	index := strings.Index(raw, ":")
	if index <= 0 {
		return "", "", fmt.Errorf("auth_header must be in \"Name: Value\" form")
	}
	name = strings.TrimSpace(raw[:index])
	value = strings.TrimSpace(raw[index+1:])
	if name == "" || value == "" {
		return "", "", fmt.Errorf("auth_header must be in \"Name: Value\" form")
	}
	return name, value, nil
}

// mustJSONList encodes a string list as JSON, never failing on a valid list.
func mustJSONList(values []string) types.JSON {
	if values == nil {
		values = []string{}
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return types.JSON("[]")
	}
	return types.JSON(raw)
}

// trimDedupeStrings trims and dedupes non-empty strings.
func trimDedupeStrings(values []string) []string {
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

// mapExternalFeedError maps repository misses onto the API 404.
func mapExternalFeedError(err error) error {
	if errors.Is(err, repository.ErrExternalSubscriptionNotFound) {
		return apperrors.NewNotFoundError("subscription feed not found")
	}
	return err
}
