package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// stubBudgetRepo is an in-memory TokenBudgetRepository.
type stubBudgetRepo struct {
	interfaces.TokenBudgetRepository

	budgets   map[uint64]*types.TokenBudget
	chains    []*types.ModelFallbackChain
	notices   map[string]types.TokenBudgetStatus
	used      map[uint64]int64
	budgetErr error
	chainErr  error
	sumErr    error
	noticeErr error
}

func newStubBudgetRepo() *stubBudgetRepo {
	return &stubBudgetRepo{
		budgets: map[uint64]*types.TokenBudget{},
		notices: map[string]types.TokenBudgetStatus{},
		used:    map[uint64]int64{},
	}
}

func noticeKey(tenantID uint64, period types.TokenBudgetPeriod, window time.Time) string {
	return fmt.Sprintf("%d|%s|%s", tenantID, string(period),
		window.UTC().Format(time.RFC3339))
}

func (s *stubBudgetRepo) GetBudget(
	_ context.Context, tenantID uint64,
) (*types.TokenBudget, error) {
	if s.budgetErr != nil {
		return nil, s.budgetErr
	}
	budget, ok := s.budgets[tenantID]
	if !ok {
		return nil, nil
	}
	copyOf := *budget
	return &copyOf, nil
}

func (s *stubBudgetRepo) SaveBudget(_ context.Context, budget *types.TokenBudget) error {
	copyOf := *budget
	s.budgets[budget.TenantID] = &copyOf
	return nil
}

func (s *stubBudgetRepo) DeleteBudget(_ context.Context, tenantID uint64) error {
	delete(s.budgets, tenantID)
	return nil
}

func (s *stubBudgetRepo) SumUsageTokens(
	_ context.Context, tenantID uint64, _ time.Time,
) (int64, error) {
	if s.sumErr != nil {
		return 0, s.sumErr
	}
	return s.used[tenantID], nil
}

func (s *stubBudgetRepo) GetFallbackChain(
	_ context.Context, tenantID uint64, scene string,
) (*types.ModelFallbackChain, error) {
	if s.chainErr != nil {
		return nil, s.chainErr
	}
	for _, chain := range s.chains {
		if chain.TenantID == tenantID && chain.Scene == scene {
			return chain, nil
		}
	}
	return nil, nil
}

func (s *stubBudgetRepo) SaveFallbackChain(
	_ context.Context, chain *types.ModelFallbackChain,
) error {
	for index, existing := range s.chains {
		if existing.TenantID == chain.TenantID && existing.Scene == chain.Scene {
			s.chains[index] = chain
			return nil
		}
	}
	s.chains = append(s.chains, chain)
	return nil
}

func (s *stubBudgetRepo) DeleteFallbackChain(
	_ context.Context, tenantID uint64, scene string,
) error {
	kept := make([]*types.ModelFallbackChain, 0, len(s.chains))
	for _, chain := range s.chains {
		if chain.TenantID == tenantID && chain.Scene == scene {
			continue
		}
		kept = append(kept, chain)
	}
	s.chains = kept
	return nil
}

func (s *stubBudgetRepo) ListFallbackChains(
	_ context.Context, tenantID uint64,
) ([]*types.ModelFallbackChain, error) {
	out := make([]*types.ModelFallbackChain, 0, len(s.chains))
	for _, chain := range s.chains {
		if chain.TenantID == tenantID {
			out = append(out, chain)
		}
	}
	return out, nil
}

func (s *stubBudgetRepo) LoadNotifiedStatus(
	_ context.Context, tenantID uint64, period types.TokenBudgetPeriod, window time.Time,
) (types.TokenBudgetStatus, error) {
	if s.noticeErr != nil {
		return "", s.noticeErr
	}
	return s.notices[noticeKey(tenantID, period, window)], nil
}

func (s *stubBudgetRepo) SaveNotifiedStatus(
	_ context.Context, tenantID uint64, period types.TokenBudgetPeriod,
	window time.Time, status types.TokenBudgetStatus,
) error {
	s.notices[noticeKey(tenantID, period, window)] = status
	return nil
}

func newBudgetServiceForTest(repo interfaces.TokenBudgetRepository) *tokenBudgetService {
	svc := NewTokenBudgetService(repo, nil).(*tokenBudgetService)
	svc.now = func() time.Time {
		return time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	}
	return svc
}

// TestGetBudgetReturnsDefaultWhenUnset keeps "no budget" out of the error path.
func TestGetBudgetReturnsDefaultWhenUnset(t *testing.T) {
	svc := newBudgetServiceForTest(newStubBudgetRepo())
	budget, err := svc.GetBudget(context.Background(), 7)
	require.NoError(t, err)
	require.NotNil(t, budget)
	assert.Zero(t, budget.LimitTokens, "unset means no ceiling")
	assert.Equal(t, types.TokenBudgetMonthly, budget.Period)
	assert.Equal(t, types.DefaultTokenBudgetWarnPercent, budget.WarnPercent)
}

// TestCheckNotifiesOncePerBand is the core alerting rule end to end.
func TestCheckNotifiesOncePerBand(t *testing.T) {
	repo := newStubBudgetRepo()
	repo.budgets[7] = &types.TokenBudget{
		TenantID: 7, Period: types.TokenBudgetDaily, LimitTokens: 1000, WarnPercent: 80,
	}
	svc := newBudgetServiceForTest(repo)
	ctx := context.Background()

	repo.used[7] = 500
	first, err := svc.Check(ctx, 7)
	require.NoError(t, err)
	assert.Equal(t, types.TokenBudgetOK, first.Status)
	assert.False(t, first.ShouldNotify)

	repo.used[7] = 850
	second, err := svc.Check(ctx, 7)
	require.NoError(t, err)
	assert.Equal(t, types.TokenBudgetWarn, second.Status)
	assert.True(t, second.ShouldNotify)

	// Same band: silent.
	repo.used[7] = 900
	third, err := svc.Check(ctx, 7)
	require.NoError(t, err)
	assert.False(t, third.ShouldNotify, "the warn band alerts once")

	repo.used[7] = 1250
	fourth, err := svc.Check(ctx, 7)
	require.NoError(t, err)
	assert.Equal(t, types.TokenBudgetCritical, fourth.Status)
	assert.True(t, fourth.ShouldNotify, "crossing a band alerts again")
	assert.Equal(t, types.TokenBudgetWarn, fourth.PreviousStatus)
}

// TestCheckPersistsTheBandBeforeReturning is what makes restart safe: without
// the write, the next process would alert again immediately.
func TestCheckPersistsTheBandBeforeReturning(t *testing.T) {
	repo := newStubBudgetRepo()
	repo.budgets[7] = &types.TokenBudget{
		TenantID: 7, Period: types.TokenBudgetDaily, LimitTokens: 100, WarnPercent: 80,
	}
	repo.used[7] = 90
	svc := newBudgetServiceForTest(repo)

	assessment, err := svc.Check(context.Background(), 7)
	require.NoError(t, err)
	require.True(t, assessment.ShouldNotify)

	// A fresh service instance (simulating a restart) must not re-alert.
	restarted := newBudgetServiceForTest(repo)
	again, err := restarted.Check(context.Background(), 7)
	require.NoError(t, err)
	assert.False(t, again.ShouldNotify,
		"the band was persisted, so a restart does not repeat the notice")
}

// TestCheckSurvivesUnreadableNoticeLedger prefers a duplicate alert over a
// missed one.
func TestCheckSurvivesUnreadableNoticeLedger(t *testing.T) {
	repo := newStubBudgetRepo()
	repo.budgets[7] = &types.TokenBudget{
		TenantID: 7, Period: types.TokenBudgetDaily, LimitTokens: 100, WarnPercent: 80,
	}
	repo.used[7] = 90
	repo.noticeErr = errors.New("ledger unreadable")
	svc := newBudgetServiceForTest(repo)

	assessment, err := svc.Check(context.Background(), 7)
	require.NoError(t, err)
	assert.True(t, assessment.ShouldNotify,
		"an unreadable ledger means 'not reported yet', not 'stay silent'")
}

// TestCheckWithoutBudgetIsOk proves an unconfigured tenant is not misreported.
func TestCheckWithoutBudgetIsOk(t *testing.T) {
	repo := newStubBudgetRepo()
	repo.used[7] = 1_000_000
	svc := newBudgetServiceForTest(repo)

	assessment, err := svc.Check(context.Background(), 7)
	require.NoError(t, err)
	assert.Equal(t, types.TokenBudgetOK, assessment.Status)
	assert.False(t, assessment.ShouldNotify)
	assert.NotEmpty(t, assessment.Note)
}

// TestSaveBudgetValidatesAndNormalizes covers the service-side guard.
func TestSaveBudgetValidatesAndNormalizes(t *testing.T) {
	svc := newBudgetServiceForTest(newStubBudgetRepo())
	ctx := context.Background()

	_, err := svc.SaveBudget(ctx, 7, types.TokenBudget{Period: "weekly"})
	require.Error(t, err)

	saved, err := svc.SaveBudget(ctx, 7, types.TokenBudget{
		Period: types.TokenBudgetDaily, LimitTokens: 500,
	})
	require.NoError(t, err)
	require.NotNil(t, saved)
	assert.Equal(t, uint64(7), saved.TenantID, "the tenant comes from the context")
	assert.Equal(t, types.DefaultTokenBudgetWarnPercent, saved.WarnPercent)
}

// TestBudgetRequiresTenant refuses an unscoped call.
func TestBudgetRequiresTenant(t *testing.T) {
	svc := newBudgetServiceForTest(newStubBudgetRepo())
	_, err := svc.GetBudget(context.Background(), 0)
	require.Error(t, err)
	_, err = svc.Check(context.Background(), 0)
	require.Error(t, err)
	_, err = svc.SaveBudget(context.Background(), 0, types.TokenBudget{})
	require.Error(t, err)
}

// TestCandidatesFallsBackToPrimaryOnError is the availability rule: a chain
// lookup failure must not break a call that would otherwise succeed.
func TestCandidatesFallsBackToPrimaryOnError(t *testing.T) {
	repo := newStubBudgetRepo()
	repo.chainErr = errors.New("chain table locked")
	svc := newBudgetServiceForTest(repo)

	assert.Equal(t, []string{"primary"},
		svc.Candidates(context.Background(), 7, "chat", "primary"))
}

// TestCandidatesUsesTheConfiguredChain covers the happy path.
func TestCandidatesUsesTheConfiguredChain(t *testing.T) {
	repo := newStubBudgetRepo()
	svc := newBudgetServiceForTest(repo)
	_, err := svc.SaveFallbackChain(context.Background(), 7, types.ModelFallbackChain{
		Scene: "chat", ModelIDs: []string{"m1", "m2"},
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"m1", "m2"},
		svc.Candidates(context.Background(), 7, "chat", "unused"))
	// A different scene has no chain.
	assert.Equal(t, []string{"primary"},
		svc.Candidates(context.Background(), 7, "summary", "primary"))
}

// TestSaveFallbackChainNormalizes covers the service guard.
func TestSaveFallbackChainNormalizes(t *testing.T) {
	svc := newBudgetServiceForTest(newStubBudgetRepo())
	ctx := context.Background()

	_, err := svc.SaveFallbackChain(ctx, 7, types.ModelFallbackChain{Scene: "chat"})
	require.Error(t, err, "a chain with no models is rejected")

	saved, err := svc.SaveFallbackChain(ctx, 7, types.ModelFallbackChain{
		Scene: " chat ", ModelIDs: []string{"m1", " m1 ", "m2"},
	})
	require.NoError(t, err)
	assert.Equal(t, "chat", saved.Scene)
	assert.Equal(t, []string{"m1", "m2"}, saved.ModelIDs)
}

// TestListFallbackChainsReturnsEmptySlice keeps the JSON shape stable.
func TestListFallbackChainsReturnsEmptySlice(t *testing.T) {
	svc := newBudgetServiceForTest(newStubBudgetRepo())
	chains, err := svc.ListFallbackChains(context.Background(), 7)
	require.NoError(t, err)
	assert.NotNil(t, chains, "an empty list must not serialize as null")
	assert.Empty(t, chains)
}

// TestDeleteFallbackChainRequiresScene keeps an empty path parameter out.
func TestDeleteFallbackChainRequiresScene(t *testing.T) {
	svc := newBudgetServiceForTest(newStubBudgetRepo())
	require.Error(t, svc.DeleteFallbackChain(context.Background(), 7, ""))
	require.NoError(t, svc.DeleteFallbackChain(context.Background(), 7, "chat"))
}
