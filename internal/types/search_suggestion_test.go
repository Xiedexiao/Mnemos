package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNormalizeSuggestionPrefixCollapsesWhitespace covers the matching form.
func TestNormalizeSuggestionPrefixCollapsesWhitespace(t *testing.T) {
	assert.Equal(t, "e45 报警", NormalizeSuggestionPrefix("  E45   报警 "))
	assert.Equal(t, "", NormalizeSuggestionPrefix("   "))
}

// TestSuggestionPrefixUsableEnforcesMinimumLength is the anti-enumeration rule:
// one character would dump the whole title vocabulary.
func TestSuggestionPrefixUsableEnforcesMinimumLength(t *testing.T) {
	assert.False(t, SuggestionPrefixUsable("e"))
	assert.False(t, SuggestionPrefixUsable(" "))
	// Two runes is the floor. Using runes (not bytes) matters for CJK: "报"
	// is three bytes but one character.
	assert.True(t, SuggestionPrefixUsable("e4"))
	assert.True(t, SuggestionPrefixUsable("报警"))
}

// TestClampSuggestionLimit covers both bounds.
func TestClampSuggestionLimit(t *testing.T) {
	assert.Equal(t, DefaultSuggestionLimit, ClampSuggestionLimit(0))
	assert.Equal(t, DefaultSuggestionLimit, ClampSuggestionLimit(-5))
	assert.Equal(t, 12, ClampSuggestionLimit(12))
	assert.Equal(t, MaxSuggestionLimit, ClampSuggestionLimit(9999))
}

// TestClampHotQueryDays covers the window bounds.
func TestClampHotQueryDays(t *testing.T) {
	assert.Equal(t, DefaultHotQueryDays, ClampHotQueryDays(0))
	assert.Equal(t, 7, ClampHotQueryDays(7))
	assert.Equal(t, 365, ClampHotQueryDays(9999))
}

// TestDedupeSuggestionsKeepsFirstOccurrence pins cross-bucket dedupe: history
// wins over hot, so the list reads as "mine first, then everyone's".
func TestDedupeSuggestionsKeepsFirstOccurrence(t *testing.T) {
	history := []SearchSuggestion{
		{Text: "E45 报警", Kind: SuggestionKindHistory},
		{Text: "备件清单", Kind: SuggestionKindHistory},
	}
	hot := []SearchSuggestion{
		// Different case: still the same term to a reader.
		{Text: "e45 报警", Kind: SuggestionKindHot, Count: 40},
		{Text: "工单流程", Kind: SuggestionKindHot, Count: 12},
	}
	out := DedupeSuggestions(history, hot)
	require.Len(t, out, 3)
	assert.Equal(t, SuggestionKindHistory, out[0].Kind)
	assert.Equal(t, SuggestionKindHistory, out[1].Kind)
	assert.Equal(t, "工单流程", out[2].Text)
}

// TestDedupeSuggestionsSkipsEmptyText keeps blank rows from becoming a
// suggestion the reader cannot click.
func TestDedupeSuggestionsSkipsEmptyText(t *testing.T) {
	out := DedupeSuggestions([]SearchSuggestion{
		{Text: "   ", Kind: SuggestionKindHistory},
		{Text: "", Kind: SuggestionKindHot},
	})
	assert.Empty(t, out)
}

// TestSortHotSuggestionsBreaksTiesByText keeps an unordered SQL result stable.
func TestSortHotSuggestionsBreaksTiesByText(t *testing.T) {
	items := []SearchSuggestion{
		{Text: "b", Count: 5},
		{Text: "c", Count: 9},
		{Text: "a", Count: 5},
	}
	SortHotSuggestions(items)
	assert.Equal(t, []string{"c", "a", "b"}, []string{
		items[0].Text, items[1].Text, items[2].Text,
	})
}

// TestSuggestionKindValid covers the enum guard.
func TestSuggestionKindValid(t *testing.T) {
	for _, kind := range []SuggestionKind{
		SuggestionKindHistory, SuggestionKindHot,
		SuggestionKindTitle, SuggestionKindGlossary,
	} {
		assert.True(t, kind.Valid(), string(kind))
	}
	assert.False(t, SuggestionKind("nonsense").Valid())
}
