package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

// ErrShareLinkNotFound is returned when a token or (tenant, id) pair does
// not resolve to a share_links row. Services map it to an HTTP 404.
var ErrShareLinkNotFound = errors.New("share link not found")

// shareLinkRepository persists share links (KnowAxis V3.0 §2.5 v1).
// Rows are immutable except for two bounded writes: the revocation stamp
// and the visit bookkeeping — the latter must stay a single UPDATE per
// visit because public resolves are unauthenticated and hot links can be
// hit continuously.
type shareLinkRepository struct {
	db *gorm.DB
}

// NewShareLinkRepository constructs the production share link repository
// backed by the shared GORM connection.
func NewShareLinkRepository(db *gorm.DB) interfaces.ShareLinkRepository {
	return &shareLinkRepository{db: db}
}

// Create inserts one share link row. The UNIQUE(token) constraint is the
// last line of defence against a minting collision.
func (r *shareLinkRepository) Create(ctx context.Context, link *types.ShareLink) error {
	return r.db.WithContext(ctx).Create(link).Error
}

// ListByKnowledge returns every link issued for one knowledge entry in the
// tenant, newest first. Token is included: v1 has no "reveal once" store,
// so managers may re-copy the URL from the list.
func (r *shareLinkRepository) ListByKnowledge(
	ctx context.Context,
	tenantID uint64,
	knowledgeID string,
) ([]*types.ShareLink, error) {
	var links []*types.ShareLink
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_id = ?", tenantID, knowledgeID).
		Order("created_at DESC").
		Find(&links).Error; err != nil {
		return nil, err
	}
	return links, nil
}

// GetByToken resolves a visitor credential. Unknown tokens surface as
// ErrShareLinkNotFound so the service can answer the public endpoint with
// a uniform 404 for "unknown, revoked and expired" alike (no oracle).
func (r *shareLinkRepository) GetByToken(ctx context.Context, token string) (*types.ShareLink, error) {
	var link types.ShareLink
	if err := r.db.WithContext(ctx).
		Where("token = ?", token).
		First(&link).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrShareLinkNotFound
		}
		return nil, err
	}
	return &link, nil
}

// Revoke stamps revoked_at in a single conditional UPDATE. The
// "revoked_at IS NULL" predicate makes the operation idempotent-safe at
// the storage level: a second revoke matches zero rows.
func (r *shareLinkRepository) Revoke(
	ctx context.Context,
	tenantID uint64,
	shareLinkID string,
	revokedAt time.Time,
) (int64, error) {
	res := r.db.WithContext(ctx).
		Model(&types.ShareLink{}).
		Where("tenant_id = ? AND id = ? AND revoked_at IS NULL", tenantID, shareLinkID).
		Update("revoked_at", revokedAt)
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

// AppendVisit records one visit as a SINGLE UPDATE: visit_count is
// incremented and the new entry is prepended to visit_log, then the array
// is sliced back down to the most recent maxEntries entries — all inside
// one statement, so two concurrent visitors can never lose each other's
// entry or drift the count.
//
// The expression relies on PostgreSQL 12+ jsonb_path_query_array:
//
//	(?::jsonb || visit_log)   — prepend the new visit (jsonb || keeps the
//	                            left array's elements first),
//	'$[0 to N-1]'             — inclusive jsonpath slice keeping the first
//	                            (newest) N entries.
//
// maxEntries must be a compile-time-bounded small int (the caller passes
// types.ShareLinkVisitLogMaxEntries); it is formatted into the jsonpath
// literal because jsonpath strings cannot bind parameters.
func (r *shareLinkRepository) AppendVisit(
	ctx context.Context,
	shareLinkID string,
	visit types.ShareLinkVisit,
	maxEntries int,
) error {
	if maxEntries <= 0 {
		maxEntries = types.ShareLinkVisitLogMaxEntries
	}
	payload, err := json.Marshal(visit)
	if err != nil {
		// A visit entry that cannot be marshalled is a programming bug;
		// fall back to a timestamp-only entry so telemetry never blocks a
		// public resolve.
		payload, _ = json.Marshal(types.ShareLinkVisit{At: visit.At})
	}
	return r.db.WithContext(ctx).
		Model(&types.ShareLink{}).
		Where("id = ?", shareLinkID).
		Updates(map[string]interface{}{
			"visit_count": gorm.Expr("visit_count + 1"),
			"visit_log": gorm.Expr(
				fmt.Sprintf("jsonb_path_query_array(?::jsonb || visit_log, '$[0 to %d]')", maxEntries-1),
				string(payload),
			),
			"updated_at": time.Now(),
		}).Error
}
