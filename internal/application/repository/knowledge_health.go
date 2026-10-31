package repository

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

// knowledgeHealthRepository implements interfaces.KnowledgeHealthRepository.
type knowledgeHealthRepository struct {
	db *gorm.DB
}

// NewKnowledgeHealthRepository constructs the knowledge health repository.
func NewKnowledgeHealthRepository(db *gorm.DB) interfaces.KnowledgeHealthRepository {
	return &knowledgeHealthRepository{db: db}
}

// lifecycleCountRow is one GROUP BY lifecycle_status result row.
type lifecycleCountRow struct {
	LifecycleStatus string
	Count           int64
}

// healthSignalRow carries the single-pass CASE WHEN aggregates shared by the
// completeness and governance dimensions (one roundtrip instead of four).
type healthSignalRow struct {
	WithCustomMetadata int64
	WithDescription    int64
	NoReviewCycle      int64
	BeyondInternal     int64
}

// duplicateHashRow carries the duplicate-file_hash rollup over the per-(KB,
// hash) groups.
type duplicateHashRow struct {
	DuplicateRows   int64
	DuplicateGroups int64
}

// CreateSnapshot persists one snapshot row.
func (r *knowledgeHealthRepository) CreateSnapshot(ctx context.Context, snapshot *types.KnowledgeHealthSnapshot) error {
	return r.db.WithContext(ctx).Create(snapshot).Error
}

// LatestSnapshot returns the newest snapshot for (tenant, scope, scopeID);
// gorm.ErrRecordNotFound when none exists. The migration's
// (tenant_id, scope, computed_at DESC) index serves the ordering.
func (r *knowledgeHealthRepository) LatestSnapshot(
	ctx context.Context, tenantID uint64, scope types.KnowledgeHealthScope, scopeID string,
) (*types.KnowledgeHealthSnapshot, error) {
	var snapshot types.KnowledgeHealthSnapshot
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND scope = ? AND scope_id = ?", tenantID, string(scope), scopeID).
		Order("computed_at DESC").
		First(&snapshot).Error; err != nil {
		return nil, err
	}
	return &snapshot, nil
}

// scopeLive narrows a knowledges query to the tenant (and optionally one KB).
// The queries run on Model(&types.Knowledge{}) so gorm's soft-delete scope
// applies automatically — implementations must not add a redundant
// deleted_at IS NULL here (对照 KnowledgeBaseRepository.CountByVectorStoreID
// 的约定).
func (r *knowledgeHealthRepository) scopeLive(db *gorm.DB, tenantID uint64, kbID string) *gorm.DB {
	q := db.Where("tenant_id = ?", tenantID)
	if kbID != "" {
		q = q.Where("knowledge_base_id = ?", kbID)
	}
	return q
}

// CollectKnowledgeMetrics aggregates the v1 health signals from the
// knowledges table for one KB (kbID != "") or the whole tenant (kbID == "").
//
// 重复判定是"同 KB 内"：租户口径下按 (knowledge_base_id, file_hash) 分组，
// 不会把两个库各自的同名上传算成重复；KB 口径下分组键退化为 file_hash。
// custom_metadata 非空 = 非 NULL 且不是 '{}'/[]/null（JSONB 归一后无空白，
// SQLite 侧列同为紧凑 JSON 文本，两侧共用同一判定）。
func (r *knowledgeHealthRepository) CollectKnowledgeMetrics(
	ctx context.Context, tenantID uint64, kbID string,
) (*types.KnowledgeHealthRawStats, error) {
	stats := &types.KnowledgeHealthRawStats{LifecycleCounts: map[string]int64{}}

	// 新鲜度：按 lifecycle_status 分组计数，Total = 各组之和。
	var lifecycleRows []lifecycleCountRow
	if err := r.scopeLive(r.db.WithContext(ctx).Model(&types.Knowledge{}), tenantID, kbID).
		Select("lifecycle_status, COUNT(*) AS count").
		Group("lifecycle_status").
		Scan(&lifecycleRows).Error; err != nil {
		return nil, err
	}
	for _, row := range lifecycleRows {
		stats.LifecycleCounts[row.LifecycleStatus] = row.Count
		stats.Total += row.Count
	}

	// 补全 + 治理：一趟 CASE WHEN 聚合。
	var signals healthSignalRow
	if err := r.scopeLive(r.db.WithContext(ctx).Model(&types.Knowledge{}), tenantID, kbID).
		Select(
			"COALESCE(SUM(CASE WHEN COALESCE(CAST(custom_metadata AS TEXT), '') NOT IN ('', '{}', '[]', 'null') " +
				"THEN 1 ELSE 0 END), 0) AS with_custom_metadata, " +
				"COALESCE(SUM(CASE WHEN COALESCE(TRIM(description), '') <> '' THEN 1 ELSE 0 END), 0) AS with_description, " +
				"COALESCE(SUM(CASE WHEN review_cycle_days = 0 THEN 1 ELSE 0 END), 0) AS no_review_cycle, " +
				"COALESCE(SUM(CASE WHEN classification NOT IN ('internal', '') THEN 1 ELSE 0 END), 0) AS beyond_internal",
		).
		Scan(&signals).Error; err != nil {
		return nil, err
	}
	stats.WithCustomMetadata = signals.WithCustomMetadata
	stats.WithDescription = signals.WithDescription
	stats.NoReviewCycle = signals.NoReviewCycle
	stats.BeyondInternal = signals.BeyondInternal

	// 结构：同 KB 重复 file_hash。子查询按 (kb, hash) 分组取 >1 的组，外层
	// 汇总重复条数与组数。
	dupGroups := r.scopeLive(r.db.WithContext(ctx).Model(&types.Knowledge{}), tenantID, kbID).
		Select("knowledge_base_id, file_hash, COUNT(*) AS cnt").
		Where("file_hash <> ''").
		Group("knowledge_base_id, file_hash").
		Having("COUNT(*) > 1")
	var duplicates duplicateHashRow
	if err := r.db.WithContext(ctx).
		Table("(?) AS d", dupGroups).
		Select("COALESCE(SUM(cnt), 0) AS duplicate_rows, COUNT(*) AS duplicate_groups").
		Scan(&duplicates).Error; err != nil {
		return nil, err
	}
	stats.DuplicateFileCount = duplicates.DuplicateRows
	stats.DuplicateGroupCount = duplicates.DuplicateGroups

	// 软删存量：Unscoped 绕过软删作用域后显式数 deleted_at 非空的行。
	softDeleted := r.db.WithContext(ctx).Unscoped().Model(&types.Knowledge{}).
		Where("tenant_id = ? AND deleted_at IS NOT NULL", tenantID)
	if kbID != "" {
		softDeleted = softDeleted.Where("knowledge_base_id = ?", kbID)
	}
	if err := softDeleted.Count(&stats.SoftDeleted).Error; err != nil {
		return nil, err
	}
	return stats, nil
}

// KnowledgeBaseExists reports whether the KB belongs to the tenant. The
// gorm.DeletedAt scope on KnowledgeBase excludes soft-deleted rows
// automatically.
func (r *knowledgeHealthRepository) KnowledgeBaseExists(ctx context.Context, tenantID uint64, kbID string) (bool, error) {
	var count int64
	if err := r.db.WithContext(ctx).
		Model(&types.KnowledgeBase{}).
		Where("tenant_id = ? AND id = ?", tenantID, kbID).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// ListKnowledgeBaseIDs returns every live KB id of the tenant in stable
// creation order (CronComputeAll's iteration input).
func (r *knowledgeHealthRepository) ListKnowledgeBaseIDs(ctx context.Context, tenantID uint64) ([]string, error) {
	var ids []string
	if err := r.db.WithContext(ctx).
		Model(&types.KnowledgeBase{}).
		Where("tenant_id = ?", tenantID).
		Order("created_at ASC").
		Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}
