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

// stubVersionRepo serves version history.
type stubVersionRepo struct {
	interfaces.KnowledgeVersionRepository

	rows      []*types.KnowledgeVersion
	createErr error
}

func (s *stubVersionRepo) CreateKnowledgeVersion(
	_ context.Context, version *types.KnowledgeVersion,
) error {
	if s.createErr != nil {
		return s.createErr
	}
	s.rows = append([]*types.KnowledgeVersion{version}, s.rows...)
	return nil
}

func (s *stubVersionRepo) ListKnowledgeVersions(
	_ context.Context, _ string, limit int,
) ([]*types.KnowledgeVersion, error) {
	if limit > 0 && len(s.rows) > limit {
		return s.rows[:limit], nil
	}
	return s.rows, nil
}

func (s *stubVersionRepo) GetLatestKnowledgeVersion(
	_ context.Context, _ string,
) (*types.KnowledgeVersion, error) {
	if len(s.rows) == 0 {
		return nil, nil
	}
	return s.rows[0], nil
}

func (s *stubVersionRepo) GetKnowledgeVersion(
	_ context.Context, _ uint64, _ string, version int,
) (*types.KnowledgeVersion, error) {
	for _, row := range s.rows {
		if row.Version == version {
			return row, nil
		}
	}
	return nil, nil
}

func (s *stubVersionRepo) ListKnowledgeVersionsUpTo(
	_ context.Context, _ string, at time.Time, limit int,
) ([]*types.KnowledgeVersion, error) {
	out := make([]*types.KnowledgeVersion, 0, len(s.rows))
	for _, row := range s.rows {
		if !row.CreatedAt.After(at) {
			out = append(out, row)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// stubVersionKnowledge serves the document lookup.
type stubVersionKnowledge struct {
	interfaces.KnowledgeRepository
	row *types.Knowledge
}

func (s *stubVersionKnowledge) GetKnowledgeByID(
	_ context.Context, _ uint64, id string,
) (*types.Knowledge, error) {
	if s.row == nil || s.row.ID != id {
		return nil, errors.New("not found")
	}
	return s.row, nil
}

// stubVersionLifecycle records the publish the rollback triggers.
type stubVersionLifecycle struct {
	interfaces.KnowledgeLifecycleService
	lastInput *types.KnowledgeLifecycleTransitionInput
	onPublish func(input *types.KnowledgeLifecycleTransitionInput)
	err       error
}

func (s *stubVersionLifecycle) Transition(
	_ context.Context, input *types.KnowledgeLifecycleTransitionInput,
) (*types.Knowledge, error) {
	s.lastInput = input
	if s.err != nil {
		return nil, s.err
	}
	if s.onPublish != nil {
		s.onPublish(input)
	}
	return &types.Knowledge{ID: input.KnowledgeID}, nil
}

func newVersionFixture() (*knowledgeVersionQuery, *stubVersionRepo, *stubVersionLifecycle) {
	repo := &stubVersionRepo{rows: []*types.KnowledgeVersion{
		{
			ID: "v3", TenantID: 7, KnowledgeID: "k-1", Version: 3,
			SemanticVersion: "1.2.0", BumpSize: "minor", Title: "E45 SOP",
			ContentHash: "hash-3", Snapshot: types.JSON(`{"title":"E45 SOP","description":"第一段\n第二段"}`),
			CreatedAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			ID: "v2", TenantID: 7, KnowledgeID: "k-1", Version: 2,
			SemanticVersion: "1.1.0", BumpSize: "minor", Title: "E45 SOP",
			ContentHash: "hash-2", Snapshot: types.JSON(`{"title":"E45 SOP","description":"第一段"}`),
			CreatedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			ID: "v1", TenantID: 7, KnowledgeID: "k-1", Version: 1,
			SemanticVersion: "1.0.0", BumpSize: "patch", Title: "E45 SOP",
			ContentHash: "hash-1", Snapshot: types.JSON(`{"title":"E45 SOP","description":"初版"}`),
			CreatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		},
	}}
	knowledge := &stubVersionKnowledge{row: &types.Knowledge{
		ID: "k-1", TenantID: 7, Title: "E45 SOP", Description: "第一段\n第二段\n第三段",
		FileHash: "hash-current", CurrentVersion: 4,
	}}
	lifecycle := &stubVersionLifecycle{}
	svc := &knowledgeVersionQuery{
		versions:  repo,
		knowledge: knowledge,
		lifecycle: lifecycle,
		now:       func() time.Time { return time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC) },
	}
	return svc, repo, lifecycle
}

// TestDiffDefaultsToPreviousVersion is the common question: "what changed in
// the latest version".
func TestDiffDefaultsToPreviousVersion(t *testing.T) {
	svc, _, _ := newVersionFixture()
	diff, err := svc.Diff(recommendationCaller(), 7, "k-1", "", "3")
	require.NoError(t, err)
	assert.Equal(t, "1.1.0", diff.Left)
	assert.Equal(t, "1.2.0", diff.Right)
	assert.Equal(t, 1, diff.Added, "第二段 was added")
	assert.Zero(t, diff.Removed)
	assert.True(t, diff.Changed)
}

// TestDiffResolvesSemanticAndCurrentRefs covers both ref forms.
func TestDiffResolvesSemanticAndCurrentRefs(t *testing.T) {
	svc, _, _ := newVersionFixture()
	diff, err := svc.Diff(recommendationCaller(), 7, "k-1", "1.0.0", "current")
	require.NoError(t, err)
	assert.Equal(t, "1.0.0", diff.Left)
	assert.Equal(t, "current", diff.Right)
	assert.Equal(t, 3, diff.Added)
	assert.Equal(t, 1, diff.Removed)
}

// TestDiffUnknownVersionIs404 covers the lookup miss.
func TestDiffUnknownVersionIs404(t *testing.T) {
	svc, _, _ := newVersionFixture()
	_, err := svc.Diff(recommendationCaller(), 7, "k-1", "9.9.9", "current")
	require.Error(t, err)
}

// TestDiffRejectsANonNumericRef covers the input guard.
func TestDiffRejectsANonNumericRef(t *testing.T) {
	svc, _, _ := newVersionFixture()
	_, err := svc.Diff(recommendationCaller(), 7, "k-1", "first", "current")
	require.Error(t, err)
}

// TestAsOfResolvesTheVersionInForce is the §7.4 as-of query.
func TestAsOfResolvesTheVersionInForce(t *testing.T) {
	svc, _, _ := newVersionFixture()

	// Between v2 (Oct 1) and v3 (Nov 1) the answer is v2.
	result, err := svc.AsOf(recommendationCaller(), 7, "k-1",
		time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.NotNil(t, result.Version)
	assert.Equal(t, 2, result.Version.Version)
	assert.Equal(t, "1.1.0", result.Version.SemanticVersion)

	// Before the first version there is nothing, and the note says why.
	result, err = svc.AsOf(recommendationCaller(), 7, "k-1",
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.Nil(t, result.Version)
	assert.NotEmpty(t, result.Note)
}

// TestAsOfRequiresAnInstant covers the guard.
func TestAsOfRequiresAnInstant(t *testing.T) {
	svc, _, _ := newVersionFixture()
	_, err := svc.AsOf(recommendationCaller(), 7, "k-1", time.Time{})
	require.Error(t, err)
}

// TestRollbackCreatesANewVersionInsteadOfDeleting is the core §7.4 property:
// history stays queryable, so a rollback is a new version that names what it
// restored.
func TestRollbackCreatesANewVersionInsteadOfDeleting(t *testing.T) {
	svc, repo, lifecycle := newVersionFixture()
	lifecycle.onPublish = func(input *types.KnowledgeLifecycleTransitionInput) {
		// The lifecycle service writes the snapshot; emulate that.
		repo.rows = append([]*types.KnowledgeVersion{{
			ID: "v4", TenantID: 7, KnowledgeID: "k-1", Version: 4,
			SemanticVersion: "1.2.1", BumpSize: "patch", Title: "E45 SOP",
			ContentHash: "hash-2", RolledBackFrom: "1.1.0",
			Snapshot:  types.JSON(`{"title":"E45 SOP","description":"第一段"}`),
			CreatedAt: time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC),
		}}, repo.rows...)
	}

	result, err := svc.Rollback(recommendationCaller(), 7, types.VersionRollbackInput{
		KnowledgeID: "k-1", TargetVersion: "1.1.0", ChangeNote: "参数写错了，回退到旧口径",
	})
	require.NoError(t, err)
	require.NotNil(t, result.NewVersion)
	assert.Equal(t, "1.1.0", result.RestoredFrom)
	assert.Equal(t, "1.2.0", result.PreviousVersion)
	assert.Equal(t, 4, result.NewVersion.Version)
	assert.Equal(t, 4, len(repo.rows), "the old versions are still there")
	assert.Equal(t, "1.2.0", repo.rows[1].SemanticVersion, "v3 was not deleted")

	// The rollback went through the lifecycle publish, so the state machine
	// and receipt checks still apply.
	require.NotNil(t, lifecycle.lastInput)
	assert.Equal(t, types.KnowledgeLifecycleActionPublish, lifecycle.lastInput.Action)
	assert.Equal(t, "1.1.0", lifecycle.lastInput.RolledBackFrom)
	assert.Equal(t, string(types.VersionBumpPatch), lifecycle.lastInput.VersionBump)
}

// TestRollbackRejectsTheLatestVersion keeps a no-op rollback out.
func TestRollbackRejectsTheLatestVersion(t *testing.T) {
	svc, _, _ := newVersionFixture()
	_, err := svc.Rollback(recommendationCaller(), 7, types.VersionRollbackInput{
		KnowledgeID: "k-1", TargetVersion: "1.2.0", ChangeNote: "already here",
	})
	require.Error(t, err)
}

// TestRollbackRequiresAReason covers the audit requirement.
func TestRollbackRequiresAReason(t *testing.T) {
	svc, _, _ := newVersionFixture()
	_, err := svc.Rollback(recommendationCaller(), 7, types.VersionRollbackInput{
		KnowledgeID: "k-1", TargetVersion: "1.1.0",
	})
	require.Error(t, err)
}

// TestRollbackPropagatesLifecycleFailure keeps a refused publish from looking
// like a successful rollback.
func TestRollbackPropagatesLifecycleFailure(t *testing.T) {
	svc, _, lifecycle := newVersionFixture()
	lifecycle.err = errors.New("审批凭证缺失")
	_, err := svc.Rollback(recommendationCaller(), 7, types.VersionRollbackInput{
		KnowledgeID: "k-1", TargetVersion: "1.1.0", ChangeNote: "回退",
	})
	require.Error(t, err)
}
