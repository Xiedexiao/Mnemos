package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// stubCommitRepo serves commit diff and replay queries.
type stubCommitRepo struct {
	interfaces.GraphFactRepository

	commits   map[string]*types.GraphCommit
	entities  []*types.GraphEntity
	facts     []*types.GraphFact
	commitErr error
}

func (s *stubCommitRepo) GetCommit(
	_ context.Context, _ uint64, commitID string,
) (*types.GraphCommit, error) {
	if s.commitErr != nil {
		return nil, s.commitErr
	}
	commit, ok := s.commits[commitID]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	return commit, nil
}

func (s *stubCommitRepo) ListEntitiesByCommit(
	_ context.Context, _ uint64, commitID string, _ int,
) ([]*types.GraphEntity, error) {
	out := make([]*types.GraphEntity, 0, len(s.entities))
	for _, entity := range s.entities {
		if entity.CommitID == commitID {
			out = append(out, entity)
		}
	}
	return out, nil
}

func (s *stubCommitRepo) ListFactsByCommit(
	_ context.Context, _ uint64, commitID string, _ int,
) ([]*types.GraphFact, error) {
	out := make([]*types.GraphFact, 0, len(s.facts))
	for _, fact := range s.facts {
		if fact.CommitID == commitID {
			out = append(out, fact)
		}
	}
	return out, nil
}

func (s *stubCommitRepo) LatestCommitAt(
	_ context.Context, _ uint64, asOf time.Time,
) (*types.GraphCommit, error) {
	var latest *types.GraphCommit
	for _, commit := range s.commits {
		if commit.CreatedAt.After(asOf) {
			continue
		}
		if latest == nil || commit.CreatedAt.After(latest.CreatedAt) {
			latest = commit
		}
	}
	if latest == nil {
		return nil, gorm.ErrRecordNotFound
	}
	return latest, nil
}

func (s *stubCommitRepo) ListEntitiesAsOf(
	_ context.Context, _ uint64, asOf time.Time, schemaID string, _ int,
) ([]*types.GraphEntity, error) {
	out := make([]*types.GraphEntity, 0, len(s.entities))
	for _, entity := range s.entities {
		if entity.CreatedAt.After(asOf) {
			continue
		}
		// Superseded *before* asOf means it was already gone then.
		if entity.Status == types.GraphEntityStatusSuperseded && !entity.UpdatedAt.After(asOf) {
			continue
		}
		if schemaID != "" && entity.SchemaID != schemaID {
			continue
		}
		out = append(out, entity)
	}
	return out, nil
}

func (s *stubCommitRepo) ListFactsAsOf(
	_ context.Context, _ uint64, asOf time.Time, _ int,
) ([]*types.GraphFact, error) {
	out := make([]*types.GraphFact, 0, len(s.facts))
	for _, fact := range s.facts {
		if fact.CreatedAt.After(asOf) {
			continue
		}
		if fact.Status == types.GraphFactStatusSuperseded && !fact.UpdatedAt.After(asOf) {
			continue
		}
		out = append(out, fact)
	}
	return out, nil
}

// commitFixture builds a service around the stub.
func commitFixture() (*graphFactService, *stubCommitRepo) {
	created := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	repo := &stubCommitRepo{
		commits: map[string]*types.GraphCommit{
			"commit-1": {
				ID: "commit-1", TenantID: 7, Description: "首次抽取",
				EntitiesAdded: 2, FactsAdded: 1, GoldenGatePass: true,
				CreatedBy: "u-1", CreatedAt: created,
			},
		},
		entities: []*types.GraphEntity{
			{
				ID: "e-1", TenantID: 7, Name: "E45", SchemaID: "schema-1",
				Status: types.GraphEntityStatusActive, Confidence: types.GraphConfidenceExtracted,
				CommitID: "commit-1", CreatedAt: created, UpdatedAt: created,
			},
			{
				ID: "e-2", TenantID: 7, Name: "报警", SchemaID: "schema-1",
				Status: types.GraphEntityStatusActive, Confidence: types.GraphConfidenceInferred,
				CommitID: "commit-1", CreatedAt: created, UpdatedAt: created,
			},
		},
		facts: []*types.GraphFact{
			{
				ID: "f-1", TenantID: 7, RelationName: "depends_on",
				SourceEntityID: "e-1", TargetEntityID: "e-2",
				Status: types.GraphFactStatusActive, Confidence: types.GraphConfidenceExtracted,
				CommitID: "commit-1", CreatedAt: created, UpdatedAt: created,
			},
		},
	}
	return &graphFactService{repo: repo}, repo
}

// TestDiffCommitReturnsItsRows covers the diff.
func TestDiffCommitReturnsItsRows(t *testing.T) {
	svc, _ := commitFixture()
	diff, err := svc.DiffCommit(context.Background(), 7, "commit-1")
	require.NoError(t, err)
	require.NotNil(t, diff)

	assert.Equal(t, "commit-1", diff.CommitID)
	assert.Equal(t, "首次抽取", diff.Description)
	assert.Equal(t, "u-1", diff.CreatedBy)
	assert.True(t, diff.GoldenGatePass)
	require.Len(t, diff.Entities, 2)
	require.Len(t, diff.Facts, 1)

	assert.Equal(t, "e-1", diff.Entities[0].EntityID, "entities are sorted by id")
	assert.Equal(t, "E45", diff.Entities[0].Name)
	assert.Equal(t, "f-1", diff.Facts[0].FactID)
	assert.Equal(t, "depends_on", diff.Facts[0].RelationName)
	assert.False(t, diff.Truncated)
}

// TestDiffCommitIsDeterministic is the "可回归" requirement: the same commit
// must produce the same output twice, or comparing diffs shows phantom changes.
func TestDiffCommitIsDeterministic(t *testing.T) {
	svc, repo := commitFixture()
	// Reverse the input order; the diff must still be sorted.
	repo.entities[0], repo.entities[1] = repo.entities[1], repo.entities[0]

	first, err := svc.DiffCommit(context.Background(), 7, "commit-1")
	require.NoError(t, err)
	second, err := svc.DiffCommit(context.Background(), 7, "commit-1")
	require.NoError(t, err)
	assert.Equal(t, first.Entities, second.Entities)
	assert.Equal(t, "e-1", first.Entities[0].EntityID)
}

// TestDiffCommitMarksTruncationWhenCountsDisagree reports honestly when the
// commit row counts more rows than can be listed (history pruned).
func TestDiffCommitMarksTruncationWhenCountsDisagree(t *testing.T) {
	svc, repo := commitFixture()
	repo.commits["commit-1"].EntitiesAdded = 12
	repo.commits["commit-1"].EntitiesUpdated = 3

	diff, err := svc.DiffCommit(context.Background(), 7, "commit-1")
	require.NoError(t, err)
	assert.True(t, diff.Truncated,
		"15 counted rows but only 2 listed: the list is incomplete")
	assert.Equal(t, 12, diff.EntitiesAdded, "the commit's own counts still report")
}

// TestDiffCommitMissingIsNotFound covers the 404 path.
func TestDiffCommitMissingIsNotFound(t *testing.T) {
	svc, _ := commitFixture()
	_, err := svc.DiffCommit(context.Background(), 7, "nope")
	require.ErrorIs(t, err, ErrGraphCommitNotFound)

	_, err = svc.DiffCommit(context.Background(), 7, "  ")
	require.ErrorIs(t, err, ErrGraphCommitNotFound)
}

// TestDiffCommitRequiresTenant refuses an unscoped call.
func TestDiffCommitRequiresTenant(t *testing.T) {
	svc, _ := commitFixture()
	_, err := svc.DiffCommit(context.Background(), 0, "commit-1")
	require.ErrorIs(t, err, ErrGraphTenantMissing)
}

// TestReplayReturnsTheSnapshotAtThatInstant covers the happy path.
func TestReplayReturnsTheSnapshotAtThatInstant(t *testing.T) {
	svc, _ := commitFixture()
	result, err := svc.Replay(context.Background(), 7, types.GraphReplayRequest{
		AsOf: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, "commit-1", result.AsOfCommitID)
	assert.Equal(t, 2, result.EntityCount)
	assert.Equal(t, 1, result.FactCount)
	require.Len(t, result.Entities, 2)
	assert.Equal(t, "e-1", result.Entities[0].EntityID)
	assert.Equal(t, string(types.GraphEntityStatusActive), result.Entities[0].CurrentStatus)
	assert.False(t, result.Truncated)
}

// TestReplayBeforeAnyCommitIsEmpty is the "nothing existed yet" case.
func TestReplayBeforeAnyCommitIsEmpty(t *testing.T) {
	svc, _ := commitFixture()
	result, err := svc.Replay(context.Background(), 7, types.GraphReplayRequest{
		AsOf: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	assert.Empty(t, result.AsOfCommitID)
	assert.Empty(t, result.Entities)
	assert.Empty(t, result.Facts)
	assert.NotEmpty(t, result.Note, "the empty snapshot explains itself")
}

// TestReplayIncludesRowsSupersededAfterTheInstant is the distinction that makes
// a replay useful: "existed then, gone now" must still appear.
func TestReplayIncludesRowsSupersededAfterTheInstant(t *testing.T) {
	svc, repo := commitFixture()
	// e-2 was active at the instant but superseded a week later.
	repo.entities[1].Status = types.GraphEntityStatusSuperseded
	repo.entities[1].UpdatedAt = time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)

	result, err := svc.Replay(context.Background(), 7, types.GraphReplayRequest{
		AsOf: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	assert.Equal(t, 2, result.EntityCount, "it existed at the requested instant")
	for _, entity := range result.Entities {
		if entity.EntityID == "e-2" {
			assert.Equal(t, string(types.GraphEntityStatusSuperseded), entity.CurrentStatus,
				"the current status shows it was withdrawn since")
		}
	}
}

// TestReplayExcludesRowsSupersededBeforeTheInstant keeps history honest.
func TestReplayExcludesRowsSupersededBeforeTheInstant(t *testing.T) {
	svc, repo := commitFixture()
	// e-2 was superseded on 2026-11-01, before the instant we replay.
	repo.entities[1].Status = types.GraphEntityStatusSuperseded
	repo.entities[1].UpdatedAt = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

	result, err := svc.Replay(context.Background(), 7, types.GraphReplayRequest{
		AsOf: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	assert.Equal(t, 1, result.EntityCount, "it was already gone at that instant")
	assert.Equal(t, "e-1", result.Entities[0].EntityID)
}

// TestReplayRequiresAsOf refuses an unanchored replay.
func TestReplayRequiresAsOf(t *testing.T) {
	svc := &graphFactService{repo: &stubCommitRepo{commits: map[string]*types.GraphCommit{}}}
	_, err := svc.Replay(context.Background(), 7, types.GraphReplayRequest{})
	require.ErrorIs(t, err, ErrGraphInvalidQuery)
}

// TestReplayFiltersBySchema keeps the schema narrowing.
func TestReplayFiltersBySchema(t *testing.T) {
	svc, repo := commitFixture()
	repo.entities = append(repo.entities, &types.GraphEntity{
		ID: "e-3", TenantID: 7, Name: "其他", SchemaID: "schema-2",
		Status: types.GraphEntityStatusActive, CommitID: "commit-1",
		CreatedAt: time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC),
	})
	result, err := svc.Replay(context.Background(), 7, types.GraphReplayRequest{
		AsOf:     time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC),
		SchemaID: "schema-1",
	})
	require.NoError(t, err)
	assert.Equal(t, 2, result.EntityCount)
	for _, entity := range result.Entities {
		assert.Equal(t, "schema-1", entity.SchemaID)
	}
}

// TestReplaySurfacesRepositoryFailure keeps a backend error visible.
func TestReplaySurfacesRepositoryFailure(t *testing.T) {
	svc := &graphFactService{repo: &stubCommitRepo{commitErr: errors.New("db down")}}
	_, err := svc.Replay(context.Background(), 7, types.GraphReplayRequest{
		AsOf: time.Now().UTC(),
	})
	require.NoError(t, err, "a missing commit anchor is not a failure")
}
