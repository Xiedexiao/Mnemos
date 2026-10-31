package repository

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

// ============================================================================
// 导出管控 + DLP 命中的存储原语（KnowAxis V3.0 §2.2 / §13 v1）。两个仓储
// 都是无状态 CRUD：状态机校验在 service 层，这里只管按租户隔离读写。
// ============================================================================

// 导出申请列表是低频人读接口（Admin 审批队列），不设分页 v1；DLP findings
// 列表沿用证据包/审计列表的"默认 + 硬上限"口径，防止无界扫描。
const (
	// dlpFindingsDefaultLimit is applied when the caller passes limit <= 0.
	dlpFindingsDefaultLimit = 50
	// dlpFindingsMaxLimit is the hard cap per list call.
	dlpFindingsMaxLimit = 200
)

// exportRequestRepository implements interfaces.ExportRequestRepository.
type exportRequestRepository struct {
	db *gorm.DB
}

// NewExportRequestRepository constructs the export request repository.
func NewExportRequestRepository(db *gorm.DB) interfaces.ExportRequestRepository {
	return &exportRequestRepository{db: db}
}

// CreateRequest inserts one request (ID assigned by the model hook).
func (r *exportRequestRepository) CreateRequest(ctx context.Context, req *types.ExportRequest) error {
	return r.db.WithContext(ctx).Create(req).Error
}

// GetRequest resolves one request by ID within the tenant.
func (r *exportRequestRepository) GetRequest(
	ctx context.Context, tenantID uint64, requestID string,
) (*types.ExportRequest, error) {
	var req types.ExportRequest
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, requestID).
		First(&req).Error; err != nil {
		return nil, err
	}
	return &req, nil
}

// SaveRequest persists the full state of a loaded request (the service
// loads → mutates → saves, mirroring the glossary UpdateTerm posture).
func (r *exportRequestRepository) SaveRequest(ctx context.Context, req *types.ExportRequest) error {
	return r.db.WithContext(ctx).Save(req).Error
}

// ListRequests lists the tenant's requests, newest first; an empty status
// lists all. The service validates a non-empty status against the enum.
func (r *exportRequestRepository) ListRequests(
	ctx context.Context, tenantID uint64, status string,
) ([]*types.ExportRequest, error) {
	query := r.db.WithContext(ctx).Where("tenant_id = ?", tenantID)
	if status != "" {
		query = query.Where("status = ?", status)
	}
	var reqs []*types.ExportRequest
	err := query.Order("created_at DESC").Find(&reqs).Error
	return reqs, err
}

// dlpRepository implements interfaces.DLPRepository (findings event stream).
type dlpRepository struct {
	db *gorm.DB
}

// NewDLPRepository constructs the DLP findings repository.
func NewDLPRepository(db *gorm.DB) interfaces.DLPRepository {
	return &dlpRepository{db: db}
}

// CreateFinding inserts one finding row (best-effort callers never block
// their output path on this write; see DLPService.ScanText).
func (r *dlpRepository) CreateFinding(ctx context.Context, finding *types.DLPFinding) error {
	return r.db.WithContext(ctx).Create(finding).Error
}

// ListFindings returns the tenant's most recent findings, newest first.
// limit <= 0 uses the default; the hard cap mirrors the evidence list
// posture (unbounded scans stay off the table).
func (r *dlpRepository) ListFindings(
	ctx context.Context, tenantID uint64, limit int,
) ([]*types.DLPFinding, error) {
	if limit <= 0 {
		limit = dlpFindingsDefaultLimit
	}
	if limit > dlpFindingsMaxLimit {
		limit = dlpFindingsMaxLimit
	}
	var findings []*types.DLPFinding
	err := r.db.WithContext(ctx).
		Where("tenant_id = ?", tenantID).
		Order("created_at DESC").
		Limit(limit).
		Find(&findings).Error
	return findings, err
}
