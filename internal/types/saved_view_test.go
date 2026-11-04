package types

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidateSavedViewName covers the trim and length rules.
func TestValidateSavedViewName(t *testing.T) {
	name, err := ValidateSavedViewName("  我负责的设备  ")
	require.NoError(t, err)
	assert.Equal(t, "我负责的设备", name)

	_, err = ValidateSavedViewName("   ")
	require.Error(t, err)

	long := make([]rune, MaxSavedViewNameRunes+1)
	for i := range long {
		long[i] = '名'
	}
	_, err = ValidateSavedViewName(string(long))
	require.Error(t, err, "an over-long name is rejected, not truncated")

	exact := string(long[:MaxSavedViewNameRunes])
	_, err = ValidateSavedViewName(exact)
	require.NoError(t, err, "the boundary itself is allowed")
}

// TestValidateSavedViewFilterAcceptsARealisticView is the happy path.
func TestValidateSavedViewFilterAcceptsARealisticView(t *testing.T) {
	filter := SavedViewFilter{
		Keyword:           "E45",
		TagIDs:            []string{"tag-1", "tag-2"},
		FileType:          "pdf",
		Source:            "web",
		FolderPath:        "sop/e45",
		FolderScope:       string(FolderScopeSubtree),
		LifecycleStatuses: []string{string(KnowledgeLifecyclePendingReviewCycle)},
		SortBy:            string(KnowledgeListSortByUpdatedAt),
		SortOrder:         string(KnowledgeListSortDescending),
		UpdatedFrom:       "2026-01-01T00:00:00Z",
	}
	require.NoError(t, ValidateSavedViewFilter(filter))
}

// TestValidateSavedViewFilterRejectsUnknownEnums is the "fail loudly" rule:
// a typo in a status name would otherwise silently return an empty list.
func TestValidateSavedViewFilterRejectsUnknownEnums(t *testing.T) {
	cases := map[string]SavedViewFilter{
		"lifecycle":    {LifecycleStatuses: []string{"not_a_status"}},
		"sort field":   {SortBy: "relevance"},
		"sort order":   {SortOrder: "sideways"},
		"folder scope": {FolderScope: "deep", FolderPath: "sop"},
	}
	for label, filter := range cases {
		assert.Error(t, ValidateSavedViewFilter(filter), label)
	}
}

// TestValidateSavedViewFilterRequiresFolderPath keeps an empty condition from
// being stored: "scope=subtree with no path" would silently match nothing.
func TestValidateSavedViewFilterRequiresFolderPath(t *testing.T) {
	assert.Error(t, ValidateSavedViewFilter(SavedViewFilter{
		FolderScope: string(FolderScopeSubtree),
	}))
	// FolderScopeAny means "no folder condition", which is fine.
	assert.NoError(t, ValidateSavedViewFilter(SavedViewFilter{
		FolderScope: string(FolderScopeAny),
	}))
}

// TestValidateSavedViewFilterChecksTimestamps covers the RFC3339 contract.
func TestValidateSavedViewFilterChecksTimestamps(t *testing.T) {
	assert.NoError(t, ValidateSavedViewFilter(SavedViewFilter{
		UpdatedFrom: "2026-01-01T00:00:00Z",
	}))
	assert.Error(t, ValidateSavedViewFilter(SavedViewFilter{
		UpdatedFrom: "2026-01-01",
	}), "a date without time is ambiguous across time zones")
}

// TestValidateSavedViewFilterCapsCollections bounds payload size.
func TestValidateSavedViewFilterCapsCollections(t *testing.T) {
	tags := make([]string, MaxSavedViewTagCount+1)
	for i := range tags {
		tags[i] = "tag"
	}
	assert.Error(t, ValidateSavedViewFilter(SavedViewFilter{TagIDs: tags}))
	assert.Error(t, ValidateSavedViewFilter(SavedViewFilter{TagIDs: []string{"  "}}))

	long := make([]rune, MaxSavedViewKeywordRunes+1)
	for i := range long {
		long[i] = 'k'
	}
	assert.Error(t, ValidateSavedViewFilter(SavedViewFilter{Keyword: string(long)}))
}

// TestSavedViewFilterNormalizeDedupes covers the free-form cleanup.
func TestSavedViewFilterNormalizeDedupes(t *testing.T) {
	filter := SavedViewFilter{
		Keyword:           "  E45  ",
		TagIDs:            []string{" tag-1 ", "tag-1", "", "tag-2"},
		LifecycleStatuses: []string{"valid", " valid ", ""},
		FolderPath:        "  sop ",
	}
	filter.Normalize()
	assert.Equal(t, "E45", filter.Keyword)
	assert.Equal(t, []string{"tag-1", "tag-2"}, filter.TagIDs)
	assert.Equal(t, []string{"valid"}, filter.LifecycleStatuses)
	assert.Equal(t, "sop", filter.FolderPath)
}

// TestToListFilterRestoresEveryDimension proves the round trip.
func TestToListFilterRestoresEveryDimension(t *testing.T) {
	filter := SavedViewFilter{
		Keyword:           "报警",
		TagIDs:            []string{"tag-1"},
		FileType:          "pdf",
		ParseStatus:       "completed",
		Source:            "web",
		UpdatedFrom:       "2026-01-01T00:00:00Z",
		UpdatedTo:         "2026-02-01T00:00:00Z",
		FolderPath:        "sop",
		FolderScope:       string(FolderScopeSubtree),
		LifecycleStatuses: []string{"valid", "stale"},
		SortBy:            string(KnowledgeListSortByFileName),
		SortOrder:         string(KnowledgeListSortAscending),
	}
	listFilter := filter.ToListFilter()

	assert.Equal(t, "报警", listFilter.Keyword)
	assert.Equal(t, []string{"tag-1"}, listFilter.TagIDs)
	assert.Equal(t, "pdf", listFilter.FileType)
	assert.Equal(t, "completed", listFilter.ParseStatus)
	assert.Equal(t, "web", listFilter.Source)
	assert.Equal(t, FolderScopeSubtree, listFilter.FolderScope)
	assert.Equal(t, "sop", listFilter.FolderPath)
	assert.Equal(t, []string{"valid", "stale"}, listFilter.LifecycleStatuses)
	assert.Equal(t, KnowledgeListSortByFileName, listFilter.SortBy)
	assert.Equal(t, KnowledgeListSortAscending, listFilter.SortOrder)
	assert.Equal(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), listFilter.UpdatedFrom)
	assert.Equal(t, time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), listFilter.UpdatedTo)
}

// TestToListFilterLeavesPermissionFieldsEmpty is the security-relevant part:
// a saved view must never carry a permission verdict from the past.
func TestToListFilterLeavesPermissionFieldsEmpty(t *testing.T) {
	listFilter := SavedViewFilter{Keyword: "报警"}.ToListFilter()
	assert.Empty(t, listFilter.ExcludeKnowledgeIDs,
		"the denial set is recomputed per request, never frozen into a view")
	assert.Empty(t, listFilter.MaxClassification)
}

// TestToListFilterIgnoresMalformedTimestamps keeps a bad value from poisoning
// the query: an unparsable bound becomes "unbounded".
func TestToListFilterIgnoresMalformedTimestamps(t *testing.T) {
	listFilter := SavedViewFilter{UpdatedFrom: "not-a-date"}.ToListFilter()
	assert.True(t, listFilter.UpdatedFrom.IsZero())
}

// TestSavedViewFilterRoundTripThroughJSON covers the storage form.
func TestSavedViewFilterRoundTripThroughJSON(t *testing.T) {
	view := &SavedKnowledgeView{}
	filter := SavedViewFilter{
		Keyword:           "E45",
		TagIDs:            []string{"tag-1"},
		LifecycleStatuses: []string{"valid"},
	}
	require.NoError(t, view.SetFilter(filter))
	require.NotEmpty(t, view.Filter)

	decoded := view.SavedViewFilterOf()
	assert.Equal(t, filter, decoded)
}

// TestSavedViewFilterOfToleratesCorruptJSON degrades to "no conditions"
// instead of breaking the whole list.
func TestSavedViewFilterOfToleratesCorruptJSON(t *testing.T) {
	view := &SavedKnowledgeView{Filter: JSON("{not json")}
	assert.Equal(t, SavedViewFilter{}, view.SavedViewFilterOf())

	empty := &SavedKnowledgeView{}
	assert.Equal(t, SavedViewFilter{}, empty.SavedViewFilterOf())

	var nilView *SavedKnowledgeView
	assert.Equal(t, SavedViewFilter{}, nilView.SavedViewFilterOf())
}
