package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// PermissionVersionRepository stores the authorization epochs used to
// invalidate permission-dependent caches, and the revocation propagation
// ledger (KnowAxis V3.0 §2.6).
type PermissionVersionRepository interface {
	// Bump advances the epoch of one scope and returns the new value.
	Bump(
		ctx context.Context, tenantID uint64, scopeType types.PermissionScopeType,
		scopeID, reason string,
	) (int64, error)
	// Get returns the current epoch, or 0 when the scope was never bumped.
	Get(
		ctx context.Context, tenantID uint64, scopeType types.PermissionScopeType, scopeID string,
	) (int64, error)
	// RecordPropagation appends one outlet outcome row.
	RecordPropagation(ctx context.Context, row *types.RevocationPropagation) error
	// ListPropagationsByKnowledge returns the propagation ledger of one document.
	ListPropagationsByKnowledge(
		ctx context.Context, tenantID uint64, knowledgeID string, limit int,
	) ([]*types.RevocationPropagation, error)
}

// PermissionVersionService is the business face of the epoch ledger: it bumps
// epochs on authorization changes and answers the composite cache identity.
type PermissionVersionService interface {
	// Bump advances the epoch of one scope.
	Bump(
		ctx context.Context, tenantID uint64, scopeType types.PermissionScopeType,
		scopeID, reason string,
	) (int64, error)
	// BumpForKnowledge advances the document epoch and marks the enclosing
	// space epoch dirty, so space-scoped caches also miss.
	BumpForKnowledge(ctx context.Context, tenantID uint64, knowledgeID, kbID, reason string) (int64, error)
	// CacheKey returns the permission component of a cache key: the document
	// epoch, the space epoch and the tenant epoch folded into one string.
	CacheKey(ctx context.Context, tenantID uint64, knowledgeID, kbID string) string
	// RecordRevocation writes the full §2.6 outlet sweep for one revocation and
	// returns the rows it persisted. Outlets the deployment does not have are
	// recorded as not_applicable rather than omitted, so the ledger always
	// answers "which exits were considered".
	RecordRevocation(
		ctx context.Context, tenantID uint64, knowledgeID, subjectID string,
		trigger types.RevocationTrigger,
	) ([]*types.RevocationPropagation, error)
	// ListRevocations returns the ledger of one document.
	ListRevocations(
		ctx context.Context, tenantID uint64, knowledgeID string, limit int,
	) ([]*types.RevocationPropagation, error)
}
