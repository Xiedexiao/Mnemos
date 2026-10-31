package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// KnowledgeHealthRepository reads the health signals behind the knowledge
// health dashboard (KnowAxis V3.0 §14.6 / §16.1 v1) and persists its
// snapshots. The dashboard is read-only: snapshots are append-only rows.
type KnowledgeHealthRepository interface {
	// CreateSnapshot persists one snapshot row (ID assigned by the model hook).
	CreateSnapshot(ctx context.Context, snapshot *types.KnowledgeHealthSnapshot) error
	// LatestSnapshot returns the newest snapshot for (tenant, scope, scopeID);
	// returns gorm.ErrRecordNotFound when none exists yet (看板首次访问由
	// 上层现算).
	LatestSnapshot(
		ctx context.Context, tenantID uint64, scope types.KnowledgeHealthScope, scopeID string,
	) (*types.KnowledgeHealthSnapshot, error)
	// CollectKnowledgeMetrics aggregates the v1 health signals from the
	// knowledges table for one knowledge base (kbID != "") or the whole
	// tenant (kbID == ""). Soft-deleted rows are excluded from the live
	// counts; the soft-deleted stock is reported separately.
	CollectKnowledgeMetrics(ctx context.Context, tenantID uint64, kbID string) (*types.KnowledgeHealthRawStats, error)
	// KnowledgeBaseExists reports whether the KB belongs to the tenant
	// (soft-deleted KBs do not count).
	KnowledgeBaseExists(ctx context.Context, tenantID uint64, kbID string) (bool, error)
	// ListKnowledgeBaseIDs returns every live KB id of the tenant
	// (CronComputeAll's iteration input, stable order).
	ListKnowledgeBaseIDs(ctx context.Context, tenantID uint64) ([]string, error)
}

// KnowledgeHealthService computes and serves the knowledge health dashboard
// (只读看板：Compute 落快照供 Latest 与 GET 现算复用；审计不强制).
type KnowledgeHealthService interface {
	// Compute computes one snapshot: scopeID == "" → tenant scope, otherwise
	// the given KB (not-found error when the KB does not belong to the
	// tenant). The snapshot is persisted, then returned.
	Compute(ctx context.Context, tenantID uint64, scopeID string) (*types.KnowledgeHealthSnapshot, error)
	// Latest returns the newest persisted snapshot; passes through
	// gorm.ErrRecordNotFound when none exists so callers can fall back to
	// Compute (现算现返回).
	Latest(
		ctx context.Context, tenantID uint64, scope types.KnowledgeHealthScope, scopeID string,
	) (*types.KnowledgeHealthSnapshot, error)
	// CronComputeAll snapshots every live KB of the tenant plus the tenant
	// scope itself (供后续定时任务调用). Per-scope failures are logged and
	// skipped — one broken KB must not kill the batch; the returned list
	// holds the snapshots that succeeded.
	CronComputeAll(ctx context.Context, tenantID uint64) ([]*types.KnowledgeHealthSnapshot, error)
}
