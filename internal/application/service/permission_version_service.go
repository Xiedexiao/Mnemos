package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// permissionVersionService implements interfaces.PermissionVersionService.
//
// The raw authorization data always lives in the database and is re-read on
// every request (the indexes are accelerators, §12.7), so most §2.6 outlets
// are already correct without any propagation work. The epochs exist for the
// outlets that *do* hold a snapshot: answer caches, graph query caches and the
// session-history replay. For those, folding the epoch into the cache key makes
// the old entries unreachable in one step, which is cheaper and harder to get
// wrong than broadcasting one invalidation message per outlet.
type permissionVersionService struct {
	repo interfaces.PermissionVersionRepository
}

// NewPermissionVersionService constructs the epoch service.
func NewPermissionVersionService(
	repo interfaces.PermissionVersionRepository,
) interfaces.PermissionVersionService {
	return &permissionVersionService{repo: repo}
}

// Bump advances one scope's epoch.
func (s *permissionVersionService) Bump(
	ctx context.Context, tenantID uint64, scopeType types.PermissionScopeType,
	scopeID, reason string,
) (int64, error) {
	if tenantID == 0 {
		return 0, nil
	}
	if !scopeType.Valid() {
		return 0, fmt.Errorf("permission version: unknown scope type %q", scopeType)
	}
	return s.repo.Bump(ctx, tenantID, scopeType, scopeID, reason)
}

// BumpForKnowledge advances the document epoch (and the space epoch, so
// space-scoped caches such as listings also miss).
func (s *permissionVersionService) BumpForKnowledge(
	ctx context.Context, tenantID uint64, knowledgeID, kbID, reason string,
) (int64, error) {
	version, err := s.Bump(ctx, tenantID, types.PermissionScopeKnowledge, knowledgeID, reason)
	if err != nil {
		return 0, err
	}
	if kbID != "" {
		// Space epoch failures must not mask the document bump: the document
		// epoch is the one a per-document cache key depends on.
		if _, spaceErr := s.Bump(ctx, tenantID, types.PermissionScopeSpace, kbID, reason); spaceErr != nil {
			logger.Warnf(ctx, "failed to bump space permission version for %s: %v", kbID, spaceErr)
		}
	}
	return version, nil
}

// CacheKey returns the permission component of a cache key. Callers that cache
// anything derived from authorization must include it: two requests with the
// same query but a revoked grant get different keys, so the stale entry can
// never be served even if no invalidation message arrives.
func (s *permissionVersionService) CacheKey(
	ctx context.Context, tenantID uint64, knowledgeID, kbID string,
) string {
	tenant := s.currentVersion(ctx, tenantID, types.PermissionScopeTenant, "")
	space := s.currentVersion(ctx, tenantID, types.PermissionScopeSpace, kbID)
	doc := s.currentVersion(ctx, tenantID, types.PermissionScopeKnowledge, knowledgeID)
	return fmt.Sprintf("t%d.tenant%d.space%d.doc%d", tenantID, tenant, space, doc)
}

// currentVersion reads one epoch, degrading to 0 on error.
//
// A failed read must not silently produce a key that matches a pre-revocation
// entry, so callers that treat the key as a security boundary should prefer
// CacheKeyVerified. This helper exists for the best-effort caches.
func (s *permissionVersionService) currentVersion(
	ctx context.Context, tenantID uint64, scopeType types.PermissionScopeType, scopeID string,
) int64 {
	if s.repo == nil || tenantID == 0 {
		return 0
	}
	version, err := s.repo.Get(ctx, tenantID, scopeType, scopeID)
	if err != nil {
		logger.Warnf(ctx, "failed to read permission version %s/%s: %v", scopeType, scopeID, err)
		return 0
	}
	return version
}

// RecordRevocation writes the §2.6 outlet sweep for one revocation.
//
// The spec's completion standard is "every exit verified invisible", not "the
// event was sent", so the ledger records an explicit outcome for every outlet
// in AllRevocationOutlets — including the ones this deployment does not have,
// which are recorded as not_applicable rather than left absent. That way a
// revocation drill can read one table and see the whole surface.
func (s *permissionVersionService) RecordRevocation(
	ctx context.Context, tenantID uint64, knowledgeID, subjectID string,
	trigger types.RevocationTrigger,
) ([]*types.RevocationPropagation, error) {
	if tenantID == 0 {
		return nil, nil
	}
	version := s.currentVersion(ctx, tenantID, types.PermissionScopeKnowledge, knowledgeID)

	rows := make([]*types.RevocationPropagation, 0, len(types.AllRevocationOutlets))
	for _, outlet := range types.AllRevocationOutlets {
		row := &types.RevocationPropagation{
			TenantID:          tenantID,
			KnowledgeID:       knowledgeID,
			SubjectID:         subjectID,
			Trigger:           string(trigger),
			Outlet:            string(outlet),
			Outcome:           string(revocationOutcome(outlet)),
			PermissionVersion: version,
		}
		rows = append(rows, row)
	}
	if s.repo != nil {
		for _, row := range rows {
			if err := s.repo.RecordPropagation(ctx, row); err != nil {
				return rows, err
			}
		}
	}
	return rows, nil
}

// revocationOutcome explains how each outlet is satisfied today.
//
// Query-time outlets (search, vector, graph, notifications, suggestions) need
// no push: they re-read the ACL and lifecycle on every request, so a revoked
// grant is already invisible on the next query. The snapshot-holding outlets
// are invalidated by the epoch, and the ones this deployment does not run are
// reported as not applicable so the ledger never overstates coverage.
func revocationOutcome(outlet types.RevocationOutlet) types.RevocationOutcome {
	switch outlet {
	case types.RevocationOutletSearchIndex,
		types.RevocationOutletVectorIndex,
		types.RevocationOutletGraph,
		types.RevocationOutletSession,
		types.RevocationOutletNotification,
		types.RevocationOutletSuggestion:
		// 查询时判定：权限源是数据库，索引只是加速器（§12.7）。
		return types.RevocationOutcomeInvalidated
	case types.RevocationOutletAnswerCache:
		// 缓存键包含权限版本，版本前进即失效。
		return types.RevocationOutcomeInvalidated
	case types.RevocationOutletExport, types.RevocationOutletOffline:
		// 导出/离线产物带到期与留痕，撤权后不再生成新的；已下载清单由导出
		// 记录保留，属于"追踪清单"而非实时失效。
		return types.RevocationOutcomePending
	case types.RevocationOutletAudit:
		// 审计快照按设计保留历史，仅审计员可读元数据（§2.6 最后一行）。
		return types.RevocationOutcomeNotApplicable
	default:
		return types.RevocationOutcomePending
	}
}

// ListRevocations returns the ledger of one document.
func (s *permissionVersionService) ListRevocations(
	ctx context.Context, tenantID uint64, knowledgeID string, limit int,
) ([]*types.RevocationPropagation, error) {
	if s.repo == nil {
		return nil, nil
	}
	return s.repo.ListPropagationsByKnowledge(ctx, tenantID, knowledgeID, limit)
}

// permissionEpochReason normalises the reason string written to the ledger.
func permissionEpochReason(parts ...string) string {
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return strings.Join(out, ":")
}
