package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// KnowledgeCandidateRepository 候选卡片表（knowledge_candidates）的存储
// 原语。租户隔离：全部方法按 tenant_id 过滤。
type KnowledgeCandidateRepository interface {
	// Create 插入候选卡片（服务层已填好 ID/审计字段）。
	Create(ctx context.Context, candidate *types.KnowledgeCandidate) error
	// GetByID 按租户 + ID 取候选；不存在返回
	// repository.ErrKnowledgeCandidateNotFound。
	GetByID(ctx context.Context, tenantID uint64, id string) (*types.KnowledgeCandidate, error)
	// List 分页列出候选（created_at 倒序），返回总数。filter 的 Status/Level
	// 为空不过滤。
	List(ctx context.Context, tenantID uint64, filter types.KnowledgeCandidateListFilter) ([]*types.KnowledgeCandidate, int64, error)
	// Update 全量保存候选行（调用方加载后修改再回写）。
	Update(ctx context.Context, candidate *types.KnowledgeCandidate) error

	// FindExactTitle 返回同租户内标题精确相同的候选（可排除自身），供
	// v1 查重第一步使用。
	FindExactTitle(ctx context.Context, tenantID uint64, title string, excludeID string) ([]*types.KnowledgeCandidate, error)
	// CountSimilarTitles 统计同租户内标题包含给定关键词（title LIKE）的候选
	// 数量（排除自身与精确命中已在 FindExactTitle 覆盖的部分），供查重第二
	// 步的"相似标记"使用。
	CountSimilarTitles(ctx context.Context, tenantID uint64, titleFragment string, excludeID string) (int64, error)
}

// KnowledgeCandidateService 过程经验候选卡片服务（V3.0 §10 v1：候选卡片 +
// 审核发布闭环）。租户一律取自 ctx（types.TenantIDFromContext），缺失返回
// service.ErrCandidateTenantMissing。发布走 interfaces.KnowledgeRepository.
// CreateKnowledge 直接建 manual 知识行（候选卡片 → Markdown）。
type KnowledgeCandidateService interface {
	// CreateCandidate 创建候选卡片（手工/未来连接器共用）：校验来源/层级/
	// 置信白名单，做 v1 查重（同租户同标题精确 → duplicate；标题 LIKE 相似
	// → conflict），落卡并写审计 candidate.created。
	CreateCandidate(ctx context.Context, input types.KnowledgeCandidateCreateInput) (*types.KnowledgeCandidate, error)
	// ConfirmCandidate 确认推进：candidate→in_review 由提交人触发（本人或
	// Admin 可调）；in_review→confirmed 由审核人触发（v1 审核人 = Admin+，
	// L1→L2 晋级）。每次推进写审计 candidate.confirmed。
	ConfirmCandidate(ctx context.Context, candidateID, actorID string) (*types.KnowledgeCandidate, error)
	// PublishCandidate 发布（confirmed→published；§10.4 门禁：level=L3 需
	// Admin 且 confidence != AMBIGUOUS，否则 400）：创建 manual 正式知识
	// （title=候选标题、content=卡片字段拼 Markdown、lifecycle 默认 valid、
	// level 写 CustomMetadata），回填 knowledge_id 并写审计
	// candidate.published。kbID 可选：提供时校验存在并落入该 KB；为空生成
	// 未归档知识行（后续版本接归档 KB）。
	PublishCandidate(ctx context.Context, candidateID, publisherID, kbID string) (*types.KnowledgeCandidate, error)
	// RejectCandidate 审核驳回（in_review→rejected；终态），记录审核人与
	// 意见，写审计 candidate.rejected。
	RejectCandidate(ctx context.Context, candidateID, reviewerID, comment string) (*types.KnowledgeCandidate, error)
	// WithdrawCandidate 提交人撤回（candidate|in_review→withdrawn；终态，
	// 仅提交人本人可撤）。
	WithdrawCandidate(ctx context.Context, candidateID, actorID string) (*types.KnowledgeCandidate, error)
	// ListCandidates 分页列出候选（status/level 过滤）。
	ListCandidates(ctx context.Context, filter types.KnowledgeCandidateListFilter) ([]*types.KnowledgeCandidate, int64, error)
	// GetCandidate 按候选 ID 取单条（租户隔离）。
	GetCandidate(ctx context.Context, candidateID string) (*types.KnowledgeCandidate, error)
}
