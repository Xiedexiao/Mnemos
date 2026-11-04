package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// AnswerConversionService turns one answered turn into an experience candidate
// (KnowAxis V3.0 §13 回答→知识沉淀).
//
// 它只做"把一次问答整理成候选卡片"这一件事，发布仍走
// KnowledgeCandidateService 的既有审核与门禁路径——答案由模型生成，跳过
// 审核直接成为正式知识等于让模型自己给自己盖章。
type AnswerConversionService interface {
	// Convert creates (or reuses) the candidate for one Evidence Pack.
	Convert(
		ctx context.Context, tenantID uint64, actorID string,
		input types.AnswerConversionInput,
	) (*types.AnswerConversionResult, error)
}

// AnswerConversionRepository supplies the answered turn being converted.
type AnswerConversionRepository interface {
	// GetAnsweredTurn returns the Evidence Pack for one request id, scoped to
	// the tenant. A missing row is an error the service maps to 404.
	GetAnsweredTurn(
		ctx context.Context, tenantID uint64, requestID string,
	) (*types.EvidencePack, error)
	// FindCandidateBySource returns an existing candidate created from the
	// same (source type, request id), which is what makes conversion
	// idempotent under a double click.
	FindCandidateBySource(
		ctx context.Context, tenantID uint64, sourceType, sourceID string,
	) (*types.KnowledgeCandidate, error)
}
