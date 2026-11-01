package repository

import (
	"context"
	"errors"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// parseQualityRepository implements interfaces.ParseQualityRepository on the
// parse_quality_scores table created by migration 000156.
type parseQualityRepository struct {
	db *gorm.DB
}

// NewParseQualityRepository constructs the parse quality repository.
func NewParseQualityRepository(db *gorm.DB) interfaces.ParseQualityRepository {
	return &parseQualityRepository{db: db}
}

const (
	// parseQualityQueueDefault / parseQualityQueueHardLimit 是解析异常队列的
	// 默认与硬上限（队列是治理待办，不是可翻页的全量列表）。
	parseQualityQueueDefault   = 50
	parseQualityQueueHardLimit = 200
)

// UpsertScore 写入或覆盖一条评分。冲突目标 = UNIQUE(knowledge_id)：重算
// 覆盖六维度、总分、档位、below_threshold、details 与 computed_at，行 id
// 与首次插入保持一致（评分是"当前快照"，不保留历史）。
//
// grade / below_threshold 在落库前由 ApplyGrade 从总分派生一次，保证三列
// 不会出现自相矛盾的组合（即使调用方忘了填）。
func (r *parseQualityRepository) UpsertScore(
	ctx context.Context, score *types.ParseQualityScore,
) error {
	if score == nil {
		return errors.New("parse quality: nil score row")
	}
	score.ApplyGrade()
	if score.ID == "" {
		score.ID = uuid.NewString()
	}
	if score.ComputedAt.IsZero() {
		score.ComputedAt = time.Now().UTC()
	}
	if len(score.Details) == 0 {
		score.Details = types.JSON("{}")
	}
	return r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "knowledge_id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"tenant_id",
				"text_completeness",
				"structure_completeness",
				"table_usability",
				"ocr_quality",
				"metadata_completeness",
				"citable",
				"total_score",
				"grade",
				"below_threshold",
				"details",
				"computed_at",
			}),
		}).
		Create(score).Error
}

// GetByKnowledge 按租户 + 知识 ID 取评分行；不存在返回 gorm.ErrRecordNotFound
// （调用方据此区分"未评分"与"读取失败"）。
func (r *parseQualityRepository) GetByKnowledge(
	ctx context.Context, tenantID uint64, knowledgeID string,
) (*types.ParseQualityScore, error) {
	var score types.ParseQualityScore
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_id = ?", tenantID, knowledgeID).
		First(&score).Error; err != nil {
		return nil, err
	}
	return &score, nil
}

// ListBelowThreshold 返回本租户解析异常的行（total_score 升序，最差的在前；
// 同分按 computed_at 倒序——刚被重算出来的同一档问题先看到）。
func (r *parseQualityRepository) ListBelowThreshold(
	ctx context.Context, tenantID uint64, limit int,
) ([]*types.ParseQualityScore, error) {
	var scores []*types.ParseQualityScore
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND below_threshold = ?", tenantID, true).
		Order("total_score ASC, computed_at DESC").
		Limit(clampParseQualityQueueLimit(limit)).
		Find(&scores).Error; err != nil {
		return nil, err
	}
	return scores, nil
}

// ListQueueItemsBelowThreshold 是队列的展示形态：先取 below_threshold 的
// 分数行，再用一次批量查询补齐知识元信息（title/file_name/file_type/
// parse_status/knowledge_base_id/error_message）。软删知识不返回；缺失的
// 知识行（不该出现，防御性处理）保留分数行但元信息为空。
//
// 两次查询而非 join：分数行很少，批量 IN 的代价远低于 join + 去重，也让
// gorm 的软删作用域（Model(&types.Knowledge{})）直接生效。
func (r *parseQualityRepository) ListQueueItemsBelowThreshold(
	ctx context.Context, tenantID uint64, limit int,
) ([]*types.ParseQualityQueueItem, error) {
	scores, err := r.ListBelowThreshold(ctx, tenantID, limit)
	if err != nil {
		return nil, err
	}
	if len(scores) == 0 {
		return nil, nil
	}

	knowledgeIDs := make([]string, 0, len(scores))
	for _, score := range scores {
		knowledgeIDs = append(knowledgeIDs, score.KnowledgeID)
	}

	// 只取展示所需的列；软删由 Knowledge 的 gorm.DeletedAt 作用域自动过滤。
	var rows []*types.Knowledge
	if err := r.db.WithContext(ctx).
		Model(&types.Knowledge{}).
		Select("id, title, file_name, file_type, parse_status, knowledge_base_id, error_message").
		Where("tenant_id = ? AND id IN ?", tenantID, knowledgeIDs).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	byID := make(map[string]*types.Knowledge, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}

	items := make([]*types.ParseQualityQueueItem, 0, len(scores))
	for _, score := range scores {
		knowledge := byID[score.KnowledgeID]
		if knowledge == nil {
			continue // 知识已软删：不进治理队列
		}
		items = append(items, &types.ParseQualityQueueItem{
			ParseQualityScore: score,
			Title:             knowledge.Title,
			FileName:          knowledge.FileName,
			FileType:          knowledge.FileType,
			ParseStatus:       knowledge.ParseStatus,
			KnowledgeBaseID:   knowledge.KnowledgeBaseID,
			ErrorMessage:      knowledge.ErrorMessage,
		})
	}
	return items, nil
}

// clampParseQualityQueueLimit clamps a caller-supplied limit into
// (0, parseQualityQueueHardLimit]; ≤0 means the default.
func clampParseQualityQueueLimit(limit int) int {
	if limit <= 0 {
		return parseQualityQueueDefault
	}
	if limit > parseQualityQueueHardLimit {
		return parseQualityQueueHardLimit
	}
	return limit
}
