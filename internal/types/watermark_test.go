package types

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWatermarkStrengthByClassification is the §16.3 rule: stricter content
// gets a more intrusive mark, public content gets none.
func TestWatermarkStrengthByClassification(t *testing.T) {
	assert.Equal(t, WatermarkStrengthNone,
		WatermarkStrengthForClassification(KnowledgeClassificationPublic))
	assert.Equal(t, WatermarkStrengthFooter,
		WatermarkStrengthForClassification(KnowledgeClassificationInternal))
	assert.Equal(t, WatermarkStrengthTile,
		WatermarkStrengthForClassification(KnowledgeClassificationSecret))
	assert.Equal(t, WatermarkStrengthFull,
		WatermarkStrengthForClassification(KnowledgeClassificationConfidential))
	// An empty classification reads as internal, matching Parse's default.
	assert.Equal(t, WatermarkStrengthFooter,
		WatermarkStrengthForClassification(KnowledgeClassification("")))
}

// TestWatermarkRequiredSkipsPublic is the ledger noise guard.
func TestWatermarkRequiredSkipsPublic(t *testing.T) {
	assert.False(t, WatermarkRequired(KnowledgeClassificationPublic))
	assert.True(t, WatermarkRequired(KnowledgeClassificationInternal))
	assert.True(t, WatermarkRequired(KnowledgeClassificationSecret))
	assert.True(t, WatermarkRequired(KnowledgeClassificationConfidential))
}

// TestCanInjectWatermark covers the format whitelist.
func TestCanInjectWatermark(t *testing.T) {
	for _, name := range []string{"doc.md", "notes.TXT", "表.csv", "data.tsv", "a.log"} {
		assert.True(t, CanInjectWatermark(name), name)
	}
	// JSON is deliberately excluded: a comment makes it invalid, so downstream
	// parsers break.
	assert.False(t, CanInjectWatermark("payload.json"))
	assert.False(t, CanInjectWatermark("report.pdf"))
	assert.False(t, CanInjectWatermark("spec.docx"))
	assert.False(t, CanInjectWatermark("photo.png"))
	assert.False(t, CanInjectWatermark(""))
}

// TestPlanWatermarkInjectsForText covers the injected path.
func TestPlanWatermarkInjectsForText(t *testing.T) {
	now := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	applied := PlanWatermark("sop.md", "k-1", "E45 SOP", "u-1",
		KnowledgeClassificationInternal, now)

	assert.Equal(t, WatermarkStrengthFooter, applied.Strength)
	assert.Equal(t, WatermarkInjected, applied.Mode)
	assert.Equal(t, "sop.md", applied.FileName)
	assert.Empty(t, applied.SidecarName)
	// The mark names who, when, which document, its classification and a
	// per-download code.
	assert.Contains(t, applied.Text, "u-1")
	assert.Contains(t, applied.Text, "2026-11-05 10:00")
	assert.Contains(t, applied.Text, "k-1")
	assert.Contains(t, applied.Text, "internal")
}

// TestPlanWatermarkUsesSidecarForBinary is the honest boundary: formats the
// server cannot rewrite get a note instead of a fake mark.
func TestPlanWatermarkUsesSidecarForBinary(t *testing.T) {
	now := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	applied := PlanWatermark("spec.pdf", "k-2", "设备手册", "u-1",
		KnowledgeClassificationSecret, now)

	assert.Equal(t, WatermarkStrengthTile, applied.Strength)
	assert.Equal(t, WatermarkSidecar, applied.Mode)
	assert.Equal(t, "spec.watermark.md", applied.SidecarName)
	assert.NotEmpty(t, applied.Note)
	assert.Contains(t, applied.Note, "无法在服务端可靠写入")

	body := applied.SidecarBody()
	assert.Contains(t, body, applied.Text)
	assert.Contains(t, body, "设备手册")
	assert.Contains(t, body, "secret")
}

// TestPlanWatermarkPublicGetsNothing keeps public downloads clean.
func TestPlanWatermarkPublicGetsNothing(t *testing.T) {
	applied := PlanWatermark("readme.md", "k-3", "公开说明", "u-1",
		KnowledgeClassificationPublic, time.Now())
	assert.Equal(t, WatermarkStrengthNone, applied.Strength)
	assert.Empty(t, applied.Text)
	assert.Empty(t, applied.Mode)
	assert.Equal(t, "readme.md", applied.FileName)
}

// TestBuildWatermarkTextIsUniquePerDownload is why a random code is included:
// two downloads of the same file at the same second must be distinguishable.
func TestBuildWatermarkTextIsUniquePerDownload(t *testing.T) {
	now := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	first := BuildWatermarkText("u-1", "k-1", KnowledgeClassificationSecret, now)
	second := BuildWatermarkText("u-1", "k-1", KnowledgeClassificationSecret, now)
	assert.NotEqual(t, first, second)
}

// TestBuildWatermarkTextHandlesMissingUser keeps an empty actor from producing
// a blank mark.
func TestBuildWatermarkTextHandlesMissingUser(t *testing.T) {
	text := BuildWatermarkText("", "k-1", KnowledgeClassificationInternal, time.Now())
	assert.Contains(t, text, "unknown")
}

// TestInjectTextWatermarkAppendsAndKeepsContent is the non-destructive rule.
func TestInjectTextWatermarkAppendsAndKeepsContent(t *testing.T) {
	origin := []byte("# 标题\n\n正文内容。\n")
	applied := WatermarkApplied{
		Strength: WatermarkStrengthFooter, Mode: WatermarkInjected, Text: "u-1 · 2026 · k-1",
	}
	out := InjectTextWatermark(origin, "doc.md", applied)
	assert.True(t, strings.HasPrefix(string(out), string(origin)),
		"the original content must be preserved byte for byte")
	assert.Contains(t, string(out), applied.Text)
	assert.Contains(t, string(out), "受控分发")
}

// TestInjectTextWatermarkCSVUsesCommentRow covers the CSV case: a prose footer
// would break column parsing.
func TestInjectTextWatermarkCSVUsesCommentRow(t *testing.T) {
	applied := WatermarkApplied{Text: "u-1 · k-1"}
	out := string(InjectTextWatermark([]byte("a,b\n1,2\n"), "表.csv", applied))
	assert.Contains(t, out, "# ,水印,", "CSV gets a comment row, not a Markdown footer")
	assert.NotContains(t, out, ">")
}

// TestWatermarkStrengthValid covers the enum guard.
func TestWatermarkStrengthValid(t *testing.T) {
	for _, strength := range []WatermarkStrength{
		WatermarkStrengthNone, WatermarkStrengthFooter,
		WatermarkStrengthTile, WatermarkStrengthFull,
	} {
		assert.True(t, strength.Valid(), string(strength))
	}
	assert.False(t, WatermarkStrength("waterfall").Valid())
}

// TestSortWatermarkRecordsOrdersNewestFirst keeps the ledger readable.
func TestSortWatermarkRecordsOrdersNewestFirst(t *testing.T) {
	now := time.Now()
	records := []*WatermarkRecord{
		{ID: "old", CreatedAt: now.Add(-time.Hour)},
		{ID: "new", CreatedAt: now},
	}
	SortWatermarkRecords(records)
	require.Len(t, records, 2)
	assert.Equal(t, "new", records[0].ID)
}
