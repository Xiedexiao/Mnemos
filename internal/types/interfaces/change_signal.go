package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// ChangeSignalRepository persists the §7.3 signal catalogue.
type ChangeSignalRepository interface {
	// Create inserts one signal; reports whether it was new (duplicates within
	// the pending window are silently ignored).
	Create(ctx context.Context, signal *types.ChangeSignal) (bool, error)
	// List returns signals, optionally filtered by status and type.
	List(
		ctx context.Context, tenantID uint64, status types.ChangeSignalStatus,
		signalType types.ChangeSignalType, limit int,
	) ([]*types.ChangeSignal, error)
	// Get returns one signal.
	Get(ctx context.Context, tenantID uint64, signalID string) (*types.ChangeSignal, error)
	// MarkConverted records that a signal produced a change request.
	MarkConverted(
		ctx context.Context, tenantID uint64, signalID, changeRequestID, actorID string,
	) (*types.ChangeSignal, error)
	// Dismiss marks a signal as a false positive.
	Dismiss(ctx context.Context, tenantID uint64, signalID, actorID string) (*types.ChangeSignal, error)
	// CountPending returns the untriaged signal count.
	CountPending(ctx context.Context, tenantID uint64) (int64, error)
}

// ChangeSignalService is the §7.3 surface: collect a signal, list the queue,
// and turn a signal into a change-request draft.
type ChangeSignalService interface {
	// Collect records one signal. Collectors call this; it dedupes and fills the
	// derived fields (default action, dedupe key).
	Collect(ctx context.Context, signal *types.ChangeSignal) (*types.ChangeSignal, error)
	// List returns the triage queue.
	List(
		ctx context.Context, tenantID uint64, status types.ChangeSignalStatus,
		signalType types.ChangeSignalType, limit int,
	) ([]*types.ChangeSignal, error)
	// Convert turns a signal into a change-request draft and links them.
	Convert(
		ctx context.Context, tenantID uint64, signalID, actorID string,
	) (*types.ChangeSignal, error)
	// Dismiss marks a signal as a false positive.
	Dismiss(ctx context.Context, tenantID uint64, signalID, actorID string) (*types.ChangeSignal, error)
}
