package service

import (
	"context"
	"strings"
	"time"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// tenantExportService implements interfaces.TenantExportService
// (KnowAxis V3.0 §16.3 租户级逻辑导出).
//
// 与 ExportControl 的关系：ExportControl 管**审批**（谁申请、谁批、密级上限、
// 水印），这里管**产物**（导出哪些表、写成什么）。两者分开是因为审批有独立的
// 生命周期与审计要求，而导出格式会随 schema 演进。
type tenantExportService struct {
	repo interfaces.TenantExportRepository
	// exports looks up the approved ExportControl request so a high-classification
	// export cannot run on a bare promise.
	exports interfaces.ExportControlService
	now     func() time.Time
}

// NewTenantExportService constructs the service.
func NewTenantExportService(
	repo interfaces.TenantExportRepository,
	exports interfaces.ExportControlService,
) interfaces.TenantExportService {
	return &tenantExportService{repo: repo, exports: exports, now: time.Now}
}

// Plan reports what an export would contain and the approval ceiling.
func (s *tenantExportService) Plan(
	ctx context.Context, tenantID uint64,
) (*types.TenantExportManifest, error) {
	if tenantID == 0 {
		return nil, apperrors.NewUnauthorizedError("tenant context is required")
	}
	summaries, err := s.Preview(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	manifest := types.NewTenantExportManifest(tenantID, "", "", s.now())
	manifest.Tables = summaries

	classifications, err := s.repo.DistinctClassifications(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	ceiling := types.TenantExportClassificationCeiling(classifications)
	if types.TenantExportNeedsApproval(ceiling) {
		// 在 Note 里直接说明需要审批：调用方读到 Plan 就知道下一步要做什么，
		// 而不是发出请求后才被拒。
		manifest.Note = types.TenantExportNote +
			" 本次导出包含 " + string(ceiling) + " 级内容，需要审批。"
	}
	return &manifest, nil
}

// Preview counts what each whitelisted table would contribute.
func (s *tenantExportService) Preview(
	ctx context.Context, tenantID uint64,
) ([]types.TenantExportTableSummary, error) {
	tables := types.ExportableTables()
	summaries := make([]types.TenantExportTableSummary, 0, len(tables))
	for _, table := range tables {
		exists, err := s.repo.TableExists(ctx, table.Name)
		if err != nil {
			return nil, err
		}
		if !exists {
			// 白名单跨版本共享：这个部署还没建这张表。跳过并写明，
			// 不让整次导出失败，也不假装它贡献了 0 行。
			summaries = append(summaries, types.TenantExportTableSummary{
				Name: table.Name, Skipped: true,
				Reason: "表在本部署中不存在",
			})
			continue
		}
		count, err := s.repo.CountTable(ctx, tenantID, table)
		if err != nil {
			// 单表计数失败同样降级为"跳过"，并保留原因。
			summaries = append(summaries, types.TenantExportTableSummary{
				Name: table.Name, Skipped: true, Reason: err.Error(),
			})
			continue
		}
		if count > types.TenantExportMaxRowsPerTable {
			summaries = append(summaries, types.TenantExportTableSummary{
				Name: table.Name, Rows: types.TenantExportMaxRowsPerTable,
				Reason: "超过单表上限，导出已截断",
			})
			continue
		}
		summaries = append(summaries, types.TenantExportTableSummary{
			Name: table.Name, Rows: int(count),
		})
	}
	return summaries, nil
}

// Export builds the export document.
func (s *tenantExportService) Export(
	ctx context.Context, tenantID uint64, actorID, exportRequestID string,
) (*types.TenantExportDocument, error) {
	if tenantID == 0 {
		return nil, apperrors.NewUnauthorizedError("tenant context is required")
	}
	if strings.TrimSpace(actorID) == "" {
		return nil, apperrors.NewUnauthorizedError("caller identity is required")
	}

	classifications, err := s.repo.DistinctClassifications(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	ceiling := types.TenantExportClassificationCeiling(classifications)
	if types.TenantExportNeedsApproval(ceiling) {
		// 高密级导出必须带一个已批准的导出申请。没有凭据就拒绝，而不是
		// "先导出再补审批"——数据一旦流出无法召回。
		if strings.TrimSpace(exportRequestID) == "" {
			return nil, apperrors.NewForbiddenError(
				"this export contains " + string(ceiling) +
					"-classified content and needs an approved export request")
		}
		if err := s.assertExportApproved(ctx, tenantID, exportRequestID); err != nil {
			return nil, err
		}
	}

	document := &types.TenantExportDocument{
		Manifest: types.NewTenantExportManifest(tenantID, actorID, exportRequestID, s.now()),
		Data:     make(map[string][]map[string]any, len(types.ExportableTables())),
	}
	for _, table := range types.ExportableTables() {
		exists, err := s.repo.TableExists(ctx, table.Name)
		if err != nil {
			return nil, err
		}
		if !exists {
			document.Manifest.Tables = append(document.Manifest.Tables,
				types.TenantExportTableSummary{
					Name: table.Name, Skipped: true, Reason: "表在本部署中不存在",
				})
			continue
		}
		rows, err := s.repo.ExportTable(ctx, tenantID, table, types.TenantExportMaxRowsPerTable)
		if err != nil {
			// 单表失败不中断整次导出：写进清单说明它缺失，比给出一份
			// 看起来完整、实际少了一张表的产物要好。
			logger.Warnf(ctx, "[tenant-export] table %s failed: %v", table.Name, err)
			document.Manifest.Tables = append(document.Manifest.Tables,
				types.TenantExportTableSummary{
					Name: table.Name, Skipped: true, Reason: err.Error(),
				})
			continue
		}
		document.Data[table.Name] = rows
		document.Manifest.Tables = append(document.Manifest.Tables,
			types.TenantExportTableSummary{Name: table.Name, Rows: len(rows)})
	}
	return document, nil
}

// assertExportApproved verifies the linked ExportControl request.
//
// 只在服务可用时校验：导出审批服务未接通的部署（测试或精简版）按"调用方
// 已自证"处理，但日志留痕。生产部署里该服务总是存在。
func (s *tenantExportService) assertExportApproved(
	ctx context.Context, tenantID uint64, exportRequestID string,
) error {
	if s.exports == nil {
		logger.Warnf(ctx,
			"[tenant-export] export control is not wired; accepting request %s unverified",
			exportRequestID)
		return nil
	}
	// ExportControlService 只暴露 List（没有按 ID 取单条），因此这里按
	// 已批准状态列出租户的申请再匹配 ID。只查已批准的两条状态，避免把
	// 一长串待审/驳回的申请拉进内存。
	for _, status := range []string{
		string(types.ExportRequestStatusApproved),
		string(types.ExportRequestStatusFulfilled),
	} {
		requests, err := s.exports.List(ctx, tenantID, status)
		if err != nil {
			return apperrors.NewForbiddenError(
				"the referenced export request cannot be verified")
		}
		for _, request := range requests {
			if request != nil && request.ID == exportRequestID {
				return nil
			}
		}
	}
	return apperrors.NewForbiddenError(
		"no approved export request matches the given id")
}

// compile-time guard.
var _ interfaces.TenantExportService = (*tenantExportService)(nil)
