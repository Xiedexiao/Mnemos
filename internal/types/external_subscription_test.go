package types

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNormalizeExternalSubscriptionURL rejects values that cannot be fetched.
func TestNormalizeExternalSubscriptionURL(t *testing.T) {
	assert.NoError(t, NormalizeExternalSubscriptionURL("https://www.sac.gov.cn/feed.xml"))
	assert.NoError(t, NormalizeExternalSubscriptionURL("http://example.com/rss"))
	assert.Error(t, NormalizeExternalSubscriptionURL(""))
	assert.Error(t, NormalizeExternalSubscriptionURL("/relative/feed.xml"))
	assert.Error(t, NormalizeExternalSubscriptionURL("ftp://example.com/feed.xml"))
}

// TestNormalizeExternalSubscriptionInput covers defaults and bounds.
func TestNormalizeExternalSubscriptionInput(t *testing.T) {
	input, err := NormalizeExternalSubscriptionInput(ExternalSubscriptionFeedInput{
		Name:       "  国标  ",
		FeedURL:    "https://example.com/feed.xml",
		Format:     "RSS",
		DomainTags: []string{" 安全法规 ", "安全法规", "", "环保"},
	}, true)
	require.NoError(t, err)
	assert.Equal(t, "国标", input.Name)
	assert.Equal(t, "rss", input.Format)
	assert.Equal(t, DefaultExternalFeedIntervalHours, input.CheckIntervalHours)
	assert.Equal(t, []string{"安全法规", "环保"}, input.DomainTags)

	_, err = NormalizeExternalSubscriptionInput(ExternalSubscriptionFeedInput{
		CheckIntervalHours: -1,
	}, false)
	require.Error(t, err)

	_, err = NormalizeExternalSubscriptionInput(ExternalSubscriptionFeedInput{
		CheckIntervalHours: MaxExternalFeedIntervalHours + 1,
	}, false)
	require.Error(t, err)

	_, err = NormalizeExternalSubscriptionInput(ExternalSubscriptionFeedInput{
		Format: "carrier-pigeon",
	}, false)
	require.Error(t, err)

	// Create requires a name.
	_, err = NormalizeExternalSubscriptionInput(ExternalSubscriptionFeedInput{}, true)
	require.Error(t, err)
}

// TestExternalFeedDue covers the scheduler's due rule.
func TestExternalFeedDue(t *testing.T) {
	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	checked := now.Add(-2 * time.Hour)

	due := &ExternalSubscriptionFeed{Enabled: true, CheckIntervalHours: 1, LastCheckedAt: &checked}
	assert.True(t, due.Due(now))

	notDue := &ExternalSubscriptionFeed{Enabled: true, CheckIntervalHours: 24, LastCheckedAt: &checked}
	assert.False(t, notDue.Due(now))

	neverChecked := &ExternalSubscriptionFeed{Enabled: true, CheckIntervalHours: 24}
	assert.True(t, neverChecked.Due(now))

	disabled := &ExternalSubscriptionFeed{Enabled: false, CheckIntervalHours: 1, LastCheckedAt: &checked}
	assert.False(t, disabled.Due(now), "a disabled feed is never due")

	// A zero interval falls back to the default instead of becoming "always due".
	zeroInterval := &ExternalSubscriptionFeed{Enabled: true, LastCheckedAt: &checked}
	assert.False(t, zeroInterval.Due(now))
}

// TestExternalFeedDecodesListsToleratesBadJSON keeps a corrupt column from
// breaking the sweep.
func TestExternalFeedDecodesListsToleratesBadJSON(t *testing.T) {
	feed := &ExternalSubscriptionFeed{
		DomainTags:       JSON(`["安全法规","环保"]`),
		KnowledgeBaseIDs: JSON(`["kb-1"]`),
	}
	assert.Equal(t, []string{"安全法规", "环保"}, feed.ExternalFeedDomainTags())
	assert.Equal(t, []string{"kb-1"}, feed.ExternalFeedKnowledgeBaseIDs())

	broken := &ExternalSubscriptionFeed{DomainTags: JSON(`{not json`)}
	assert.Nil(t, broken.ExternalFeedDomainTags())
}

// TestNormalizeTagNamesPreservesCaseAndOrder covers the display contract: the
// operator typed "EHS", the UI must not show "ehs".
func TestNormalizeTagNamesPreservesCaseAndOrder(t *testing.T) {
	assert.Equal(t, []string{"EHS", "安全法规"},
		NormalizeTagNames([]string{" EHS ", "ehs", "", "安全法规"}))
}
