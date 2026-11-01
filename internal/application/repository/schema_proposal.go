package repository

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

// ErrSchemaProposalNotFound 提案不存在（或不在本租户）。
var ErrSchemaProposalNotFound = errors.New("schema proposal not found")

const (
	// schemaProposalListDefaultLimit 是提案列表的默认条数。
	schemaProposalListDefaultLimit = 50
	// schemaProposalListMaxLimit 是提案列表的硬上限：提案是低频人读队列，
	// 200 条足够一屏，防止无界扫描。
	schemaProposalListMaxLimit = 200
)

// schemaProposalRepository implements interfaces.SchemaProposalRepository on
// the schema_proposals table created by migration 000155.
type schemaProposalRepository struct {
	db *gorm.DB
}

// NewSchemaProposalRepository constructs the schema proposal repository.
func NewSchemaProposalRepository(db *gorm.DB) interfaces.SchemaProposalRepository {
	return &schemaProposalRepository{db: db}
}

// Create inserts one proposal row. The status/name indexes live in the
// migration; UNIQUE constraints are intentionally absent (the same name may be
// proposed more than once — a rejected proposal can be re-proposed).
func (r *schemaProposalRepository) Create(ctx context.Context, proposal *types.SchemaProposal) error {
	return r.db.WithContext(ctx).Create(proposal).Error
}

// GetByID returns one tenant-scoped proposal.
func (r *schemaProposalRepository) GetByID(
	ctx context.Context, tenantID uint64, proposalID string,
) (*types.SchemaProposal, error) {
	var proposal types.SchemaProposal
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, proposalID).
		First(&proposal).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrSchemaProposalNotFound
		}
		return nil, err
	}
	return &proposal, nil
}

// List returns the tenant's proposals newest first plus the total count.
func (r *schemaProposalRepository) List(
	ctx context.Context, tenantID uint64, filter types.SchemaProposalListFilter,
) ([]*types.SchemaProposal, int64, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = schemaProposalListDefaultLimit
	}
	if limit > schemaProposalListMaxLimit {
		limit = schemaProposalListMaxLimit
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}

	query := r.db.WithContext(ctx).
		Model(&types.SchemaProposal{}).
		Where("tenant_id = ?", tenantID)
	if status := strings.TrimSpace(filter.Status); status != "" {
		query = query.Where("status = ?", status)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	items := make([]*types.SchemaProposal, 0, limit)
	if err := query.
		Order("created_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// Update persists the governance fields of one proposal. The payload columns
// (name/domain/proposal/source_knowledge_ids/created_by/created_at) are
// deliberately excluded: a proposal's content is immutable evidence of what was
// reviewed.
func (r *schemaProposalRepository) Update(ctx context.Context, proposal *types.SchemaProposal) error {
	proposal.UpdatedAt = time.Now()
	return r.db.WithContext(ctx).
		Model(proposal).
		Select("status", "reviewed_by", "review_comment", "applied_schema_id",
			"compatibility_report", "updated_at").
		Updates(proposal).Error
}

// UpdateCompatibilityReport writes only the compatibility columns, so a check
// running concurrently with an approval cannot clobber the decision fields.
func (r *schemaProposalRepository) UpdateCompatibilityReport(
	ctx context.Context, proposal *types.SchemaProposal,
) error {
	proposal.UpdatedAt = time.Now()
	return r.db.WithContext(ctx).
		Model(proposal).
		Select("compatibility_report", "updated_at").
		Updates(proposal).Error
}
