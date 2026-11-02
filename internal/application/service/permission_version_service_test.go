package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// stubPermissionVersions is an in-memory epoch ledger.
type stubPermissionVersions struct {
	versions map[string]int64
	recorded []*types.RevocationPropagation
}

func newStubPermissionVersions() *stubPermissionVersions {
	return &stubPermissionVersions{versions: map[string]int64{}}
}

func epochKey(tenantID uint64, scopeType types.PermissionScopeType, scopeID string) string {
	return string(scopeType) + "/" + scopeID + "@" + itoa(tenantID)
}

func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	out := ""
	for v > 0 {
		out = string(rune('0'+v%10)) + out
		v /= 10
	}
	return out
}

func (s *stubPermissionVersions) Bump(
	_ context.Context, tenantID uint64, scopeType types.PermissionScopeType,
	scopeID, _ string,
) (int64, error) {
	key := epochKey(tenantID, scopeType, scopeID)
	s.versions[key]++
	return s.versions[key], nil
}

func (s *stubPermissionVersions) Get(
	_ context.Context, tenantID uint64, scopeType types.PermissionScopeType, scopeID string,
) (int64, error) {
	return s.versions[epochKey(tenantID, scopeType, scopeID)], nil
}

func (s *stubPermissionVersions) RecordPropagation(
	_ context.Context, row *types.RevocationPropagation,
) error {
	s.recorded = append(s.recorded, row)
	return nil
}

func (s *stubPermissionVersions) ListPropagationsByKnowledge(
	_ context.Context, _ uint64, _ string, _ int,
) ([]*types.RevocationPropagation, error) {
	return s.recorded, nil
}

// TestCacheKeyMovesWhenPermissionChanges covers §2.7: a cache entry must not
// survive a revocation, otherwise a user keeps seeing an answer assembled from
// evidence they can no longer open.
func TestCacheKeyMovesWhenPermissionChanges(t *testing.T) {
	ctx := context.Background()
	store := newStubPermissionVersions()
	svc := NewPermissionVersionService(store)

	before := svc.CacheKey(ctx, 7, "doc-1", "kb-1")
	require.NotEmpty(t, before)

	// Same inputs, same epoch: the cache key must be stable, or nothing would
	// ever hit.
	assert.Equal(t, before, svc.CacheKey(ctx, 7, "doc-1", "kb-1"))

	_, err := svc.BumpForKnowledge(ctx, 7, "doc-1", "kb-1", "acl_revoke")
	require.NoError(t, err)
	after := svc.CacheKey(ctx, 7, "doc-1", "kb-1")
	assert.NotEqual(t, before, after, "revoking a grant must invalidate the answer-cache key")

	// A space-level change must also move the key, because listings and
	// space-scoped caches depend on it.
	spaceBefore := svc.CacheKey(ctx, 7, "doc-2", "kb-1")
	_, err = svc.Bump(ctx, 7, types.PermissionScopeSpace, "kb-1", "share_revoke")
	require.NoError(t, err)
	assert.NotEqual(t, spaceBefore, svc.CacheKey(ctx, 7, "doc-2", "kb-1"))
}

// TestCacheKeyIsTenantScoped keeps two tenants that happen to share document or
// space IDs from colliding in a shared cache.
func TestCacheKeyIsTenantScoped(t *testing.T) {
	ctx := context.Background()
	svc := NewPermissionVersionService(newStubPermissionVersions())
	assert.NotEqual(t, svc.CacheKey(ctx, 1, "doc-1", "kb-1"), svc.CacheKey(ctx, 2, "doc-1", "kb-1"))
}

// TestRecordRevocationCoversEveryOutlet is the §2.6 completion rule: the ledger
// must account for all ten exits, including the ones this deployment does not
// run, so a revocation drill can read one table and see the whole surface.
func TestRecordRevocationCoversEveryOutlet(t *testing.T) {
	ctx := context.Background()
	store := newStubPermissionVersions()
	svc := NewPermissionVersionService(store)

	rows, err := svc.RecordRevocation(ctx, 7, "doc-1", "user-1", types.RevocationTriggerACLRevoke)
	require.NoError(t, err)
	require.Len(t, rows, len(types.AllRevocationOutlets))

	seen := map[string]types.RevocationOutcome{}
	for _, row := range rows {
		require.NotEmpty(t, row.Outlet)
		require.NotEmpty(t, row.Outcome, "every outlet must state an outcome, not be silently omitted")
		assert.Equal(t, string(types.RevocationTriggerACLRevoke), row.Trigger)
		assert.Equal(t, "user-1", row.SubjectID)
		seen[row.Outlet] = types.RevocationOutcome(row.Outcome)
	}
	for _, outlet := range types.AllRevocationOutlets {
		_, ok := seen[string(outlet)]
		assert.True(t, ok, "outlet %s missing from the ledger", outlet)
	}

	// Query-time outlets are already correct because the database is the
	// permission source; the audit snapshot is intentionally retained.
	assert.Equal(t, types.RevocationOutcomeInvalidated, seen[string(types.RevocationOutletSearchIndex)])
	assert.Equal(t, types.RevocationOutcomeInvalidated, seen[string(types.RevocationOutletAnswerCache)])
	assert.Equal(t, types.RevocationOutcomeNotApplicable, seen[string(types.RevocationOutletAudit)])
	// Export/offline artefacts are tracked lists rather than live revocation.
	assert.Equal(t, types.RevocationOutcomePending, seen[string(types.RevocationOutletExport)])
}

// TestBumpRejectsUnknownScope keeps a typo from creating a scope nobody reads.
func TestBumpRejectsUnknownScope(t *testing.T) {
	svc := NewPermissionVersionService(newStubPermissionVersions())
	_, err := svc.Bump(context.Background(), 7, types.PermissionScopeType("bogus"), "x", "test")
	require.Error(t, err)
}

// TestBumpZeroTenantIsNoop keeps unauthenticated contexts from writing epochs.
func TestBumpZeroTenantIsNoop(t *testing.T) {
	svc := NewPermissionVersionService(newStubPermissionVersions())
	version, err := svc.Bump(context.Background(), 0, types.PermissionScopeKnowledge, "doc", "test")
	require.NoError(t, err)
	assert.Zero(t, version)
}

var _ interfaces.PermissionVersionService = (*permissionVersionService)(nil)
