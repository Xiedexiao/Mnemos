package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/Tencent/WeKnora/internal/types"
)

// TestSourceUpdateStale covers §3.3/§7.10: a redelivered older event must not
// overwrite a newer version. Before this check the platform recorded
// source_updated_at but never compared it, so an out-of-order write silently
// rolled content back.
func TestSourceUpdateStale(t *testing.T) {
	base := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	stored := map[string]string{"source_updated_at": base.Format(time.RFC3339)}

	cases := []struct {
		name     string
		incoming time.Time
		stored   map[string]string
		want     bool
	}{
		{
			name:     "newer event replaces",
			incoming: base.Add(time.Hour),
			stored:   stored,
			want:     false,
		},
		{
			name:     "same instant is the same version",
			incoming: base,
			stored:   stored,
			want:     false,
		},
		{
			name:     "older event is rejected",
			incoming: base.Add(-time.Hour),
			stored:   stored,
			want:     true,
		},
		{
			name:     "no stored timestamp never blocks",
			incoming: base.Add(-24 * time.Hour),
			stored:   map[string]string{},
			want:     false,
		},
		{
			name:     "unparsable stored timestamp never blocks",
			incoming: base.Add(-24 * time.Hour),
			stored:   map[string]string{"source_updated_at": "not-a-time"},
			want:     false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item := &types.FetchedItem{ExternalID: "doc-1", UpdatedAt: tc.incoming}
			assert.Equal(t, tc.want, sourceUpdateStale(item, tc.stored))
		})
	}
}

// TestSourceUpdateStaleIgnoresUnknownIncoming keeps connectors that expose no
// modified time working: dropping their events would be worse than an
// occasional out-of-order write.
func TestSourceUpdateStaleIgnoresUnknownIncoming(t *testing.T) {
	stored := map[string]string{
		"source_updated_at": time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC).Format(time.RFC3339),
	}
	assert.False(t, sourceUpdateStale(&types.FetchedItem{ExternalID: "doc-1"}, stored),
		"an item with no timestamp cannot be ordered, so it must not be dropped")
	assert.False(t, sourceUpdateStale(nil, stored))
}

// TestSourceVersionOf covers the stored-row adapter.
func TestSourceVersionOf(t *testing.T) {
	assert.Nil(t, sourceVersionOf(nil))

	knowledge := &types.Knowledge{}
	knowledge.Metadata = types.JSON(`{"source_updated_at":"2026-11-03T10:00:00Z","external_id":"doc-1"}`)
	meta := sourceVersionOf(knowledge)
	assert.Equal(t, "2026-11-03T10:00:00Z", meta["source_updated_at"])
	assert.Equal(t, "doc-1", meta["external_id"])
}
