package service

import (
	"context"
	"errors"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// 知识关联的业务哨兵错误。
var (
	// ErrRelationSelfLink 不能把文档关联到自己。
	ErrRelationSelfLink = errors.New("knowledge relation: a document cannot be linked to itself")
	// ErrRelationInvalidType 关系类型不在白名单内。
	ErrRelationInvalidType = errors.New("knowledge relation: unknown relation type")
	// ErrRelationCrossTenant 两端文档不属于同一租户。
	ErrRelationCrossTenant = errors.New("knowledge relation: both documents must belong to the same tenant")
)

// knowledgeRelationService implements interfaces.KnowledgeRelationService
// (KnowAxis V3.0 §4.5).
type knowledgeRelationService struct {
	repo       interfaces.KnowledgeRelationRepository
	knowledges interfaces.KnowledgeRepository
}

// NewKnowledgeRelationService constructs the relation service.
func NewKnowledgeRelationService(
	repo interfaces.KnowledgeRelationRepository,
	knowledges interfaces.KnowledgeRepository,
) interfaces.KnowledgeRelationService {
	return &knowledgeRelationService{repo: repo, knowledges: knowledges}
}

// Link creates a typed edge and its reverse mirror.
//
// 两端都必须存在且同租户：跨租户关联会把一篇文档的标题泄露给另一租户的
// 读者（§2.10 存在性不泄露），所以这里是硬拒绝而不是静默跳过。
func (s *knowledgeRelationService) Link(
	ctx context.Context, tenantID uint64, fromID, toID string,
	relationType types.KnowledgeRelationType, note, actorID string,
	origin types.KnowledgeRelationOrigin,
) (*types.KnowledgeRelation, error) {
	if tenantID == 0 {
		return nil, apperrors.NewUnauthorizedError("tenant context is required")
	}
	if fromID == "" || toID == "" {
		return nil, apperrors.NewBadRequestError("from_knowledge_id and to_knowledge_id are required")
	}
	if fromID == toID {
		return nil, apperrors.NewBadRequestError("a document cannot be linked to itself")
	}
	if relationType == "" {
		relationType = types.RelationRelated
	}
	if !relationType.Valid() {
		return nil, apperrors.NewBadRequestError("unknown relation type")
	}
	if origin == "" {
		origin = types.RelationOriginManual
	}
	if !origin.Valid() {
		return nil, apperrors.NewBadRequestError("unknown relation origin")
	}
	for _, id := range []string{fromID, toID} {
		knowledge, err := s.knowledges.GetKnowledgeByID(ctx, tenantID, id)
		if err != nil || knowledge == nil || knowledge.TenantID != tenantID {
			return nil, apperrors.NewNotFoundError("knowledge not found: " + id)
		}
	}

	pairKey := types.KnowledgeRelationPairKey(fromID, toID, string(relationType))
	forward := &types.KnowledgeRelation{
		TenantID:        tenantID,
		FromKnowledgeID: fromID,
		ToKnowledgeID:   toID,
		RelationType:    string(relationType),
		PairKey:         pairKey,
		Direction:       "forward",
		Origin:          string(origin),
		Note:            types.NormalizeRelationNote(note),
		CreatedBy:       actorID,
	}
	reverse := &types.KnowledgeRelation{
		TenantID:        tenantID,
		FromKnowledgeID: toID,
		ToKnowledgeID:   fromID,
		RelationType:    string(relationType.Inverse()),
		PairKey:         pairKey,
		Direction:       "reverse",
		Origin:          string(origin),
		Note:            types.NormalizeRelationNote(note),
		CreatedBy:       actorID,
	}
	if err := s.repo.CreatePair(ctx, forward, reverse); err != nil {
		return nil, err
	}
	return forward, nil
}

// Unlink removes both directions.
func (s *knowledgeRelationService) Unlink(
	ctx context.Context, tenantID uint64, fromID, toID string,
	relationType types.KnowledgeRelationType,
) (int64, error) {
	if tenantID == 0 {
		return 0, apperrors.NewUnauthorizedError("tenant context is required")
	}
	if fromID == "" || toID == "" {
		return 0, apperrors.NewBadRequestError("from_knowledge_id and to_knowledge_id are required")
	}
	if relationType == "" {
		relationType = types.RelationRelated
	}
	if !relationType.Valid() {
		return 0, apperrors.NewBadRequestError("unknown relation type")
	}
	return s.repo.DeletePair(ctx, tenantID, fromID, toID, string(relationType))
}

// List returns the reader-facing views of a document's edges.
func (s *knowledgeRelationService) List(
	ctx context.Context, tenantID uint64, knowledgeID string,
) ([]types.KnowledgeRelationView, error) {
	if knowledgeID == "" {
		return nil, apperrors.NewBadRequestError("knowledge_id is required")
	}
	relations, err := s.repo.ListForKnowledge(ctx, tenantID, knowledgeID)
	if err != nil {
		return nil, err
	}
	// 双向存储意味着同一个逻辑边会出现两次（正向 + 反向）。视图层只保留
	// 正向行：反向行是"别人指向我"的镜像，重复展示会让页面上每个关联都
	// 出现两遍。
	forward := make([]*types.KnowledgeRelation, 0, len(relations))
	for _, relation := range relations {
		if relation == nil || relation.IsReverse() {
			continue
		}
		if relation.FromKnowledgeID != knowledgeID && relation.ToKnowledgeID != knowledgeID {
			continue
		}
		forward = append(forward, relation)
	}
	return types.BuildRelationViews(knowledgeID, forward), nil
}

// DeleteImpact reports how many edges a delete would orphan.
func (s *knowledgeRelationService) DeleteImpact(
	ctx context.Context, tenantID uint64, knowledgeID string,
) (int64, error) {
	if knowledgeID == "" {
		return 0, nil
	}
	return s.repo.CountForKnowledge(ctx, tenantID, knowledgeID)
}

// OnKnowledgeDeleted removes every edge touching a deleted document.
func (s *knowledgeRelationService) OnKnowledgeDeleted(
	ctx context.Context, tenantID uint64, knowledgeID string,
) (int64, error) {
	if knowledgeID == "" {
		return 0, nil
	}
	return s.repo.DeleteAllForKnowledge(ctx, tenantID, knowledgeID)
}
