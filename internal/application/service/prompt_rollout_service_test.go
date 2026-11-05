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

// stubRolloutRepo serves pointers and versions.
type stubRolloutRepo struct {
	interfaces.PromptRolloutRepository

	pointers map[string]*types.PromptVersionPointer
	versions map[string]*types.PromptVersion
}

func newStubRolloutRepo() *stubRolloutRepo {
	return &stubRolloutRepo{
		pointers: map[string]*types.PromptVersionPointer{},
		versions: map[string]*types.PromptVersion{},
	}
}

func rolloutKey(tenantID uint64, templateKey string) string {
	return templateKey + "@" + string(rune('0'+tenantID%10))
}

func rolloutVersionKey(tenantID uint64, templateKey string, version int) string {
	return rolloutKey(tenantID, templateKey) + "#" + string(rune('0'+version))
}

func (s *stubRolloutRepo) GetPointer(
	_ context.Context, tenantID uint64, templateKey string,
) (*types.PromptVersionPointer, error) {
	pointer, ok := s.pointers[rolloutKey(tenantID, templateKey)]
	if !ok {
		return nil, nil
	}
	copyOf := *pointer
	return &copyOf, nil
}

func (s *stubRolloutRepo) SavePointer(
	_ context.Context, pointer *types.PromptVersionPointer,
) error {
	copyOf := *pointer
	s.pointers[rolloutKey(pointer.TenantID, pointer.TemplateKey)] = &copyOf
	return nil
}

func (s *stubRolloutRepo) GetVersion(
	_ context.Context, tenantID uint64, templateKey string, version int,
) (*types.PromptVersion, error) {
	row, ok := s.versions[rolloutVersionKey(tenantID, templateKey, version)]
	if !ok {
		return nil, nil
	}
	return row, nil
}

func newRolloutServiceForTest(repo interfaces.PromptRolloutRepository) *promptRolloutService {
	svc := NewPromptRolloutService(repo, nil).(*promptRolloutService)
	svc.now = func() time.Time { return time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC) }
	return svc
}

// TestConfigureRequiresBothVersionsToExist is the silent-fallback guard: a
// pointer to a missing version would serve the latest while the operator
// believes a canary is running.
func TestConfigureRequiresBothVersionsToExist(t *testing.T) {
	repo := newStubRolloutRepo()
	repo.versions[rolloutVersionKey(7, "chat", 2)] = &types.PromptVersion{Version: 2}
	svc := newRolloutServiceForTest(repo)

	// Stable exists, canary does not.
	_, err := svc.Configure(context.Background(), 7, "u-1", types.PromptVersionPointer{
		TemplateKey: "chat", StableVersion: 2, CanaryVersion: 9, CanaryPercent: 10,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not exist")

	// Neither exists.
	_, err = svc.Configure(context.Background(), 7, "u-1", types.PromptVersionPointer{
		TemplateKey: "chat", StableVersion: 8,
	})
	require.Error(t, err)
}

// TestConfigureSavesAValidSplit covers the happy path.
func TestConfigureSavesAValidSplit(t *testing.T) {
	repo := newStubRolloutRepo()
	repo.versions[rolloutVersionKey(7, "chat", 1)] = &types.PromptVersion{Version: 1}
	repo.versions[rolloutVersionKey(7, "chat", 2)] = &types.PromptVersion{Version: 2}
	svc := newRolloutServiceForTest(repo)

	pointer, err := svc.Configure(context.Background(), 7, "u-1", types.PromptVersionPointer{
		TemplateKey: " chat ", StableVersion: 1, CanaryVersion: 2, CanaryPercent: 20,
	})
	require.NoError(t, err)
	assert.Equal(t, "chat", pointer.TemplateKey, "the key is trimmed")
	assert.Equal(t, uint64(7), pointer.TenantID)
	assert.Equal(t, "u-1", pointer.UpdatedBy)
	assert.Equal(t, 2, pointer.CanaryVersion)
}

// TestRollbackMovesThePointerAndClearsTheCanary is the rollback contract.
func TestRollbackMovesThePointerAndClearsTheCanary(t *testing.T) {
	repo := newStubRolloutRepo()
	for _, version := range []int{1, 2, 3} {
		repo.versions[rolloutVersionKey(7, "chat", version)] = &types.PromptVersion{Version: version}
	}
	svc := newRolloutServiceForTest(repo)
	_, err := svc.Configure(context.Background(), 7, "u-1", types.PromptVersionPointer{
		TemplateKey: "chat", StableVersion: 3, CanaryVersion: 2, CanaryPercent: 10,
	})
	require.NoError(t, err)

	pointer, err := svc.Rollback(context.Background(), 7, "u-2", types.PromptRollbackRequest{
		TemplateKey: "chat", ToVersion: 1, Note: "新版本答复明显变短",
	})
	require.NoError(t, err)
	assert.Equal(t, 1, pointer.StableVersion)
	// 回滚同时清灰度：继续把流量分给"刚被判定有问题"的版本是自相矛盾的。
	assert.Zero(t, pointer.CanaryVersion)
	assert.Zero(t, pointer.CanaryPercent)

	stored, err := svc.Get(context.Background(), 7, "chat")
	require.NoError(t, err)
	assert.Equal(t, 1, stored.StableVersion)
}

// TestRollbackRefusesAMissingTarget keeps the pointer from dangling.
func TestRollbackRefusesAMissingTarget(t *testing.T) {
	repo := newStubRolloutRepo()
	repo.versions[rolloutVersionKey(7, "chat", 1)] = &types.PromptVersion{Version: 1}
	svc := newRolloutServiceForTest(repo)

	_, err := svc.Rollback(context.Background(), 7, "u-1", types.PromptRollbackRequest{
		TemplateKey: "chat", ToVersion: 99, Note: "回滚",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not exist")
}

// TestRollbackRequiresANote covers the accountability check at the service layer.
func TestRollbackRequiresANote(t *testing.T) {
	repo := newStubRolloutRepo()
	repo.versions[rolloutVersionKey(7, "chat", 1)] = &types.PromptVersion{Version: 1}
	svc := newRolloutServiceForTest(repo)

	_, err := svc.Rollback(context.Background(), 7, "u-1", types.PromptRollbackRequest{
		TemplateKey: "chat", ToVersion: 1,
	})
	require.Error(t, err)
}

// TestSelectWithoutPointerFallsBackToLatest keeps prior behaviour.
func TestSelectWithoutPointerFallsBackToLatest(t *testing.T) {
	svc := newRolloutServiceForTest(newStubRolloutRepo())
	selection, err := svc.Select(context.Background(), 7, "chat", "sess-1")
	require.NoError(t, err)
	assert.Zero(t, selection.Version)
	assert.Contains(t, selection.Reason, "latest")
}

// TestSelectUsesThePointer covers the configured path.
func TestSelectUsesThePointer(t *testing.T) {
	repo := newStubRolloutRepo()
	for _, version := range []int{1, 2} {
		repo.versions[rolloutVersionKey(7, "chat", version)] = &types.PromptVersion{Version: version}
	}
	svc := newRolloutServiceForTest(repo)
	_, err := svc.Configure(context.Background(), 7, "u-1", types.PromptVersionPointer{
		TemplateKey: "chat", StableVersion: 1, CanaryVersion: 2, CanaryPercent: 100,
	})
	require.NoError(t, err)

	selection, err := svc.Select(context.Background(), 7, "chat", "anyone")
	require.NoError(t, err)
	assert.Equal(t, 2, selection.Version, "100% canary serves the canary version")
	assert.True(t, selection.Canary)
}

// TestGetReturnsAZeroPointerWhenUnset keeps the read path error-free.
func TestGetReturnsAZeroPointerWhenUnset(t *testing.T) {
	svc := newRolloutServiceForTest(newStubRolloutRepo())
	pointer, err := svc.Get(context.Background(), 7, "chat")
	require.NoError(t, err)
	require.NotNil(t, pointer)
	assert.Zero(t, pointer.StableVersion)
	assert.Equal(t, "chat", pointer.TemplateKey)
}

// TestRolloutRequiresTenant refuses unscoped calls.
func TestRolloutRequiresTenant(t *testing.T) {
	svc := newRolloutServiceForTest(newStubRolloutRepo())
	_, err := svc.Configure(context.Background(), 0, "u", types.PromptVersionPointer{})
	require.Error(t, err)
	_, err = svc.Select(context.Background(), 0, "chat", "s")
	require.Error(t, err)
	_, err = svc.Get(context.Background(), 0, "chat")
	require.Error(t, err)
	_, err = svc.Rollback(context.Background(), 0, "u", types.PromptRollbackRequest{})
	require.Error(t, err)
}
