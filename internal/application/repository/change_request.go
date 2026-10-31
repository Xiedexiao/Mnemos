package repository

import (
	"context"
	"errors"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

// 变更单三张表的仓储哨兵错误，service 层据此映射业务错误（404 等）。
var (
	ErrChangeRequestNotFound = errors.New("change request not found")
	ErrChangeItemNotFound    = errors.New("change item not found")
	// ErrChangeItemDuplicate (cr_id, knowledge_id) 唯一约束冲突（AddItem 并发窗口）。
	ErrChangeItemDuplicate = errors.New("change item already exists in this change request")
	ErrImpactTaskNotFound  = errors.New("impact task not found")
)

// changeRequestListDefaults 列表分页兜底：变更单是低频治理对象，一屏 50 条
// 足够，防止无界扫描。
const (
	changeRequestListDefaultLimit = 50
	changeRequestListMaxLimit     = 200
	// tagNeighborDefaultLimit 是单个条目可产出的"同 KB 近邻"任务上限，
	// 防止超大规模同标签簇把影响分析变成任务风暴。
	tagNeighborDefaultLimit = 20
)

// changeRequestRepository implements the change request repository interface.
type changeRequestRepository struct {
	db *gorm.DB
}

// NewChangeRequestRepository creates a new change request repository.
func NewChangeRequestRepository(db *gorm.DB) interfaces.ChangeRequestRepository {
	return &changeRequestRepository{db: db}
}

// Create inserts a change request row.
func (r *changeRequestRepository) Create(ctx context.Context, cr *types.ChangeRequest) error {
	return r.db.WithContext(ctx).Create(cr).Error
}

// GetByID gets a change request by tenant and id.
func (r *changeRequestRepository) GetByID(
	ctx context.Context, tenantID uint64, id string,
) (*types.ChangeRequest, error) {
	var cr types.ChangeRequest
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, id).
		First(&cr).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrChangeRequestNotFound
		}
		return nil, err
	}
	return &cr, nil
}

// List lists change requests newest-first with an optional status filter,
// returning the total count for pagination.
func (r *changeRequestRepository) List(
	ctx context.Context, tenantID uint64, filter types.ChangeRequestListFilter,
) ([]*types.ChangeRequest, int64, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = changeRequestListDefaultLimit
	}
	if limit > changeRequestListMaxLimit {
		limit = changeRequestListMaxLimit
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}

	query := r.db.WithContext(ctx).Model(&types.ChangeRequest{}).Where("tenant_id = ?", tenantID)
	if filter.Status != "" {
		query = query.Where("status = ?", filter.Status)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var crs []*types.ChangeRequest
	if err := query.
		Order("created_at DESC").
		Limit(limit).
		Offset(filter.Offset).
		Find(&crs).Error; err != nil {
		return nil, 0, err
	}
	return crs, total, nil
}

// Update saves the whole change request row (caller loads, mutates, saves back).
func (r *changeRequestRepository) Update(ctx context.Context, cr *types.ChangeRequest) error {
	return r.db.WithContext(ctx).Save(cr).Error
}

// CountByNumberPrefix counts how many change requests this tenant already has
// for the given number prefix ("CR-YYYYMMDD"), the base of the next sequence.
func (r *changeRequestRepository) CountByNumberPrefix(
	ctx context.Context, tenantID uint64, prefix string,
) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&types.ChangeRequest{}).
		Where("tenant_id = ? AND cr_number LIKE ?", tenantID, prefix+"-%").
		Count(&count).Error
	return count, err
}

// CreateItem appends one change item ((cr_id, knowledge_id) unique). A unique
// violation (concurrent AddItem on the same knowledge) is mapped to the
// ErrChangeItemDuplicate sentinel so the service can return a 409.
func (r *changeRequestRepository) CreateItem(ctx context.Context, item *types.ChangeItem) error {
	if err := r.db.WithContext(ctx).Create(item).Error; err != nil {
		if IsDuplicateKeyError(err) {
			return ErrChangeItemDuplicate
		}
		return err
	}
	return nil
}

// DeleteItem removes one change item.
func (r *changeRequestRepository) DeleteItem(ctx context.Context, crID string, knowledgeID string) error {
	res := r.db.WithContext(ctx).
		Where("cr_id = ? AND knowledge_id = ?", crID, knowledgeID).
		Delete(&types.ChangeItem{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrChangeItemNotFound
	}
	return nil
}

// GetItemByKnowledge fetches one item by (cr_id, knowledge_id).
func (r *changeRequestRepository) GetItemByKnowledge(
	ctx context.Context, crID string, knowledgeID string,
) (*types.ChangeItem, error) {
	var item types.ChangeItem
	if err := r.db.WithContext(ctx).
		Where("cr_id = ? AND knowledge_id = ?", crID, knowledgeID).
		First(&item).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrChangeItemNotFound
		}
		return nil, err
	}
	return &item, nil
}

// ListItemsByCR lists the items of the given change requests.
func (r *changeRequestRepository) ListItemsByCR(
	ctx context.Context, crIDs []string,
) ([]*types.ChangeItem, error) {
	if len(crIDs) == 0 {
		return nil, nil
	}
	var items []*types.ChangeItem
	if err := r.db.WithContext(ctx).
		Where("cr_id IN ?", crIDs).
		Order("created_at ASC").
		Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// UpdateItem writes back an item's application status.
func (r *changeRequestRepository) UpdateItem(ctx context.Context, item *types.ChangeItem) error {
	return r.db.WithContext(ctx).Save(item).Error
}

// CreateTask inserts one impact task.
func (r *changeRequestRepository) CreateTask(ctx context.Context, task *types.ImpactTask) error {
	return r.db.WithContext(ctx).Create(task).Error
}

// GetTask gets one impact task by tenant and id.
func (r *changeRequestRepository) GetTask(
	ctx context.Context, tenantID uint64, taskID string,
) (*types.ImpactTask, error) {
	var task types.ImpactTask
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, taskID).
		First(&task).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrImpactTaskNotFound
		}
		return nil, err
	}
	return &task, nil
}

// UpdateTask saves the whole impact task row.
func (r *changeRequestRepository) UpdateTask(ctx context.Context, task *types.ImpactTask) error {
	return r.db.WithContext(ctx).Save(task).Error
}

// ListTasksByCR lists the impact tasks of the given change requests.
func (r *changeRequestRepository) ListTasksByCR(
	ctx context.Context, crIDs []string,
) ([]*types.ImpactTask, error) {
	if len(crIDs) == 0 {
		return nil, nil
	}
	var tasks []*types.ImpactTask
	if err := r.db.WithContext(ctx).
		Where("cr_id IN ?", crIDs).
		Order("created_at ASC").
		Find(&tasks).Error; err != nil {
		return nil, err
	}
	return tasks, nil
}

// ListTagNeighborKnowledgeIDs returns knowledge ids inside the same KB that
// share at least one tag with the given knowledge (the v1 "谁引用了它"
// simplification for impact analysis).
//
// TODO(v3.1): replace with the real reference graph — chunk-level citations,
// wiki backlinks and notification-subscription edges (V3.0 §7.6). Tag
// co-occurrence is only a coarse stand-in: it over-approximates (siblings
// under a broad tag) and under-approximates (unrelated entries that cite the
// text). Results are deterministic (ordered by knowledge id) so re-analysis
// of an unchanged CR does not reshuffle its tasks.
func (r *changeRequestRepository) ListTagNeighborKnowledgeIDs(
	ctx context.Context, tenantID uint64, kbID string, knowledgeID string, limit int,
) ([]string, error) {
	if limit <= 0 || limit > tagNeighborDefaultLimit {
		limit = tagNeighborDefaultLimit
	}
	const query = `
SELECT DISTINCT r2.knowledge_id
FROM knowledge_tag_relations r1
JOIN knowledge_tag_relations r2
  ON r2.tag_id = r1.tag_id AND r2.knowledge_id <> r1.knowledge_id
JOIN knowledge_tags t ON t.id = r1.tag_id
JOIN knowledges k ON k.id = r2.knowledge_id AND k.deleted_at IS NULL
WHERE r1.knowledge_id = ?
  AND t.tenant_id = ? AND t.knowledge_base_id = ?
  AND k.tenant_id = ? AND k.knowledge_base_id = ?
ORDER BY r2.knowledge_id
LIMIT ?`
	ids := make([]string, 0, limit)
	if err := r.db.WithContext(ctx).
		Raw(query, knowledgeID, tenantID, kbID, tenantID, kbID, limit).
		Scan(&ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

// IsDuplicateKeyError reports whether err is a unique-constraint violation.
// gorm's postgres driver translates 23505 into ErrDuplicatedKey; other
// dialects (sqlite in tests) surface the constraint text, so fall back to a
// message check.
func IsDuplicateKeyError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint") ||
		strings.Contains(msg, "duplicate key") ||
		strings.Contains(msg, "Duplicate entry")
}
