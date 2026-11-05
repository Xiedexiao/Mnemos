package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// stubIndexCheckRepo serves canned plans or failures.
type stubIndexCheckRepo struct {
	interfaces.IndexCheckRepository

	dialect string
	plans   map[string]string
	errs    map[string]error
}

func (s *stubIndexCheckRepo) Dialect() string { return s.dialect }

func (s *stubIndexCheckRepo) Explain(
	_ context.Context, check types.IndexCheckQuery,
) (string, error) {
	if err := s.errs[check.Name]; err != nil {
		return "", err
	}
	return s.plans[check.Name], nil
}

func newIndexCheckServiceForTest(
	repo interfaces.IndexCheckRepository,
) *indexCheckService {
	svc := NewIndexCheckService(repo).(*indexCheckService)
	svc.now = func() time.Time { return time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC) }
	return svc
}

// TestRunReportsAFullScan proves the check catches the thing it exists for.
func TestRunReportsAFullScan(t *testing.T) {
	plans := map[string]string{}
	for _, check := range types.IndexCheckQueries() {
		plans[check.Name] = "3|0|0|SEARCH " + check.Name + " USING INDEX idx (x=?)"
	}
	// Exactly one check scans; the rest use an index.
	plans["knowledges.by_tenant_kb"] = "2|0|0|SCAN knowledges"
	repo := &stubIndexCheckRepo{dialect: "sqlite", plans: plans, errs: map[string]error{}}
	svc := newIndexCheckServiceForTest(repo)

	report, err := svc.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, report.FullScans)
	assert.Zero(t, report.Unknown)
	assert.False(t, report.Pass())

	var flagged *types.IndexCheckResult
	for index, result := range report.Results {
		if result.Verdict == types.IndexCheckFullScan {
			flagged = &report.Results[index]
		}
	}
	require.NotNil(t, flagged)
	assert.Equal(t, "knowledges.by_tenant_kb", flagged.Name)
	assert.NotEmpty(t, flagged.Why, "the report says what degrades")
	assert.NotEmpty(t, flagged.Advice, "the report says what to do")
}

// TestRunMarksFailuresUnknownInsteadOfPassing is the honesty rule: one broken
// check must not let the report claim success.
func TestRunMarksFailuresUnknownInsteadOfPassing(t *testing.T) {
	repo := &stubIndexCheckRepo{
		dialect: "sqlite",
		plans:   map[string]string{}, // every check yields an empty plan
		errs: map[string]error{
			"knowledges.by_tenant_kb": errors.New("no such table: knowledges"),
		},
	}
	svc := newIndexCheckServiceForTest(repo)

	report, err := svc.Run(context.Background())
	require.NoError(t, err, "one broken check does not fail the whole run")
	require.Len(t, report.Results, len(types.IndexCheckQueries()))

	// One failed lookup plus an empty plan for the rest: all unknown.
	assert.Equal(t, len(types.IndexCheckQueries()), report.Unknown)
	assert.False(t, report.Pass())

	byName := map[string]types.IndexCheckResult{}
	for _, result := range report.Results {
		byName[result.Name] = result
	}
	assert.Equal(t, types.IndexCheckUnknown, byName["knowledges.by_tenant_kb"].Verdict)
	assert.Contains(t, byName["knowledges.by_tenant_kb"].Advice, "no such table")
}

// TestRunPassesWhenEveryPlanUsesAnIndex is the green path.
func TestRunPassesWhenEveryPlanUsesAnIndex(t *testing.T) {
	plans := map[string]string{}
	for _, check := range types.IndexCheckQueries() {
		plans[check.Name] = "3|0|0|SEARCH " + check.Name + " USING INDEX idx (x=?)"
	}
	repo := &stubIndexCheckRepo{dialect: "sqlite", plans: plans, errs: map[string]error{}}
	svc := newIndexCheckServiceForTest(repo)

	report, err := svc.Run(context.Background())
	require.NoError(t, err)
	assert.Zero(t, report.FullScans)
	assert.Zero(t, report.Unknown)
	assert.True(t, report.Pass())
	assert.Equal(t, "2026-11-05T12:00:00Z", report.CheckedAt)
}

// TestRunUsesTheRepositoryDialect keeps Postgres parsing distinct.
func TestRunUsesTheRepositoryDialect(t *testing.T) {
	plans := map[string]string{}
	for _, check := range types.IndexCheckQueries() {
		plans[check.Name] = "Seq Scan on " + check.Name
	}
	// An Index Scan must not be read as a full scan.
	plans["knowledges.by_tenant_kb"] = "Index Scan using idx on knowledges (cost=0.15..8.17)"
	repo := &stubIndexCheckRepo{dialect: "postgres", plans: plans, errs: map[string]error{}}
	svc := newIndexCheckServiceForTest(repo)
	report, err := svc.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "postgres", report.Dialect)
	// Every other check is a genuine Seq Scan here, so they must be flagged;
	// the one Index Scan must NOT be. That is the dialect split working.
	assert.Equal(t, len(types.IndexCheckQueries())-1, report.FullScans,
		"only the Index Scan check may pass")
}
