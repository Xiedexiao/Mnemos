package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// ParseQualityRepository 是解析质量评分（KnowAxis V3.0 §3.5 / §9.4）的存储
// 原语，对应迁移 000156 的 parse_quality_scores 表：每个 knowledge 一行当前
// 快照（UNIQUE(knowledge_id)），重算即覆盖。
//
// 打分的输入信号（knowledges 与 chunks 行）不在这里的职责面：服务层持有
// KnowledgeRepository / ChunkRepository 读原件，本接口只搬分数行。
type ParseQualityRepository interface {
	// UpsertScore 写入或覆盖一条评分（冲突目标 = UNIQUE(knowledge_id)：
	// 重算覆盖六维度、total_score、grade、below_threshold、details 与
	// computed_at，行 id 沿用首次插入的 ID）。实现须在写库前调用
	// ParseQualityScore.ApplyGrade 以保 grade/below_threshold 与总分一致。
	UpsertScore(ctx context.Context, score *types.ParseQualityScore) error
	// GetByKnowledge 按租户 + 知识 ID 取评分行；不存在返回
	// gorm.ErrRecordNotFound（handler 据此回 404，调用方可回退"未评分"）。
	GetByKnowledge(ctx context.Context, tenantID uint64, knowledgeID string) (*types.ParseQualityScore, error)
	// ListBelowThreshold 返回本租户 below_threshold = TRUE 的评分行（total_score
	// 升序，最差的在前），limit ≤ 0 时由实现给默认值并钳制上限。
	ListBelowThreshold(ctx context.Context, tenantID uint64, limit int) ([]*types.ParseQualityScore, error)
	// ListQueueItemsBelowThreshold 是队列的展示形态：分数行 + 知识元信息
	//（title / file_name / file_type / parse_status / knowledge_base_id /
	// error_message）。软删的知识不返回。limit 语义同上。
	ListQueueItemsBelowThreshold(ctx context.Context, tenantID uint64, limit int) ([]*types.ParseQualityQueueItem, error)
}

// ParseQualityService 是解析质量评分的业务入口（KnowAxis V3.0 §3.5）：
// 打分（读知识 + 其 chunks 六维度加权）、落库、审计、以及低质量文档的
// 解析异常队列（§3.5：低质量文档可搜但不可作高置信来源）。
type ParseQualityService interface {
	// ComputeForKnowledge 计算并落库一条知识的解析质量分。
	//
	// 输入：knowledges 行 + 该知识的全部 chunks（text / table_summary /
	// table_column / image_ocr 等）。知识不属于该租户时报 not found。
	// 落库口径：六维度各 0-100，total_score 按 §3.5 权重表加权，
	// grade = ParseQualityGrade(total)，below_threshold = total < 60。
	// 每次成功落库记一条 parse_quality.computed 审计（actorID 为空 = 系统
	// 触发，不阻塞主流程）。
	ComputeForKnowledge(ctx context.Context, tenantID uint64, knowledgeID, actorID string) (*types.ParseQualityScore, error)

	// Get 返回已落库的评分；不存在报 service.ErrParseQualityNotFound
	//（handler 映射 404，前端据此提示"尚未评分"）。如果希望读时现算，
	// 调用方在 NotFound 后可改调 ComputeForKnowledge。
	Get(ctx context.Context, tenantID uint64, knowledgeID string) (*types.ParseQualityScore, error)

	// ListBelowThreshold 返回解析异常队列（§3.5）：below_threshold 的文档
	// 按 total_score 升序（最差在前），limit 由服务层钳制。行内附带知识
	// 元信息，供治理端直接定位问题文档。
	ListBelowThreshold(ctx context.Context, tenantID uint64, limit int) ([]*types.ParseQualityQueueItem, error)
}
