package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// stubConversionRepo serves answered turns and existing candidates.
type stubConversionRepo struct {
	interfaces.AnswerConversionRepository

	pack     *types.EvidencePack
	existing *types.KnowledgeCandidate
	packErr  error
	findErr  error
	// lookups records the (sourceType, sourceID) pairs that were searched.
	lookups [][2]string
}

func (s *stubConversionRepo) GetAnsweredTurn(
	_ context.Context, _ uint64, _ string,
) (*types.EvidencePack, error) {
	if s.packErr != nil {
		return nil, s.packErr
	}
	return s.pack, nil
}

func (s *stubConversionRepo) FindCandidateBySource(
	_ context.Context, _ uint64, sourceType, sourceID string,
) (*types.KnowledgeCandidate, error) {
	s.lookups = append(s.lookups, [2]string{sourceType, sourceID})
	if s.findErr != nil {
		return nil, s.findErr
	}
	return s.existing, nil
}

// stubCandidateCreator records what the conversion asked to create.
type stubCandidateCreator struct {
	interfaces.KnowledgeCandidateService

	created *types.KnowledgeCandidate
	err     error
	got     *types.KnowledgeCandidateCreateInput
	calls   int
}

func (s *stubCandidateCreator) CreateCandidate(
	_ context.Context, input types.KnowledgeCandidateCreateInput,
) (*types.KnowledgeCandidate, error) {
	s.calls++
	s.got = &input
	if s.err != nil {
		return nil, s.err
	}
	if s.created != nil {
		return s.created, nil
	}
	// Encode the evidence the way the store does, so EvidenceCount is real.
	raw, err := json.Marshal(input.Evidence)
	if err != nil {
		raw = []byte("[]")
	}
	return &types.KnowledgeCandidate{
		ID: "cand-1", Title: input.Title, Evidence: types.JSON(raw),
	}, nil
}

// answerPack builds an answered turn.
func answerPack(answer string) *types.EvidencePack {
	pack := &types.EvidencePack{
		RequestID: "req-1",
		Question:  "E45 设备报警怎么处理？",
		Answer:    answer,
	}
	pack.SetCitations([]types.EvidenceCitation{
		{Index: 1, KnowledgeID: "doc-1", ChunkID: "chunk-1", Title: "报警处理规程"},
	})
	return pack
}

func newConversionServiceForTest(
	repo interfaces.AnswerConversionRepository,
	creator interfaces.KnowledgeCandidateService,
) interfaces.AnswerConversionService {
	return NewAnswerConversionService(repo, creator)
}

// TestConvertAnswerCreatesCandidate covers the happy path end to end.
func TestConvertAnswerCreatesCandidate(t *testing.T) {
	repo := &stubConversionRepo{pack: answerPack("先断电，再复位。")}
	creator := &stubCandidateCreator{}
	svc := newConversionServiceForTest(repo, creator)

	result, err := svc.Convert(context.Background(), 7, "ana",
		types.AnswerConversionInput{RequestID: "req-1"})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Created)
	assert.Equal(t, "cand-1", result.CandidateID)
	assert.Equal(t, "req-1", result.SourceRequestID)
	assert.Equal(t, 1, result.EvidenceCount)

	require.NotNil(t, creator.got)
	assert.Equal(t, types.KnowledgeCandidateSourceQAAnswer, creator.got.SourceType)
	assert.Equal(t, "req-1", creator.got.SourceID, "the request id is the idempotency key")
	assert.Equal(t, types.KnowledgeCandidateLevelL1, creator.got.Level,
		"conversion never grants a level above the review floor")
	assert.Equal(t, types.KnowledgeCandidateConfidenceAmbiguous, creator.got.Confidence,
		"a model answer is not declared extracted")
	assert.Equal(t, "ana", creator.got.RequestedBy)
	assert.Equal(t, "E45 设备报警怎么处理", creator.got.Title)
	require.NotNil(t, creator.got.Card)
	assert.Equal(t, "先断电，再复位。", creator.got.Card.What)
}

// TestConvertAnswerIsIdempotent is the double-click rule.
func TestConvertAnswerIsIdempotent(t *testing.T) {
	repo := &stubConversionRepo{
		pack:     answerPack("答案"),
		existing: &types.KnowledgeCandidate{ID: "cand-existing", Title: "已转换"},
	}
	creator := &stubCandidateCreator{}
	svc := newConversionServiceForTest(repo, creator)

	result, err := svc.Convert(context.Background(), 7, "ana",
		types.AnswerConversionInput{RequestID: "req-1"})
	require.NoError(t, err)
	assert.False(t, result.Created, "a retry reuses the existing candidate")
	assert.Equal(t, "cand-existing", result.CandidateID)
	assert.Equal(t, 0, creator.calls, "no second candidate is created")
	assert.Equal(t, [2]string{"qa_answer", "req-1"}, repo.lookups[0],
		"the idempotency lookup is keyed on the source pair")
}

// TestConvertAnswerRejectsRefusal is the eligibility rule at the service layer.
func TestConvertAnswerRejectsRefusal(t *testing.T) {
	pack := answerPack("……")
	pack.Refusal = string(types.EvidenceRefusalConflict)
	creator := &stubCandidateCreator{}
	svc := newConversionServiceForTest(&stubConversionRepo{pack: pack}, creator)

	_, err := svc.Convert(context.Background(), 7, "ana",
		types.AnswerConversionInput{RequestID: "req-1"})
	require.Error(t, err)
	assert.Equal(t, 0, creator.calls, "a refusal never becomes a candidate")
}

// TestConvertAnswerRejectsL3 is the gate rule: conversion cannot self-grant
// authority.
func TestConvertAnswerRejectsL3(t *testing.T) {
	creator := &stubCandidateCreator{}
	svc := newConversionServiceForTest(
		&stubConversionRepo{pack: answerPack("答案")}, creator)

	_, err := svc.Convert(context.Background(), 7, "ana", types.AnswerConversionInput{
		RequestID: "req-1", Level: "L3",
	})
	require.Error(t, err)
	assert.Equal(t, 0, creator.calls)
}

// TestConvertAnswerAllowsL2 proves the caller can still escalate one step.
func TestConvertAnswerAllowsL2(t *testing.T) {
	creator := &stubCandidateCreator{}
	svc := newConversionServiceForTest(
		&stubConversionRepo{pack: answerPack("答案")}, creator)

	_, err := svc.Convert(context.Background(), 7, "ana", types.AnswerConversionInput{
		RequestID: "req-1", Level: "L2",
	})
	require.NoError(t, err)
	require.NotNil(t, creator.got)
	assert.Equal(t, types.KnowledgeCandidateLevelL2, creator.got.Level)
}

// TestConvertAnswerMapsMissingPackToNotFound keeps the 404 contract.
func TestConvertAnswerMapsMissingPackToNotFound(t *testing.T) {
	svc := newConversionServiceForTest(&stubConversionRepo{
		packErr: repository.ErrAnswerSourceNotFound,
	}, &stubCandidateCreator{})

	_, err := svc.Convert(context.Background(), 7, "ana",
		types.AnswerConversionInput{RequestID: "missing"})
	require.Error(t, err)
}

// TestConvertAnswerRequiresIdentity refuses an anonymous conversion.
func TestConvertAnswerRequiresIdentity(t *testing.T) {
	svc := newConversionServiceForTest(&stubConversionRepo{pack: answerPack("答案")},
		&stubCandidateCreator{})

	_, err := svc.Convert(context.Background(), 0, "ana",
		types.AnswerConversionInput{RequestID: "req-1"})
	require.Error(t, err)

	_, err = svc.Convert(context.Background(), 7, "  ",
		types.AnswerConversionInput{RequestID: "req-1"})
	require.Error(t, err, "an empty actor would make the candidate unattributable")
}

// TestConvertAnswerRequiresRequestID covers the payload validation.
func TestConvertAnswerRequiresRequestID(t *testing.T) {
	svc := newConversionServiceForTest(&stubConversionRepo{pack: answerPack("答案")},
		&stubCandidateCreator{})
	_, err := svc.Convert(context.Background(), 7, "ana", types.AnswerConversionInput{})
	require.Error(t, err)
}

// TestConvertAnswerSurfacesCreateFailure keeps a store error from being hidden.
func TestConvertAnswerSurfacesCreateFailure(t *testing.T) {
	creator := &stubCandidateCreator{err: errors.New("db down")}
	svc := newConversionServiceForTest(&stubConversionRepo{pack: answerPack("答案")}, creator)

	_, err := svc.Convert(context.Background(), 7, "ana",
		types.AnswerConversionInput{RequestID: "req-1"})
	require.Error(t, err)
}

// TestConvertAnswerTruncatesLongAnswer covers the copy limit end to end.
func TestConvertAnswerTruncatesLongAnswer(t *testing.T) {
	long := ""
	for i := 0; i < types.AnswerConversionMaxAnswerRunes+20; i++ {
		long += "字"
	}
	creator := &stubCandidateCreator{}
	svc := newConversionServiceForTest(&stubConversionRepo{pack: answerPack(long)}, creator)

	result, err := svc.Convert(context.Background(), 7, "ana",
		types.AnswerConversionInput{RequestID: "req-1"})
	require.NoError(t, err)
	assert.True(t, result.TruncatedAnswer)
	require.NotNil(t, creator.got)
	assert.LessOrEqual(t, len([]rune(creator.got.Card.What)), types.AnswerConversionMaxAnswerRunes+100,
		"the card carries the truncated body plus the truncation note")
}
