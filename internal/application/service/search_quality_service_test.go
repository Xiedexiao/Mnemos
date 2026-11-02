package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// stubSearchLogRepo captures written rows.
type stubSearchLogRepo struct {
	interfaces.SearchLogRepository
	entries []*types.SearchLog
	clicked []string
}

func (s *stubSearchLogRepo) Create(_ context.Context, entry *types.SearchLog) error {
	s.entries = append(s.entries, entry)
	return nil
}

func (s *stubSearchLogRepo) MarkClicked(_ context.Context, _ uint64, logID string) error {
	s.clicked = append(s.clicked, logID)
	return nil
}

// TestNormalizeSearchQuery covers the clustering key: case and whitespace noise
// must collapse so " STACK-01  E45" and "stack-01 e45" are the same gap, while
// the punctuation that makes error codes meaningful is preserved.
func TestNormalizeSearchQuery(t *testing.T) {
	assert.Equal(t, "stack-01 e45", types.NormalizeSearchQuery("  STACK-01   E45 "))
	assert.Equal(t, "sop-eq-012", types.NormalizeSearchQuery("SOP-EQ-012"))
	assert.Equal(t, "安全门开关", types.NormalizeSearchQuery("  安全门开关\n"))
	assert.Empty(t, types.NormalizeSearchQuery("   "))
}

// TestRecordResultCarriesTelemetryFields makes sure the log row holds what the
// dashboard needs — including the authorization-filtered count, which is the
// only way an operator can tell "nothing exists" from "nothing is visible".
func TestRecordResultCarriesTelemetryFields(t *testing.T) {
	store := &stubSearchLogRepo{}
	svc := NewSearchQualityService(store)
	ctx := types.WithExecutionTenant(context.Background(), 7)
	ctx = context.WithValue(ctx, types.UserIDContextKey, "user-1")

	svc.RecordResult(ctx, types.SearchChannelHybrid, "  STACK-01  E45 ",
		[]string{"kb-1"}, 0, 3, 0, 120*time.Millisecond)

	require.Len(t, store.entries, 1)
	entry := store.entries[0]
	assert.EqualValues(t, 7, entry.TenantID)
	assert.Equal(t, "user-1", entry.UserID)
	assert.Equal(t, "stack-01 e45", entry.QueryNormalized)
	assert.Equal(t, "  STACK-01  E45 ", entry.Query, "the original query is kept for display")
	assert.Equal(t, 0, entry.ResultCount)
	assert.Equal(t, 3, entry.PermissionFilteredCount)
	assert.Equal(t, 120, entry.DurationMs)
}

// TestRecordSkipsEmptyTenant keeps an unauthenticated context from writing
// tenant-less telemetry.
func TestRecordSkipsEmptyTenant(t *testing.T) {
	store := &stubSearchLogRepo{}
	svc := NewSearchQualityService(store)
	svc.Record(context.Background(), &types.SearchLog{Query: "x"})
	assert.Empty(t, store.entries)
}

// stubGapRepo keeps a map of clustered gaps.
type stubGapRepo struct {
	interfaces.KnowledgeGapRepository
	rows map[string]*types.KnowledgeGap
}

func newStubGapRepo() *stubGapRepo {
	return &stubGapRepo{rows: map[string]*types.KnowledgeGap{}}
}

func (s *stubGapRepo) UpsertOccurrence(
	_ context.Context, gap *types.KnowledgeGap,
) (*types.KnowledgeGap, error) {
	if existing, ok := s.rows[gap.QueryNormalized]; ok {
		existing.Occurrences++
		existing.LastSeenAt = gap.LastSeenAt
		return existing, nil
	}
	s.rows[gap.QueryNormalized] = gap
	return gap, nil
}

func (s *stubGapRepo) List(
	_ context.Context, _ uint64, _ types.KnowledgeGapStatus, _ int,
) ([]*types.KnowledgeGap, error) {
	out := make([]*types.KnowledgeGap, 0, len(s.rows))
	for _, row := range s.rows {
		out = append(out, row)
	}
	return out, nil
}

func (s *stubGapRepo) Update(
	_ context.Context, _ uint64, gapID string, updates map[string]interface{},
) (*types.KnowledgeGap, error) {
	for _, row := range s.rows {
		if row.ID != gapID && gapID != "" {
			continue
		}
		if status, ok := updates["status"].(string); ok {
			row.Status = status
		}
		if assignee, ok := updates["assignee_id"].(string); ok {
			row.AssigneeID = assignee
		}
		if knowledge, ok := updates["resolved_knowledge_id"].(string); ok {
			row.ResolvedKnowledgeID = knowledge
		}
		return row, nil
	}
	return nil, assert.AnError
}

func (s *stubGapRepo) Get(
	_ context.Context, _ uint64, gapID string,
) (*types.KnowledgeGap, error) {
	for _, row := range s.rows {
		if row.ID == gapID {
			return row, nil
		}
	}
	return nil, assert.AnError
}

// TestObserveClustersRepeatedMisses is the §14.4 behaviour: repeated misses of
// the same question aggregate into one row rather than flooding the list.
func TestObserveClustersRepeatedMisses(t *testing.T) {
	repo := newStubGapRepo()
	svc := NewKnowledgeGapService(repo)
	ctx := context.Background()

	for _, query := range []string{"E45 报警", "  e45   报警", "E45 报警"} {
		_, err := svc.Observe(ctx, 7, query, types.KnowledgeGapSourceSearchZeroResult)
		require.NoError(t, err)
	}
	require.Len(t, repo.rows, 1, "case/whitespace variants are the same gap")
	for _, row := range repo.rows {
		assert.Equal(t, 3, row.Occurrences)
		assert.Equal(t, string(types.KnowledgeGapOpen), row.Status)
	}
}

// TestObserveIgnoresBlankQueries keeps empty submissions out of the list.
func TestObserveIgnoresBlankQueries(t *testing.T) {
	repo := newStubGapRepo()
	svc := NewKnowledgeGapService(repo)
	gap, err := svc.Observe(context.Background(), 7, "   ", types.KnowledgeGapSourceSearchZeroResult)
	require.NoError(t, err)
	assert.Nil(t, gap)
	assert.Empty(t, repo.rows)
}

// TestUpdateStatusValidates covers the operator path: an unknown status is a bad
// request, and closing links the knowledge that filled the gap.
func TestUpdateStatusValidates(t *testing.T) {
	repo := newStubGapRepo()
	svc := NewKnowledgeGapService(repo)
	ctx := context.Background()
	gap, err := svc.Observe(ctx, 7, "E45 报警", types.KnowledgeGapSourceSearchZeroResult)
	require.NoError(t, err)
	require.NotNil(t, gap)

	_, err = svc.UpdateStatus(ctx, 7, gap.ID, types.KnowledgeGapStatus("bogus"), "")
	require.Error(t, err)

	updated, err := svc.UpdateStatus(ctx, 7, gap.ID, types.KnowledgeGapClosed, "doc-42")
	require.NoError(t, err)
	assert.Equal(t, string(types.KnowledgeGapClosed), updated.Status)
	assert.Equal(t, "doc-42", updated.ResolvedKnowledgeID)
}
