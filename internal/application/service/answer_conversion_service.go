package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// answerConversionService implements interfaces.AnswerConversionService
// (KnowAxis V3.0 §13 回答→知识沉淀).
//
// 它把一次已回答的问答整理成经验候选，然后交给既有的候选审核路径。三个
// 决定写在这里而不是散在 handler 里：
//  1. 拒答/空答案直接拒绝（400）：没有内容可沉淀；
//  2. 幂等：同一 request_id 重复转换返回已有候选（200 + created=false），
//     一键按钮太容易被连点；
//  3. level 默认 L1 且**不允许**一次给定 L3：L3 是"可作权威答案"的层级，
//     必须走 §10.4 的门禁（Admin 且 confidence 非含糊），不能靠转换动作
//     自己升上去。
type answerConversionService struct {
	repo       interfaces.AnswerConversionRepository
	candidates interfaces.KnowledgeCandidateService
	now        func() time.Time
}

// NewAnswerConversionService constructs the service.
func NewAnswerConversionService(
	repo interfaces.AnswerConversionRepository,
	candidates interfaces.KnowledgeCandidateService,
) interfaces.AnswerConversionService {
	return &answerConversionService{
		repo: repo, candidates: candidates, now: time.Now,
	}
}

// Convert turns one answered turn into a candidate.
func (s *answerConversionService) Convert(
	ctx context.Context, tenantID uint64, actorID string,
	input types.AnswerConversionInput,
) (*types.AnswerConversionResult, error) {
	if tenantID == 0 {
		return nil, apperrors.NewUnauthorizedError("tenant context is required")
	}
	if strings.TrimSpace(actorID) == "" {
		return nil, apperrors.NewUnauthorizedError("caller identity is required")
	}
	requestID, err := types.ValidateAnswerConversionInput(input)
	if err != nil {
		return nil, apperrors.NewBadRequestError(err.Error())
	}

	// 幂等先于取数：重复点击时不必再读一次证据包。
	existing, err := s.repo.FindCandidateBySource(
		ctx, tenantID, string(types.KnowledgeCandidateSourceQAAnswer), requestID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return &types.AnswerConversionResult{
			CandidateID:     existing.ID,
			Created:         false,
			Title:           existing.Title,
			SourceRequestID: requestID,
			EvidenceCount:   len(existing.Evidence),
		}, nil
	}

	pack, err := s.repo.GetAnsweredTurn(ctx, tenantID, requestID)
	if err != nil {
		if errors.Is(err, repository.ErrAnswerSourceNotFound) {
			return nil, apperrors.NewNotFoundError("answered turn not found")
		}
		return nil, err
	}
	if eligibleErr := types.AnswerConversionEligible(pack); eligibleErr != nil {
		return nil, apperrors.NewBadRequestError(eligibleErr.Error())
	}

	// L3 不能由转换动作直接给出（见类型注释）。给 L3 的请求按参数错误拒绝，
	// 而不是静默降级——静默降级会让调用方以为自己要到了权威层级。
	level := strings.TrimSpace(input.Level)
	if level == "" {
		level = string(types.KnowledgeCandidateLevelL1)
	}
	if types.KnowledgeCandidateLevel(level) == types.KnowledgeCandidateLevelL3 {
		return nil, apperrors.NewBadRequestError(
			"L3 is granted by the publish gate, not by conversion; convert at L1 or L2")
	}

	title := types.AnswerConversionTitle(pack.Question, input.Title)
	truncatedAnswer, truncated := types.TruncateAnswerForConversion(pack.Answer)
	// 复制一份 pack 的答案，让卡片构造看到截断后的内容（卡片本身不该再截断）。
	trimmed := *pack
	trimmed.Answer = truncatedAnswer
	card := types.BuildAnswerConversionCard(&trimmed, truncated)
	evidence := types.BuildAnswerConversionEvidence(pack)

	candidate, err := s.candidates.CreateCandidate(ctx, types.KnowledgeCandidateCreateInput{
		SourceType: types.KnowledgeCandidateSourceQAAnswer,
		SourceID:   requestID,
		Title:      title,
		Level:      types.KnowledgeCandidateLevel(level),
		Card:       &card,
		Evidence:   evidence,
		// 答案来自模型：默认置信度是 AMBIGUOUS，让 L3 门禁自然拦在发布前。
		// 转换动作不替模型声明"这条已经提取确认过"。
		Confidence:  types.KnowledgeCandidateConfidenceAmbiguous,
		RequestedBy: actorID,
	})
	if err != nil {
		return nil, err
	}
	return &types.AnswerConversionResult{
		CandidateID:     candidate.ID,
		Created:         true,
		Title:           candidate.Title,
		SourceRequestID: requestID,
		EvidenceCount:   len(evidence),
		TruncatedAnswer: truncated,
	}, nil
}

// compile-time guard.
var _ interfaces.AnswerConversionService = (*answerConversionService)(nil)
