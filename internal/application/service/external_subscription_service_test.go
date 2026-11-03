package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// stubExternalSubscriptionRepo is an in-memory feed store.
type stubExternalSubscriptionRepo struct {
	interfaces.ExternalSubscriptionRepository

	mu       sync.Mutex
	feeds    map[string]*types.ExternalSubscriptionFeed
	entries  []*types.ExternalSubscriptionEntry
	matches  map[string][]types.ExternalSubscriptionMatchedKnowledge
	dueFeeds []*types.ExternalSubscriptionFeed

	recordedEntries int
	checkedAt       time.Time
	checkErr        error
}

func newStubExternalRepo() *stubExternalSubscriptionRepo {
	return &stubExternalSubscriptionRepo{
		feeds:   map[string]*types.ExternalSubscriptionFeed{},
		matches: map[string][]types.ExternalSubscriptionMatchedKnowledge{},
	}
}

func (s *stubExternalSubscriptionRepo) CreateFeed(
	_ context.Context, feed *types.ExternalSubscriptionFeed,
) error {
	if feed.ID == "" {
		feed.ID = "feed-" + itoaSmall(len(s.feeds)+1)
	}
	s.feeds[feed.ID] = feed
	return nil
}

func (s *stubExternalSubscriptionRepo) GetFeed(
	_ context.Context, tenantID uint64, feedID string,
) (*types.ExternalSubscriptionFeed, error) {
	feed, ok := s.feeds[feedID]
	if !ok || feed.TenantID != tenantID {
		return nil, errors.New("feed not found")
	}
	return feed, nil
}

func (s *stubExternalSubscriptionRepo) ListFeeds(
	_ context.Context, _ uint64, enabledOnly bool,
) ([]*types.ExternalSubscriptionFeed, error) {
	out := make([]*types.ExternalSubscriptionFeed, 0, len(s.feeds))
	for _, feed := range s.feeds {
		if enabledOnly && !feed.Enabled {
			continue
		}
		out = append(out, feed)
	}
	return out, nil
}

func (s *stubExternalSubscriptionRepo) ListDueFeeds(
	_ context.Context, _ time.Time, _ int,
) ([]*types.ExternalSubscriptionFeed, error) {
	return s.dueFeeds, nil
}

func (s *stubExternalSubscriptionRepo) UpdateFeed(
	_ context.Context, feed *types.ExternalSubscriptionFeed,
) error {
	s.feeds[feed.ID] = feed
	return nil
}

func (s *stubExternalSubscriptionRepo) DeleteFeed(
	_ context.Context, _ uint64, feedID string,
) error {
	delete(s.feeds, feedID)
	return nil
}

func (s *stubExternalSubscriptionRepo) RecordEntry(
	_ context.Context, entry *types.ExternalSubscriptionEntry,
) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.entries {
		if existing.FeedID == entry.FeedID && existing.Fingerprint == entry.Fingerprint {
			return false, nil
		}
	}
	if entry.ID == "" {
		entry.ID = "entry-" + itoaSmall(len(s.entries)+1)
	}
	s.entries = append(s.entries, entry)
	s.recordedEntries++
	return true, nil
}

func (s *stubExternalSubscriptionRepo) UpdateEntryOutcome(
	_ context.Context, entryID string, matched, signals int,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entry := range s.entries {
		if entry.ID == entryID {
			entry.MatchedKnowledgeCount = matched
			entry.SignalCount = signals
		}
	}
	return nil
}

func (s *stubExternalSubscriptionRepo) DeleteEntry(
	_ context.Context, entryID string,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.entries[:0]
	for _, entry := range s.entries {
		if entry.ID != entryID {
			kept = append(kept, entry)
		}
	}
	s.entries = kept
	s.recordedEntries--
	return nil
}

func (s *stubExternalSubscriptionRepo) ListEntries(
	_ context.Context, _ uint64, _ string, _ int,
) ([]*types.ExternalSubscriptionEntry, error) {
	return s.entries, nil
}

func (s *stubExternalSubscriptionRepo) MarkFeedChecked(
	_ context.Context, _ uint64, _ string, checkedAt time.Time, syncErr error,
) error {
	s.checkedAt = checkedAt
	s.checkErr = syncErr
	return nil
}

func (s *stubExternalSubscriptionRepo) MatchKnowledgeByTags(
	_ context.Context, _ uint64, tagNames []string, _ []string,
) ([]types.ExternalSubscriptionMatchedKnowledge, error) {
	seen := map[string]bool{}
	var out []types.ExternalSubscriptionMatchedKnowledge
	for _, name := range tagNames {
		for _, match := range s.matches[strings.ToLower(name)] {
			if seen[match.KnowledgeID] {
				continue
			}
			seen[match.KnowledgeID] = true
			out = append(out, match)
		}
	}
	return out, nil
}

// stubExternalSignalService records collected signals.
type stubExternalSignalService struct {
	interfaces.ChangeSignalService
	signals []*types.ChangeSignal
	err     error
}

func (s *stubExternalSignalService) Collect(
	_ context.Context, signal *types.ChangeSignal,
) (*types.ChangeSignal, error) {
	if s.err != nil {
		return nil, s.err
	}
	s.signals = append(s.signals, signal)
	return signal, nil
}

func externalTestFeed(serverURL string) *types.ExternalSubscriptionFeed {
	return &types.ExternalSubscriptionFeed{
		ID:                 "feed-1",
		TenantID:           7,
		Name:               "国标全文公开系统",
		FeedURL:            serverURL + "/feed.xml",
		Format:             "rss",
		DomainTags:         types.JSON(`["安全法规"]`),
		KnowledgeBaseIDs:   types.JSON(`[]`),
		Enabled:            true,
		CheckIntervalHours: 24,
	}
}

func externalTestServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

const externalTestRSS = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel>
  <title>标准更新</title>
  <item>
    <guid>GB-50316-2026</guid>
    <title>GB 50316 工业金属管道设计规范（2026 修订）</title>
    <link>https://example.com/gb-50316</link>
    <description>管道等级与材料选用条款更新。</description>
    <pubDate>Mon, 02 Nov 2026 08:00:00 GMT</pubDate>
  </item>
</channel></rss>`

func newExternalTestService(
	repo *stubExternalSubscriptionRepo, signals *stubExternalSignalService,
	server *httptest.Server,
) *externalSubscriptionService {
	return &externalSubscriptionService{
		repo:     repo,
		signals:  signals,
		httpClnt: server.Client(),
		now:      func() time.Time { return time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC) },
	}
}

// TestSyncCreatesOneSignalPerMatchedKnowledge is the §7.3 contract: an
// external update is matched onto knowledge by domain, and each matched
// document gets its own review-able signal.
func TestSyncCreatesOneSignalPerMatchedKnowledge(t *testing.T) {
	server := externalTestServer(t, http.StatusOK, externalTestRSS)
	repo := newStubExternalRepo()
	signals := &stubExternalSignalService{}
	feed := externalTestFeed(server.URL)
	repo.feeds[feed.ID] = feed
	repo.matches["安全法规"] = []types.ExternalSubscriptionMatchedKnowledge{
		{KnowledgeID: "k1", KnowledgeBaseID: "kb1", Title: "管道设计 SOP", TagName: "安全法规"},
		{KnowledgeID: "k2", KnowledgeBaseID: "kb1", Title: "压力试验规程", TagName: "安全法规"},
	}
	svc := newExternalTestService(repo, signals, server)

	result, err := svc.SyncFeed(context.Background(), 7, feed.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Fetched)
	assert.Equal(t, 1, result.NewEntries)
	assert.Equal(t, 2, result.MatchedKnowledge)
	assert.Equal(t, 2, result.Signals)
	assert.Zero(t, result.UnmatchedEntries)

	require.Len(t, signals.signals, 2)
	assert.Equal(t, "k1", signals.signals[0].KnowledgeID)
	assert.Equal(t, "k2", signals.signals[1].KnowledgeID)
	for _, signal := range signals.signals {
		assert.Equal(t, string(types.ChangeSignalExternalSubscription), signal.SignalType)
		assert.Equal(t, "high", signal.Confidence)
		assert.Contains(t, signal.Summary, "GB 50316")
		assert.Equal(t, "安全法规", signalPayloadValue(signal, "matched_tag"))
	}
	require.NoError(t, repo.checkErr, "a successful sync clears the feed error")
	assert.False(t, repo.checkedAt.IsZero())
}

// TestSyncDedupesRepublishedEntry is the noise guard: a standards body that
// republishes the same item (even at a new URL) must not re-open the review.
func TestSyncDedupesRepublishedEntry(t *testing.T) {
	server := externalTestServer(t, http.StatusOK, externalTestRSS)
	repo := newStubExternalRepo()
	signals := &stubExternalSignalService{}
	feed := externalTestFeed(server.URL)
	repo.feeds[feed.ID] = feed
	repo.matches["安全法规"] = []types.ExternalSubscriptionMatchedKnowledge{
		{KnowledgeID: "k1", KnowledgeBaseID: "kb1", Title: "管道设计 SOP", TagName: "安全法规"},
	}
	svc := newExternalTestService(repo, signals, server)

	_, err := svc.SyncFeed(context.Background(), 7, feed.ID)
	require.NoError(t, err)
	require.Len(t, signals.signals, 1)

	// Same standard, new link only.
	republished := strings.Replace(externalTestRSS,
		"https://example.com/gb-50316", "https://example.com/gb-50316-v2", 1)
	server2 := externalTestServer(t, http.StatusOK, republished)
	feed.FeedURL = server2.URL + "/feed.xml"
	svc.httpClnt = server2.Client()

	result, err := svc.SyncFeed(context.Background(), 7, feed.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Fetched)
	assert.Zero(t, result.NewEntries, "the same content is already recorded")
	assert.Zero(t, result.Signals)
	assert.Len(t, signals.signals, 1)
}

// TestSyncUnmatchedEntryRaisesCoverageSignal keeps a missing domain mapping
// visible instead of silently discarding the standard update.
func TestSyncUnmatchedEntryRaisesCoverageSignal(t *testing.T) {
	server := externalTestServer(t, http.StatusOK, externalTestRSS)
	repo := newStubExternalRepo()
	signals := &stubExternalSignalService{}
	feed := externalTestFeed(server.URL)
	repo.feeds[feed.ID] = feed
	// No knowledge carries the tag.
	svc := newExternalTestService(repo, signals, server)

	result, err := svc.SyncFeed(context.Background(), 7, feed.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, result.UnmatchedEntries)
	assert.Equal(t, 1, result.Signals)

	require.Len(t, signals.signals, 1)
	signal := signals.signals[0]
	assert.Empty(t, signal.KnowledgeID)
	assert.Equal(t, "low", signal.Confidence)
	assert.Equal(t, string(types.ChangeSignalActionNotify), signal.DefaultAction)
	assert.Contains(t, signal.Summary, "未匹配到领域知识")
}

// TestSyncRollsBackEntryWhenSignalFails is the retry guarantee: an entry is not
// remembered as handled if the signals it should have produced were lost.
func TestSyncRollsBackEntryWhenSignalFails(t *testing.T) {
	server := externalTestServer(t, http.StatusOK, externalTestRSS)
	repo := newStubExternalRepo()
	signals := &stubExternalSignalService{err: errors.New("db down")}
	feed := externalTestFeed(server.URL)
	repo.feeds[feed.ID] = feed
	repo.matches["安全法规"] = []types.ExternalSubscriptionMatchedKnowledge{
		{KnowledgeID: "k1", KnowledgeBaseID: "kb1", Title: "管道设计 SOP", TagName: "安全法规"},
	}
	svc := newExternalTestService(repo, signals, server)

	_, err := svc.SyncFeed(context.Background(), 7, feed.ID)
	require.Error(t, err)
	assert.Empty(t, repo.entries, "the dedupe claim must be rolled back")
	require.Error(t, repo.checkErr, "the failure is recorded on the feed")

	// The next check retries and succeeds.
	signals.err = nil
	result, err := svc.SyncFeed(context.Background(), 7, feed.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, result.NewEntries)
	assert.Len(t, signals.signals, 1)
}

// TestSyncFetchFailureLeavesFeedMarked keeps a broken feed diagnosable.
func TestSyncFetchFailureLeavesFeedMarked(t *testing.T) {
	server := externalTestServer(t, http.StatusInternalServerError, "nope")
	repo := newStubExternalRepo()
	feed := externalTestFeed(server.URL)
	repo.feeds[feed.ID] = feed
	svc := newExternalTestService(repo, &stubExternalSignalService{}, server)

	_, err := svc.SyncFeed(context.Background(), 7, feed.ID)
	require.Error(t, err)
	assert.Contains(t, repo.checkErr.Error(), "500")
}

// TestSyncDueReportsPerFeedFailures keeps one broken feed from stopping the
// sweep.
func TestSyncDueReportsPerFeedFailures(t *testing.T) {
	good := externalTestServer(t, http.StatusOK, externalTestRSS)
	bad := externalTestServer(t, http.StatusBadGateway, "")
	repo := newStubExternalRepo()
	signals := &stubExternalSignalService{}

	goodFeed := externalTestFeed(good.URL)
	goodFeed.ID = "feed-good"
	badFeed := externalTestFeed(bad.URL)
	badFeed.ID = "feed-bad"
	repo.feeds[goodFeed.ID] = goodFeed
	repo.feeds[badFeed.ID] = badFeed
	repo.matches["安全法规"] = []types.ExternalSubscriptionMatchedKnowledge{
		{KnowledgeID: "k1", KnowledgeBaseID: "kb1", Title: "管道设计 SOP", TagName: "安全法规"},
	}
	repo.dueFeeds = []*types.ExternalSubscriptionFeed{goodFeed, badFeed}

	svc := newExternalTestService(repo, signals, good)

	result, err := svc.SyncDue(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, result.FeedsChecked)
	assert.Equal(t, 1, result.FeedsFailed, "a failure is counted, not fatal")
	assert.Equal(t, 1, result.NewEntries)
	assert.Len(t, result.Errors, 1)
	assert.Contains(t, result.Errors[0], "feed-bad")
}

// TestSyncTruncatesLongHistory keeps a first subscription from flooding the
// signal queue.
func TestSyncTruncatesLongHistory(t *testing.T) {
	var builder strings.Builder
	builder.WriteString(`<?xml version="1.0"?><rss version="2.0"><channel>`)
	for i := 0; i < types.ExternalSubscriptionMaxEntriesPerSync+5; i++ {
		builder.WriteString(`<item><guid>std-`)
		builder.WriteString(itoaSmall(i))
		builder.WriteString(`</guid><title>标准 `)
		builder.WriteString(itoaSmall(i))
		builder.WriteString(`</title></item>`)
	}
	builder.WriteString(`</channel></rss>`)
	server := externalTestServer(t, http.StatusOK, builder.String())

	repo := newStubExternalRepo()
	feed := externalTestFeed(server.URL)
	repo.feeds[feed.ID] = feed
	svc := newExternalTestService(repo, &stubExternalSignalService{}, server)

	result, err := svc.SyncFeed(context.Background(), 7, feed.ID)
	require.NoError(t, err)
	assert.True(t, result.Truncated)
	assert.Equal(t, types.ExternalSubscriptionMaxEntriesPerSync, result.NewEntries)
}

// TestCreateFeedValidatesInput covers the configuration guard rails.
func TestCreateFeedValidatesInput(t *testing.T) {
	t.Setenv("SYSTEM_AES_KEY", "weknora-test-aes-key-32bytes!!!!")
	repo := newStubExternalRepo()
	svc := &externalSubscriptionService{repo: repo, signals: &stubExternalSignalService{}, now: time.Now}
	ctx := context.Background()

	_, err := svc.CreateFeed(ctx, 7, "admin", types.ExternalSubscriptionFeedInput{
		Name: "无地址",
	})
	require.Error(t, err, "feed_url is required")

	_, err = svc.CreateFeed(ctx, 7, "admin", types.ExternalSubscriptionFeedInput{
		Name: "内部地址", FeedURL: "http://127.0.0.1/feed.xml",
	})
	require.NoError(t, err, "SSRF is blocked at request time, not config time")

	_, err = svc.CreateFeed(ctx, 7, "admin", types.ExternalSubscriptionFeedInput{
		Name: "错误协议", FeedURL: "ftp://example.com/feed.xml",
	})
	require.Error(t, err)

	_, err = svc.CreateFeed(ctx, 7, "admin", types.ExternalSubscriptionFeedInput{
		Name: "过头周期", FeedURL: "https://example.com/feed.xml", CheckIntervalHours: 99999,
	})
	require.Error(t, err)

	created, err := svc.CreateFeed(ctx, 7, "admin", types.ExternalSubscriptionFeedInput{
		Name: "  国标  ", FeedURL: "https://example.com/feed.xml",
		DomainTags: []string{" 安全法规 ", "安全法规", "环保"},
		AuthHeader: "Authorization: Bearer token",
	})
	require.NoError(t, err)
	assert.Equal(t, "国标", created.Name)
	assert.True(t, created.Enabled)
	assert.Equal(t, types.DefaultExternalFeedIntervalHours, created.CheckIntervalHours)
	assert.Equal(t, []string{"安全法规", "环保"}, created.ExternalFeedDomainTags())
	assert.True(t, created.HasAuthHeader)
	assert.NotContains(t, created.AuthHeaderEnc, "Bearer token",
		"the secret must not be stored in the clear")
	assert.Contains(t, created.AuthHeaderEnc, "enc:v1:")
}

// TestCreateFeedRefusesPlaintextSecret is the fail-closed rule: without a key
// the platform must not write a long-lived token into the database in the clear.
func TestCreateFeedRefusesPlaintextSecret(t *testing.T) {
	t.Setenv("SYSTEM_AES_KEY", "")
	repo := newStubExternalRepo()
	svc := &externalSubscriptionService{repo: repo, signals: &stubExternalSignalService{}, now: time.Now}
	_, err := svc.CreateFeed(context.Background(), 7, "admin", types.ExternalSubscriptionFeedInput{
		Name: "付费标准库", FeedURL: "https://example.com/feed.xml",
		AuthHeader: "Authorization: Bearer secret",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SYSTEM_AES_KEY")
}

// TestUpdateFeedClearsOnlyExplicitFields pins the partial-update contract.
func TestUpdateFeedClearsOnlyExplicitFields(t *testing.T) {
	t.Setenv("SYSTEM_AES_KEY", "weknora-test-aes-key-32bytes!!!!")
	repo := newStubExternalRepo()
	svc := &externalSubscriptionService{repo: repo, signals: &stubExternalSignalService{}, now: time.Now}
	ctx := context.Background()
	created, err := svc.CreateFeed(ctx, 7, "admin", types.ExternalSubscriptionFeedInput{
		Name: "国标", FeedURL: "https://example.com/feed.xml",
		DomainTags: []string{"安全法规"}, AuthHeader: "X-Key: secret",
	})
	require.NoError(t, err)

	updated, err := svc.UpdateFeed(ctx, 7, created.ID, types.ExternalSubscriptionFeedInput{
		CheckIntervalHours: 6,
	})
	require.NoError(t, err)
	assert.Equal(t, "国标", updated.Name, "empty name means unchanged")
	assert.Equal(t, []string{"安全法规"}, updated.ExternalFeedDomainTags())
	assert.Equal(t, 6, updated.CheckIntervalHours)
	assert.True(t, updated.HasAuthHeader, "an omitted header means unchanged")

	cleared, err := svc.UpdateFeed(ctx, 7, created.ID, types.ExternalSubscriptionFeedInput{
		DomainTags: []string{},
		AuthHeader: "-",
	})
	require.NoError(t, err)
	assert.Empty(t, cleared.ExternalFeedDomainTags(), "an explicit [] clears the domain mapping")
	assert.False(t, cleared.HasAuthHeader)
}

// signalPayloadValue reads one string field out of a signal payload.
func signalPayloadValue(signal *types.ChangeSignal, key string) string {
	payload, err := signal.Payload.Map()
	if err != nil {
		return ""
	}
	value, _ := payload[key].(string)
	return value
}
