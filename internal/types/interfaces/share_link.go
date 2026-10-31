package interfaces

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// ShareLinkRepository is the storage primitive for share_links
// (KnowAxis V3.0 §2.5 v1). Rows are never deleted: revocation flips
// revoked_at, and visit telemetry grows visit_count while visit_log is
// trimmed to the most recent entries on every append.
type ShareLinkRepository interface {
	// Create inserts one share link row. Token uniqueness is enforced by
	// the schema; a collision surfaces as a DB error (v1 retry budget is
	// the caller's — 43 random chars make collisions theoretical).
	Create(ctx context.Context, link *types.ShareLink) error
	// ListByKnowledge returns every link issued for one knowledge entry in
	// the tenant, newest first.
	ListByKnowledge(ctx context.Context, tenantID uint64, knowledgeID string) ([]*types.ShareLink, error)
	// GetByToken resolves a visitor credential to its link row. Returns
	// repository.ErrShareLinkNotFound when the token is unknown.
	GetByToken(ctx context.Context, token string) (*types.ShareLink, error)
	// Revoke stamps revoked_at on the (tenant, id) row when it is not
	// revoked yet, in a single UPDATE, and returns the affected row count.
	// 0 means "unknown id in this tenant" or "already revoked".
	Revoke(ctx context.Context, tenantID uint64, shareLinkID string, revokedAt time.Time) (int64, error)
	// AppendVisit records one visit atomically: visit_count + 1 and the new
	// entry prepended to visit_log, trimmed to the most recent maxEntries
	// in the SAME single UPDATE statement (PostgreSQL 12+ jsonpath slice).
	AppendVisit(ctx context.Context, shareLinkID string, visit types.ShareLinkVisit, maxEntries int) error
}

// ShareLinkService is the application-level share-link API (§2.5 v1).
// It owns the source-document classification ceiling, token minting,
// expiry/revocation checks, visit bookkeeping and the audit trail; the
// handler and router layers stay thin over it.
type ShareLinkService interface {
	// Create issues a new share link for one knowledge entry after the
	// classification ceiling check: confidential sources are rejected
	// outright, secret sources reject download permission. The token is
	// minted server-side (crypto/rand 32B → base64url).
	Create(
		ctx context.Context,
		tenantID uint64,
		knowledgeID string,
		createdBy string,
		linkType types.ShareLinkType,
		permission types.ShareLinkPermission,
		expiresAt *time.Time,
	) (*types.ShareLink, error)
	// Resolve turns a visitor token into the public read-only projection:
	// revoked/expired/unknown tokens are rejected; a valid token records
	// the visit (visit_count + trimmed visit_log + audit row) and returns
	// the knowledge view. The download URL is only minted when the link's
	// permission is download.
	Resolve(ctx context.Context, token string, visitor types.ShareLinkVisitorMeta) (*types.ShareLinkResolution, error)
	// Revoke flips revoked_at for the (tenant, id) link and writes the
	// share.revoked audit row. Revoking an unknown or already-revoked link
	// returns a not-found error.
	Revoke(ctx context.Context, tenantID uint64, shareLinkID, actorID string) error
	// ListByKnowledge returns the tenant's links for one knowledge entry.
	ListByKnowledge(ctx context.Context, tenantID uint64, knowledgeID string) ([]*types.ShareLink, error)
}
