package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// stubSignalRepo keeps signals in memory and honours the pending-dedupe rule.
type stubSignalRepo struct {
	interfaces.ChangeSignalRepository
	rows []*types.ChangeSignal
}

func (s *stubSignalRepo) Create(_ context.Context, signal *types.ChangeSignal) (bool, error) {
	if signal.DedupeKey == "" {
		signal.DedupeKey = types.ChangeSignalDedupeKey(signal.TenantID,
			signal.SignalType, signal.SourceID, signal.KnowledgeID)
	}
	for _, existing := range s.rows {
		if existing.Status == string(types.ChangeSignalPending) && existing.DedupeKey == signal.DedupeKey {
			return false, nil
		}
	}
	signal.ID = "sig-" + itoaSmall(len(s.rows)+1)
	s.rows = append(s.rows, signal)
	return true, nil
}

func (s *stubSignalRepo) List(
	_ context.Context, _ uint64, status types.ChangeSignalStatus,
	signalType types.ChangeSignalType, _ int,
) ([]*types.ChangeSignal, error) {
	out := make([]*types.ChangeSignal, 0, len(s.rows))
	for _, row := range s.rows {
		if status != "" && row.Status != string(status) {
			continue
		}
		if signalType != "" && row.SignalType != string(signalType) {
			continue
		}
		out = append(out, row)
	}
	return out, nil
}

func (s *stubSignalRepo) Get(
	_ context.Context, _ uint64, signalID string,
) (*types.ChangeSignal, error) {
	for _, row := range s.rows {
		if row.ID == signalID {
			return row, nil
		}
	}
	return nil, assert.AnError
}

func (s *stubSignalRepo) MarkConverted(
	_ context.Context, _ uint64, signalID, changeRequestID, actorID string,
) (*types.ChangeSignal, error) {
	for _, row := range s.rows {
		if row.ID == signalID {
			row.Status = string(types.ChangeSignalConverted)
			row.ChangeRequestID = changeRequestID
			row.HandledBy = actorID
			return row, nil
		}
	}
	return nil, assert.AnError
}

func (s *stubSignalRepo) Dismiss(
	_ context.Context, _ uint64, signalID, actorID string,
) (*types.ChangeSignal, error) {
	for _, row := range s.rows {
		if row.ID == signalID {
			row.Status = string(types.ChangeSignalDismissed)
			row.HandledBy = actorID
			return row, nil
		}
	}
	return nil, assert.AnError
}

func (s *stubSignalRepo) CountPending(_ context.Context, _ uint64) (int64, error) {
	count := int64(0)
	for _, row := range s.rows {
		if row.Status == string(types.ChangeSignalPending) {
			count++
		}
	}
	return count, nil
}

// stubSignalChangeRequests records the drafts a signal produces.
type stubSignalChangeRequests struct {
	interfaces.ChangeRequestService
	inputs []types.ChangeRequestCreateInput
}

func (s *stubSignalChangeRequests) CreateCR(
	_ context.Context, input types.ChangeRequestCreateInput,
) (*types.ChangeRequest, error) {
	s.inputs = append(s.inputs, input)
	return &types.ChangeRequest{ID: "cr-1", Title: input.Title}, nil
}

func newSignalService() (*changeSignalService, *stubSignalRepo, *stubSignalChangeRequests) {
	repo := &stubSignalRepo{}
	crs := &stubSignalChangeRequests{}
	return &changeSignalService{repo: repo, changeReqs: crs}, repo, crs
}

// TestAllSevenSignalTypesAreValid pins §7.3's catalogue.
func TestAllSevenSignalTypesAreValid(t *testing.T) {
	all := []types.ChangeSignalType{
		types.ChangeSignalInternalEvent, types.ChangeSignalSourceSystem,
		types.ChangeSignalExternalSubscription, types.ChangeSignalDependency,
		types.ChangeSignalUserFeedback, types.ChangeSignalConflict,
		types.ChangeSignalConnectorHealth,
	}
	assert.Len(t, all, 7, "§7.3 lists seven signal classes")
	for _, signalType := range all {
		assert.True(t, signalType.Valid(), "%s must be valid", signalType)
		assert.NotEmpty(t, signalType.DefaultAction(), "%s needs a default action", signalType)
		assert.NotEmpty(t, signalType.DefaultRiskLevel(), "%s needs a default risk", signalType)
		assert.NotEmpty(t, signalType.DefaultChangeType(), "%s needs a default change type", signalType)
	}
	assert.False(t, types.ChangeSignalType("bogus").Valid())
}

// TestSignalRiskFloor holds the important safety property: a regulatory or
// conflict signal must never draft a low-risk change, because that is exactly
// the mislabelling §6.4 forbids.
func TestSignalRiskFloor(t *testing.T) {
	assert.Equal(t, types.ChangeRiskHigh,
		types.ChangeSignalExternalSubscription.DefaultRiskLevel())
	assert.Equal(t, types.ChangeRiskHigh, types.ChangeSignalConflict.DefaultRiskLevel())
	assert.Equal(t, types.ChangeRiskLow, types.ChangeSignalConnectorHealth.DefaultRiskLevel())
}

// TestSourceSystemSignalDraftsDelist covers the default change type mapping.
func TestSourceSystemSignalDraftsDelist(t *testing.T) {
	assert.Equal(t, types.ChangeTypeDelist, types.ChangeSignalSourceSystem.DefaultChangeType())
	assert.Equal(t, types.ChangeTypeContentRevision,
		types.ChangeSignalExternalSubscription.DefaultChangeType())
}

// TestCollectDeduplicatesPendingSignals is the noise guard: a connector that
// fails every sync must not append a row per sync.
func TestCollectDeduplicatesPendingSignals(t *testing.T) {
	svc, repo, _ := newSignalService()
	ctx := context.Background()
	signal := func() *types.ChangeSignal {
		return &types.ChangeSignal{
			TenantID:   7,
			SignalType: string(types.ChangeSignalConnectorHealth),
			SourceID:   "ds-1",
			Summary:    "sync failed",
		}
	}
	_, err := svc.Collect(ctx, signal())
	require.NoError(t, err)
	_, err = svc.Collect(ctx, signal())
	require.NoError(t, err)
	assert.Len(t, repo.rows, 1, "the same pending signal is not duplicated")
}

// TestCollectValidatesSignalType keeps an unknown type out of the queue.
func TestCollectValidatesSignalType(t *testing.T) {
	svc, _, _ := newSignalService()
	_, err := svc.Collect(context.Background(), &types.ChangeSignal{
		TenantID:   7,
		SignalType: "invented",
	})
	require.Error(t, err)
}

// TestConvertCreatesDraftWithSignalDefaults covers §7.3's default action: the
// draft inherits the signal's risk and change type rather than starting blank.
func TestConvertCreatesDraftWithSignalDefaults(t *testing.T) {
	svc, repo, crs := newSignalService()
	ctx := context.Background()
	_, err := svc.Collect(ctx, &types.ChangeSignal{
		TenantID:   7,
		SignalType: string(types.ChangeSignalExternalSubscription),
		SourceID:   "GB-50316",
		Summary:    "国标 GB-50316 已更新",
	})
	require.NoError(t, err)

	converted, err := svc.Convert(ctx, 7, repo.rows[0].ID, "admin-1")
	require.NoError(t, err)
	assert.Equal(t, string(types.ChangeSignalConverted), converted.Status)
	assert.Equal(t, "cr-1", converted.ChangeRequestID)

	require.Len(t, crs.inputs, 1)
	assert.Equal(t, types.ChangeRiskHigh, crs.inputs[0].RiskLevel)
	assert.Equal(t, types.ChangeTypeContentRevision, crs.inputs[0].ChangeType)
	assert.Equal(t, types.ChangeReasonRegulation, crs.inputs[0].Reason)
}

// TestConvertRejectsHandledSignal keeps one signal from drafting twice.
func TestConvertRejectsHandledSignal(t *testing.T) {
	svc, repo, _ := newSignalService()
	ctx := context.Background()
	_, err := svc.Collect(ctx, &types.ChangeSignal{
		TenantID: 7, SignalType: string(types.ChangeSignalUserFeedback), SourceID: "msg-1",
	})
	require.NoError(t, err)
	_, err = svc.Convert(ctx, 7, repo.rows[0].ID, "admin-1")
	require.NoError(t, err)

	_, err = svc.Convert(ctx, 7, repo.rows[0].ID, "admin-1")
	require.ErrorIs(t, err, ErrChangeSignalHandled)
}

// TestDismissRemovesFromQueue covers false-positive triage.
func TestDismissRemovesFromQueue(t *testing.T) {
	svc, repo, _ := newSignalService()
	ctx := context.Background()
	_, err := svc.Collect(ctx, &types.ChangeSignal{
		TenantID: 7, SignalType: string(types.ChangeSignalConflict), SourceID: "doc-1",
	})
	require.NoError(t, err)

	dismissed, err := svc.Dismiss(ctx, 7, repo.rows[0].ID, "admin-1")
	require.NoError(t, err)
	assert.Equal(t, string(types.ChangeSignalDismissed), dismissed.Status)

	pending, err := svc.List(ctx, 7, types.ChangeSignalPending, "", 0)
	require.NoError(t, err)
	assert.Empty(t, pending)
}

// TestSignalReasonMapping covers the change-request reason each signal yields.
func TestSignalReasonMapping(t *testing.T) {
	assert.Equal(t, types.ChangeReasonRegulation,
		signalReasonFor(types.ChangeSignalExternalSubscription))
	assert.Equal(t, types.ChangeReasonCorrection,
		signalReasonFor(types.ChangeSignalUserFeedback))
	assert.Equal(t, types.ChangeReasonProcess,
		signalReasonFor(types.ChangeSignalSourceSystem))
}

// TestDedupeKeyIsStable covers the dedupe identity.
func TestDedupeKeyIsStable(t *testing.T) {
	first := types.ChangeSignalDedupeKey(7, "connector_health", "ds-1", "")
	assert.Equal(t, first, types.ChangeSignalDedupeKey(7, "connector_health", "ds-1", ""))
	assert.NotEqual(t, first, types.ChangeSignalDedupeKey(8, "connector_health", "ds-1", ""))
	assert.NotEqual(t, first, types.ChangeSignalDedupeKey(7, "conflict", "ds-1", ""))
}
