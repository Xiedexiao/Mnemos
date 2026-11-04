package service

import (
	"context"
	"time"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// readingService implements interfaces.ReadingService
// (KnowAxis V3.0 §15.2 阅读页目录与阅读进度).
type readingService struct {
	repo       interfaces.ReadingRepository
	knowledges interfaces.KnowledgeRepository
	acl        interfaces.KnowledgeACLRepository
	subjects   aclSubjectResolver
	now        func() time.Time
}

// NewReadingService constructs the service.
func NewReadingService(
	repo interfaces.ReadingRepository,
	knowledges interfaces.KnowledgeRepository,
	acl interfaces.KnowledgeACLRepository,
	subjectGroups interfaces.ACLSubjectGroupService,
) interfaces.ReadingService {
	return &readingService{
		repo: repo, knowledges: knowledges, acl: acl,
		subjects: newACLSubjectResolver(subjectGroups), now: time.Now,
	}
}

// TOC returns the document outline.
//
// 目录是"这篇文档存在哪些章节"的信息，因此与正文同级受权限约束：无权阅读
// 的人拿不到目录。ACL 查询失败按拒绝处理（fail-closed）。
func (s *readingService) TOC(
	ctx context.Context, tenantID uint64, knowledgeID string,
) (*types.ReadingTOC, error) {
	if knowledgeID == "" {
		return nil, apperrors.NewBadRequestError("knowledge_id is required")
	}
	knowledge, err := s.knowledges.GetKnowledgeByID(ctx, tenantID, knowledgeID)
	if err != nil || knowledge == nil {
		return nil, apperrors.NewNotFoundError("knowledge not found")
	}
	if !s.canView(ctx, tenantID, knowledge) {
		return nil, apperrors.NewForbiddenError("Permission denied to access this knowledge")
	}
	chunks, err := s.repo.ChunkContents(ctx, tenantID, knowledgeID)
	if err != nil {
		return nil, err
	}
	return types.BuildReadingTOC(knowledgeID, chunks), nil
}

// canView applies the document ACL gate.
func (s *readingService) canView(
	ctx context.Context, tenantID uint64, knowledge *types.Knowledge,
) bool {
	caller := types.CallerFromContext(ctx)
	if caller.UserID == "" {
		return false
	}
	// 密级门槛：secret/confidential 必须有显式 allow（ACL 查询会判定）。
	if s.acl == nil {
		return types.ParseKnowledgeClassification(knowledge.Classification).Rank() <
			types.KnowledgeClassificationSecret.Rank()
	}
	subjects, err := s.subjects.ForCaller(ctx, caller)
	if err != nil {
		logger.Warnf(ctx, "[reading] subject expansion failed: %v", err)
		return false
	}
	denied, err := s.acl.DeniedKnowledgeIDsForSubjects(
		ctx, []string{knowledge.KnowledgeBaseID}, subjects,
		types.PermissionActionView, s.now(),
	)
	if err != nil {
		logger.Warnf(ctx, "[reading] ACL check failed (fail-closed): %v", err)
		return false
	}
	for _, id := range denied {
		if id == knowledge.ID {
			return false
		}
	}
	return true
}

// SaveProgress records the caller's position.
func (s *readingService) SaveProgress(
	ctx context.Context, tenantID uint64, userID string, input types.ReadingProgressInput,
) (*types.ReadingProgress, error) {
	if userID == "" {
		return nil, apperrors.NewUnauthorizedError("caller identity is required")
	}
	normalized, err := types.NormalizeReadingProgress(input)
	if err != nil {
		return nil, apperrors.NewBadRequestError(err.Error())
	}
	now := s.now().UTC()
	progress := &types.ReadingProgress{
		TenantID:    tenantID,
		UserID:      userID,
		KnowledgeID: normalized.KnowledgeID,
		ChunkID:     normalized.ChunkID,
		Percent:     normalized.Percent,
		ChunkCount:  normalized.ChunkCount,
		// 读到 95% 以上就算读完：最后几屏往往是附录与修订记录，要求读到
		// 字面 100% 会让"已读"永远不成立。
		Completed: normalized.Percent >= 95,
		UpdatedAt: now,
		CreatedAt: now,
	}
	if err := s.repo.SaveProgress(ctx, progress); err != nil {
		return nil, err
	}
	return progress, nil
}

// GetProgress returns the caller's position in one document.
func (s *readingService) GetProgress(
	ctx context.Context, tenantID uint64, userID, knowledgeID string,
) (*types.ReadingProgress, error) {
	if userID == "" {
		return nil, apperrors.NewUnauthorizedError("caller identity is required")
	}
	return s.repo.GetProgress(ctx, tenantID, userID, knowledgeID)
}

// ContinueReading returns the caller's recent positions.
//
// 每条都过权限：撤权之后那条进度不能再出现在"继续阅读"里（标题会泄露存在
// 性），但也不能静默消失——返回占位并标 visible=false。
func (s *readingService) ContinueReading(
	ctx context.Context, tenantID uint64, userID string, limit int,
) (*types.ReadingProgressList, error) {
	if userID == "" {
		return nil, apperrors.NewUnauthorizedError("caller identity is required")
	}
	rows, err := s.repo.ListProgress(ctx, tenantID, userID, limit)
	if err != nil {
		return nil, err
	}
	items := types.BuildReadingProgressList(rows)
	if len(items) == 0 {
		return &types.ReadingProgressList{Items: items}, nil
	}
	// 一次性取标题与可见性，避免逐条查询。
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.KnowledgeID)
	}
	knowledgeRows, err := s.knowledges.GetKnowledgeBatch(ctx, tenantID, ids)
	if err != nil {
		logger.Warnf(ctx, "[reading] batch lookup failed: %v", err)
	}
	byID := make(map[string]*types.Knowledge, len(knowledgeRows))
	for _, knowledge := range knowledgeRows {
		if knowledge != nil {
			byID[knowledge.ID] = knowledge
		}
	}
	for index := range items {
		knowledge, ok := byID[items[index].KnowledgeID]
		if !ok {
			items[index].Visible = false
			continue
		}
		items[index].KnowledgeBaseID = knowledge.KnowledgeBaseID
		items[index].Title = knowledge.Title
		items[index].Visible = s.canView(ctx, tenantID, knowledge)
		if !items[index].Visible {
			// 无权时不带标题：列表本身不泄露存在性。
			items[index].Title = ""
		}
	}
	return &types.ReadingProgressList{Items: items}, nil
}
