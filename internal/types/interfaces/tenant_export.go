package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// TenantExportRepository reads one workspace's rows for a logical export.
//
// 每个方法都按租户限定；关联表通过父表 join 限定。仓库层拒绝无法安全限定的
// 表，而不是退化成全表导出——跨租户泄露的代价远高于"导出失败"。
type TenantExportRepository interface {
	// TableExists reports whether a table is present in this deployment.
	TableExists(ctx context.Context, table string) (bool, error)
	// ExportTable returns one table's rows for a tenant, capped at limit.
	ExportTable(
		ctx context.Context, tenantID uint64, table types.TenantExportTable, limit int,
	) ([]map[string]any, error)
	// CountTable counts the tenant's rows in one table.
	CountTable(ctx context.Context, tenantID uint64, table types.TenantExportTable) (int64, error)
	// DistinctClassifications returns the classifications present in the
	// tenant's documents, for approval-ceiling calculation.
	DistinctClassifications(ctx context.Context, tenantID uint64) ([]string, error)
}

// TenantExportService is the §16.3 logical export surface.
type TenantExportService interface {
	// Plan reports what an export would contain and which classification
	// ceiling it needs, without producing any data.
	Plan(ctx context.Context, tenantID uint64) (*types.TenantExportManifest, error)
	// Export builds the export document.
	//
	// exportRequestID links the artefact to the approved ExportControl request.
	// 调用方必须先取得审批：secret/confidential 内容的导出不接受"无凭据"的
	// 调用（Plan 返回的 ceiling 说明需不需要审批）。
	Export(
		ctx context.Context, tenantID uint64, actorID, exportRequestID string,
	) (*types.TenantExportDocument, error)
	// Preview returns the whitelist and what each table would contain.
	Preview(ctx context.Context, tenantID uint64) ([]types.TenantExportTableSummary, error)
}
