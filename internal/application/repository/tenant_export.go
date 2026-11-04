package repository

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// tenantExportRepository implements interfaces.TenantExportRepository
// (KnowAxis V3.0 §16.3 租户级逻辑导出).
type tenantExportRepository struct {
	db *gorm.DB
}

// NewTenantExportRepository constructs the repository.
func NewTenantExportRepository(db *gorm.DB) interfaces.TenantExportRepository {
	return &tenantExportRepository{db: db}
}

// TableExists reports whether a table is present in this deployment.
//
// 白名单跨版本共享，而某个版本可能还没建某张表（迁移未跑或已废弃）。跳过
// 一张不存在的表要在清单里写明，而不是让整次导出失败。
func (r *tenantExportRepository) TableExists(
	ctx context.Context, table string,
) (bool, error) {
	return r.db.WithContext(ctx).Migrator().HasTable(table), nil
}

// ExportTable reads one table's rows for a tenant.
//
// 两条路径：
//   - 表自带 tenant_id：直接按它过滤；
//   - 关联表（tenant 不是列）：必须显式给出 join，否则**整表导出**——
//     那会跨租户泄露。白名单里 TenantColumn 为空的表因此必须能被 join 出来，
//     这里对无法安全限定的表直接拒绝，而不是退化成全表。
func (r *tenantExportRepository) ExportTable(
	ctx context.Context, tenantID uint64, table types.TenantExportTable, limit int,
) ([]map[string]any, error) {
	if limit <= 0 || limit > types.TenantExportMaxRowsPerTable {
		limit = types.TenantExportMaxRowsPerTable
	}
	query := r.db.WithContext(ctx).Table(table.Name)
	switch table.TenantColumn {
	case "tenant_id":
		query = query.Where("tenant_id = ?", tenantID)
	case "":
		// 关联表：通过父表限定。这是唯一安全的做法，白名单里每张
		// TenantColumn 为空的表都必须在下面有对应分支。
		switch table.Name {
		case "knowledge_tag_relations":
			query = query.Where(
				"knowledge_id IN (SELECT id FROM knowledges WHERE tenant_id = ?)", tenantID)
		case "change_items":
			query = query.Where(
				"cr_id IN (SELECT id FROM change_requests WHERE tenant_id = ?)", tenantID)
		default:
			return nil, fmt.Errorf(
				"table %q has no tenant filter; exporting it would cross workspaces", table.Name)
		}
	default:
		return nil, fmt.Errorf("unsupported tenant column %q on %q",
			table.TenantColumn, table.Name)
	}

	var rows []map[string]any
	if err := query.Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// CountTable counts a table's rows for a tenant, so the caller can tell
// truncation from completeness.
func (r *tenantExportRepository) CountTable(
	ctx context.Context, tenantID uint64, table types.TenantExportTable,
) (int64, error) {
	query := r.db.WithContext(ctx).Table(table.Name)
	switch table.TenantColumn {
	case "tenant_id":
		query = query.Where("tenant_id = ?", tenantID)
	case "":
		switch table.Name {
		case "knowledge_tag_relations":
			query = query.Where(
				"knowledge_id IN (SELECT id FROM knowledges WHERE tenant_id = ?)", tenantID)
		case "change_items":
			query = query.Where(
				"cr_id IN (SELECT id FROM change_requests WHERE tenant_id = ?)", tenantID)
		default:
			return 0, fmt.Errorf("table %q has no tenant filter", table.Name)
		}
	default:
		return 0, fmt.Errorf("unsupported tenant column %q", table.TenantColumn)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return 0, err
	}
	return total, nil
}

// DistinctClassifications returns the classifications present in one tenant's
// documents, so the export can be approved at the right ceiling.
func (r *tenantExportRepository) DistinctClassifications(
	ctx context.Context, tenantID uint64,
) ([]string, error) {
	var values []string
	err := r.db.WithContext(ctx).Table("knowledges").
		Where("tenant_id = ?", tenantID).
		Distinct("classification").
		Pluck("classification", &values).Error
	if err != nil {
		return nil, err
	}
	// 空分类按 internal 处理（与 ParseKnowledgeClassification 一致），
	// 这里把它显式化，避免调用方在比较等级时漏掉"未标注"的行。
	for index, value := range values {
		if strings.TrimSpace(value) == "" {
			values[index] = string(types.KnowledgeClassificationInternal)
		}
	}
	return values, nil
}
