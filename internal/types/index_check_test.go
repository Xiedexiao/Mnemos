package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseIndexPlanSQLiteFullScan is the "no index used" form.
func TestParseIndexPlanSQLiteFullScan(t *testing.T) {
	plan := "2|0|0|SCAN knowledges"
	result := ParseIndexPlan("sqlite", plan)
	assert.True(t, result.ScansAll)
	assert.Equal(t, IndexCheckFullScan, result.Verdict)
	assert.NotEmpty(t, result.Advice)
}

// TestParseIndexPlanSQLiteIndexSearch is the "index used" form.
func TestParseIndexPlanSQLiteIndexSearch(t *testing.T) {
	plan := "3|0|0|SEARCH knowledges USING INDEX idx_knowledges_tenant_id (tenant_id=?)"
	result := ParseIndexPlan("sqlite", plan)
	assert.False(t, result.ScansAll, "SEARCH ... USING INDEX is not a full scan")
	assert.Equal(t, IndexCheckOK, result.Verdict)
}

// TestParseIndexPlanPostgresSeqScan is the Postgres full-scan form.
func TestParseIndexPlanPostgresSeqScan(t *testing.T) {
	plan := "Seq Scan on knowledges  (cost=0.00..1.20 rows=20 width=36)"
	result := ParseIndexPlan("postgres", plan)
	assert.True(t, result.ScansAll)
	assert.Equal(t, IndexCheckFullScan, result.Verdict)
}

// TestParseIndexPlanPostgresIndexScanIsNotAFullScan is the reason the two
// dialects are parsed separately: "Index Scan" contains the substring "Scan",
// so a shared matcher would misreport every healthy Postgres query as broken.
func TestParseIndexPlanPostgresIndexScanIsNotAFullScan(t *testing.T) {
	plan := "Index Scan using idx_knowledges_tenant_id on knowledges  (cost=0.15..8.17 rows=1 width=36)"
	result := ParseIndexPlan("postgres", plan)
	assert.False(t, result.ScansAll)
	assert.Equal(t, IndexCheckOK, result.Verdict)

	bitmap := ParseIndexPlan("postgres", "Bitmap Index Scan on idx_chunks_knowledge")
	assert.False(t, bitmap.ScansAll)
}

// TestParseIndexPlanEmptyIsUnknown keeps a failed plan from counting as a pass.
func TestParseIndexPlanEmptyIsUnknown(t *testing.T) {
	result := ParseIndexPlan("sqlite", "   ")
	assert.Equal(t, IndexCheckUnknown, result.Verdict)
	assert.NotEmpty(t, result.Advice)
}

// TestParseIndexPlanUnknownDialectIsUnknown covers the defensive branch.
func TestParseIndexPlanUnknownDialectIsUnknown(t *testing.T) {
	result := ParseIndexPlan("mysql", "whatever")
	assert.Equal(t, IndexCheckUnknown, result.Verdict)
}

// TestParseIndexPlanTruncatesLongPlans keeps a report readable.
func TestParseIndexPlanTruncatesLongPlans(t *testing.T) {
	long := "Seq Scan on big_table"
	for i := 0; i < 200; i++ {
		long += " -> Filter: something"
	}
	result := ParseIndexPlan("postgres", long)
	assert.LessOrEqual(t, len([]rune(result.Plan)), maxPlanReportRunes+1)
	assert.Contains(t, result.Plan, "…")
}

// TestIndexCheckReportPassRequiresEverythingIsKnown is the honesty rule: a
// check that could not run must not be counted as passing.
func TestIndexCheckReportPassRequiresEverythingIsKnown(t *testing.T) {
	assert.True(t, IndexCheckReport{Results: []IndexCheckResult{{Verdict: IndexCheckOK}}}.Pass())

	fullScan := IndexCheckReport{
		Results:   []IndexCheckResult{{Verdict: IndexCheckFullScan}},
		FullScans: 1,
	}
	assert.False(t, fullScan.Pass())

	unknown := IndexCheckReport{
		Results: []IndexCheckResult{{Verdict: IndexCheckUnknown}},
		Unknown: 1,
	}
	assert.False(t, unknown.Pass(), "unknown is not a pass")
}

// TestIndexCheckQueriesAreWellFormed keeps every configured check usable.
func TestIndexCheckQueriesAreWellFormed(t *testing.T) {
	checks := IndexCheckQueries()
	require.NotEmpty(t, checks)

	names := make(map[string]struct{}, len(checks))
	for _, check := range checks {
		assert.NotEmpty(t, check.Name)
		assert.NotEmpty(t, check.SQL)
		assert.NotEmpty(t, check.Why, "%s must state what degrades", check.Name)
		// Every check must bind the same number of arguments as placeholders.
		placeholders := 0
		for _, ch := range check.SQL {
			if ch == '?' {
				placeholders++
			}
		}
		assert.Equal(t, placeholders, len(check.Args), "%s: args must match placeholders", check.Name)
		_, duplicate := names[check.Name]
		assert.False(t, duplicate, "%s is declared twice", check.Name)
		names[check.Name] = struct{}{}
	}
}

// TestIndexCheckQueriesCoverTheHotPaths pins the handful of checks that matter.
func TestIndexCheckQueriesCoverTheHotPaths(t *testing.T) {
	names := make(map[string]struct{})
	for _, check := range IndexCheckQueries() {
		names[check.Name] = struct{}{}
	}
	for _, required := range []string{
		"knowledges.by_tenant_kb",
		"knowledges.lifecycle_exclusion",
		"chunks.by_knowledge",
		"evidence_packs.by_request",
		"knowledge_view_events.by_user_history",
	} {
		assert.Contains(t, names, required,
			"%s is on a hot path and must stay covered", required)
	}
}
