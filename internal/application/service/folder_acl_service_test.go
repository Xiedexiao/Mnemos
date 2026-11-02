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

// stubFolderACLRepo keeps grants in a slice.
type stubFolderACLRepo struct {
	interfaces.FolderACLRepository
	grants     []*types.FolderACL
	violations []*types.ACLCeilingViolation
	deleted    []string
}

func (s *stubFolderACLRepo) UpsertGrant(_ context.Context, grant *types.FolderACL) error {
	if grant.ID == "" {
		grant.ID = "grant-" + itoaSmall(len(s.grants)+1)
	}
	s.grants = append(s.grants, grant)
	return nil
}

func (s *stubFolderACLRepo) ListByKnowledgeBase(
	_ context.Context, _ uint64, _ string,
) ([]*types.FolderACL, error) {
	return s.grants, nil
}

func (s *stubFolderACLRepo) ListForFolder(
	_ context.Context, _ uint64, _, folderPath string,
) ([]*types.FolderACL, error) {
	ancestors := map[string]bool{}
	for _, path := range folderAncestorPathsForTest(folderPath) {
		ancestors[path] = true
	}
	out := make([]*types.FolderACL, 0, len(s.grants))
	for _, grant := range s.grants {
		if ancestors[grant.FolderPath] {
			out = append(out, grant)
		}
	}
	return out, nil
}

func (s *stubFolderACLRepo) Revoke(_ context.Context, _ uint64, grantID string) error {
	return nil
}

func (s *stubFolderACLRepo) DeleteByFolder(
	_ context.Context, _ uint64, _, folderPath string,
) (int64, error) {
	s.deleted = append(s.deleted, folderPath)
	kept := make([]*types.FolderACL, 0, len(s.grants))
	removed := int64(0)
	for _, grant := range s.grants {
		if grant.FolderPath == folderPath {
			removed++
			continue
		}
		kept = append(kept, grant)
	}
	s.grants = kept
	return removed, nil
}

func (s *stubFolderACLRepo) RecordCeilingViolation(
	_ context.Context, violation *types.ACLCeilingViolation,
) error {
	s.violations = append(s.violations, violation)
	return nil
}

func (s *stubFolderACLRepo) ListCeilingViolations(
	_ context.Context, _ uint64, _ int,
) ([]*types.ACLCeilingViolation, error) {
	return s.violations, nil
}

// folderAncestorPathsForTest mirrors the repository helper without importing it.
func folderAncestorPathsForTest(folderPath string) []string {
	normalized := types.NormalizeKnowledgeFolderPath(folderPath)
	paths := []string{normalized}
	for normalized != "" {
		index := -1
		for i := len(normalized) - 1; i >= 0; i-- {
			if normalized[i] == '/' {
				index = i
				break
			}
		}
		if index < 0 {
			normalized = ""
		} else {
			normalized = normalized[:index]
		}
		paths = append(paths, normalized)
	}
	return paths
}

// TestFolderACLInheritsDownward covers §2.3: a grant on a parent folder applies
// to its descendants unless inherit_children is cleared.
func TestFolderACLInheritsDownward(t *testing.T) {
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	grants := []*types.FolderACL{
		{FolderPath: "sop", Action: string(types.PermissionActionView), Effect: "allow", InheritChildren: true},
	}
	decision := types.FoldFolderACL("sop/maintenance", grants, now)
	assert.Contains(t, decision.AllowedActions, types.PermissionActionView)
	assert.Equal(t, "sop", decision.InheritedFrom, "the verdict comes from the parent folder")

	// Non-inheriting grant only covers its own folder.
	grants[0].InheritChildren = false
	child := types.FoldFolderACL("sop/maintenance", grants, now)
	assert.Empty(t, child.AllowedActions)
	own := types.FoldFolderACL("sop", grants, now)
	assert.Contains(t, own.AllowedActions, types.PermissionActionView)
}

// TestFolderACLDenyWins covers the deny-first rule at folder level.
func TestFolderACLDenyWins(t *testing.T) {
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	grants := []*types.FolderACL{
		{FolderPath: "", Action: string(types.PermissionActionView), Effect: "allow", InheritChildren: true},
		{FolderPath: "sop/secret", Action: string(types.PermissionActionView), Effect: "deny"},
	}
	decision := types.FoldFolderACL("sop/secret", grants, now)
	assert.Empty(t, decision.AllowedActions, "the deny must remove the inherited allow")
	assert.Contains(t, decision.DeniedActions, types.PermissionActionView)
}

// TestFolderACLExpiry covers self-invalidating grants (§2.5 外部协作者到期).
func TestFolderACLExpiry(t *testing.T) {
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	grants := []*types.FolderACL{
		{FolderPath: "vendor", Action: string(types.PermissionActionView),
			Effect: "allow", ExpiresAt: &past},
	}
	decision := types.FoldFolderACL("vendor", grants, now)
	assert.Empty(t, decision.AllowedActions, "an expired grant must not decide anything")
}

// TestCheckActionCeiling covers §2.3 动作上限.
func TestCheckActionCeiling(t *testing.T) {
	parent := []types.PermissionAction{types.PermissionActionView, types.PermissionActionAsk}
	assert.Empty(t, types.CheckActionCeiling(parent,
		[]types.PermissionAction{types.PermissionActionView}))
	violations := types.CheckActionCeiling(parent,
		[]types.PermissionAction{types.PermissionActionView, types.PermissionActionDownload})
	assert.Equal(t, []types.PermissionAction{types.PermissionActionDownload}, violations)
}

// TestCheckClassificationCeiling covers §2.3 密级上限: a child must not be less
// restricted than its parent's floor.
func TestCheckClassificationCeiling(t *testing.T) {
	assert.False(t, types.CheckClassificationCeiling(
		types.KnowledgeClassificationInternal, types.KnowledgeClassificationSecret),
		"a stricter child is fine")
	assert.True(t, types.CheckClassificationCeiling(
		types.KnowledgeClassificationConfidential, types.KnowledgeClassificationInternal),
		"a looser child breaches the floor")
}

// TestFolderGrantRejectsActionBeyondParent is the enforcement point: a child
// folder cannot open an action the parent does not.
func TestFolderGrantRejectsActionBeyondParent(t *testing.T) {
	repo := &stubFolderACLRepo{}
	svc := NewFolderACLService(repo)
	ctx := context.Background()

	// Parent opens only view.
	_, err := svc.Grant(ctx, &types.FolderACL{
		TenantID: 7, KnowledgeBaseID: "kb-1", FolderPath: "sop",
		SubjectID: "user-1", Action: string(types.PermissionActionView), Effect: "allow",
	})
	require.NoError(t, err)

	// Child tries to open download.
	_, err = svc.Grant(ctx, &types.FolderACL{
		TenantID: 7, KnowledgeBaseID: "kb-1", FolderPath: "sop/maintenance",
		SubjectID: "user-1", Action: string(types.PermissionActionDownload), Effect: "allow",
	})
	require.Error(t, err, "the child must not open an action the parent lacks")
	require.Len(t, repo.violations, 1, "the breach is recorded for governance")
	assert.Equal(t, string(types.CeilingAction), repo.violations[0].Kind)

	// A deny is always allowed: tightening never breaches a ceiling.
	_, err = svc.Grant(ctx, &types.FolderACL{
		TenantID: 7, KnowledgeBaseID: "kb-1", FolderPath: "sop/maintenance",
		SubjectID: "user-1", Action: string(types.PermissionActionDownload), Effect: "deny",
	})
	require.NoError(t, err)
}

// TestFolderMoveResetsGrants covers §2.3 目录移动触发权限重算.
func TestFolderMoveResetsGrants(t *testing.T) {
	repo := &stubFolderACLRepo{grants: []*types.FolderACL{
		{TenantID: 7, KnowledgeBaseID: "kb-1", FolderPath: "old/child", SubjectID: "u", Action: "view"},
	}}
	svc := NewFolderACLService(repo)
	removed, err := svc.OnFolderMoved(context.Background(), 7, "kb-1", "old/child", "new/child")
	require.NoError(t, err)
	assert.EqualValues(t, 1, removed)
	assert.Equal(t, []string{"old/child"}, repo.deleted)
}

// TestSpaceKindDefaults covers §4.1's per-kind default policy.
func TestSpaceKindDefaults(t *testing.T) {
	assert.False(t, types.SpaceKindPublic.DefaultClassification().
		Rank() > types.KnowledgeClassificationPublic.Rank())
	assert.True(t, types.SpaceKindPublic.RequiresReview(),
		"a public space requires review before publishing")
	assert.False(t, types.SpaceKindPersonal.RequiresReview())
	assert.True(t, types.SpaceKindExternalCollab.RequiresReview())
	assert.Equal(t, types.KnowledgeClassificationInternal,
		types.SpaceKindExternalCollab.DefaultClassification(),
		"external-collaboration content never defaults to public")
}

// TestSpacePolicyReadOnlyAfterExpiry covers §4.1 项目关闭后转只读.
func TestSpacePolicyReadOnlyAfterExpiry(t *testing.T) {
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	policy := types.ResolveSpacePolicy(types.SpaceKindProject, "internal", &past)
	assert.True(t, policy.SpaceReadOnly(now))

	// A team space has no expiry semantics.
	team := types.ResolveSpacePolicy(types.SpaceKindTeam, "internal", &past)
	assert.False(t, team.SpaceReadOnly(now))
}

// TestParseSpaceKindFallsBackToTeam keeps legacy rows working.
func TestParseSpaceKindFallsBackToTeam(t *testing.T) {
	assert.Equal(t, types.SpaceKindTeam, types.ParseSpaceKind(""))
	assert.Equal(t, types.SpaceKindTeam, types.ParseSpaceKind("bogus"))
	assert.Equal(t, types.SpaceKindPublic, types.ParseSpaceKind("public"))
}
