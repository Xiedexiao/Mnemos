package types

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// provenanceTestNow is the reference instant for relative wording.
var provenanceTestNow = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

// TestRenderCitationProvenanceFull covers the complete case.
func TestRenderCitationProvenanceFull(t *testing.T) {
	effective := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	reviewed := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	label := RenderCitationProvenance(CitationProvenance{
		Version: 3, EffectiveAt: &effective, LastReviewedAt: &reviewed,
	}, "张三", provenanceTestNow)

	assert.Equal(t, "v3 · 生效 2026-03-01 · 复核 2026-10-12 张三", label)
}

// TestRenderCitationProvenanceOmitsMissingParts is the honesty rule: absent
// facts are omitted, never rendered as "unknown".
func TestRenderCitationProvenanceOmitsMissingParts(t *testing.T) {
	// Version only.
	assert.Equal(t, "v1", RenderCitationProvenance(
		CitationProvenance{Version: 1}, "", provenanceTestNow))

	// Review date without a reviewer name.
	reviewed := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	assert.Equal(t, "复核 2026-10-12", RenderCitationProvenance(
		CitationProvenance{LastReviewedAt: &reviewed}, "", provenanceTestNow))

	// Nothing at all → empty, so the caller does not print a dangling "·".
	assert.Empty(t, RenderCitationProvenance(CitationProvenance{}, "", provenanceTestNow))
}

// TestRenderCitationProvenanceMarksFutureEffectiveness is the trap this guards:
// a version that is not yet in force must not read as if it were.
func TestRenderCitationProvenanceMarksFutureEffectiveness(t *testing.T) {
	future := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	label := RenderCitationProvenance(CitationProvenance{
		Version: 4, EffectiveAt: &future,
	}, "", provenanceTestNow)
	assert.Equal(t, "v4 · 将于 2027-01-01 生效", label)

	past := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	label = RenderCitationProvenance(CitationProvenance{
		Version: 4, EffectiveAt: &past,
	}, "", provenanceTestNow)
	assert.Equal(t, "v4 · 生效 2026-01-01", label)
}

// TestRenderCitationProvenanceUsesUTCDates keeps a timestamp from one time zone
// from printing as a different day in another.
func TestRenderCitationProvenanceUsesUTCDates(t *testing.T) {
	// 23:30 on 2026-03-01 in UTC+8 is 15:30 UTC on the same date, but a value
	// stored as UTC is what the label must show consistently.
	effective := time.Date(2026, 3, 2, 3, 0, 0, 0, time.UTC)
	label := RenderCitationProvenance(CitationProvenance{
		EffectiveAt: &effective,
	}, "", provenanceTestNow)
	assert.Equal(t, "生效 2026-03-02", label)
}

// TestBuildCitationProvenanceFillsLabel proves the constructor renders too.
func TestBuildCitationProvenanceFillsLabel(t *testing.T) {
	reviewed := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	provenance := BuildCitationProvenance(2, nil, &reviewed, "u-1", "李四", provenanceTestNow)
	assert.Equal(t, 2, provenance.Version)
	assert.Equal(t, "u-1", provenance.LastReviewedBy)
	assert.Equal(t, "v2 · 复核 2026-10-12 李四", provenance.Label)
}

// TestCitationProvenanceSuffix wraps only when there is content.
func TestCitationProvenanceSuffix(t *testing.T) {
	assert.Equal(t, "（v3）", CitationProvenanceSuffix(
		CitationProvenance{Version: 3}, "", provenanceTestNow))
	// No dangling brackets when nothing is known.
	assert.Equal(t, "", CitationProvenanceSuffix(
		CitationProvenance{}, "", provenanceTestNow))
}

// TestProvenanceKnown pins what counts as "known".
func TestProvenanceKnown(t *testing.T) {
	assert.False(t, CitationProvenance{}.ProvenanceKnown(),
		"an empty provenance renders nothing")
	// v1 is real information: it says the entry was never refreshed.
	assert.True(t, CitationProvenance{Version: 1}.ProvenanceKnown())
	reviewed := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	assert.True(t, CitationProvenance{LastReviewedAt: &reviewed}.ProvenanceKnown())
	effective := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	assert.True(t, CitationProvenance{EffectiveAt: &effective}.ProvenanceKnown())
}

// TestRenderCitationProvenanceIgnoresZeroTimes keeps a zero-valued pointer
// (which the database layer can produce) out of the label.
func TestRenderCitationProvenanceIgnoresZeroTimes(t *testing.T) {
	zero := time.Time{}
	label := RenderCitationProvenance(CitationProvenance{
		Version: 2, EffectiveAt: &zero, LastReviewedAt: &zero,
	}, "", provenanceTestNow)
	assert.Equal(t, "v2", label)
}
