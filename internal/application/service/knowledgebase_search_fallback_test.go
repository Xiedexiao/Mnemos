package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/models/embedding"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// fallbackTestEmbedder returns vectors of a fixed width and can fail on demand.
type fallbackTestEmbedder struct {
	embedding.Embedder

	name       string
	dimensions int
	vectors    int
	err        error
	// embedCalls counts how often this model was actually invoked.
	embedCalls *int
}

func (e fallbackTestEmbedder) GetModelName() string { return e.name }
func (e fallbackTestEmbedder) GetDimensions() int   { return e.dimensions }

func (e fallbackTestEmbedder) Embed(context.Context, string) ([]float32, error) {
	if e.embedCalls != nil {
		*e.embedCalls++
	}
	if e.err != nil {
		return nil, e.err
	}
	width := e.vectors
	if width == 0 {
		width = e.dimensions
	}
	return make([]float32, width), nil
}

// fallbackTestModels serves named embedders from a map.
type fallbackTestModels struct {
	interfaces.ModelService

	models map[string]embedding.Embedder
	// loadErr fails every lookup.
	loadErr error
	// requested records lookups in order.
	requested []string
}

func (s *fallbackTestModels) GetEmbeddingModel(
	_ context.Context, id string,
) (embedding.Embedder, error) {
	s.requested = append(s.requested, id)
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	model, ok := s.models[id]
	if !ok {
		return nil, errors.New("model not found")
	}
	return model, nil
}

func (s *fallbackTestModels) GetEmbeddingModelForTenant(
	ctx context.Context, id string, _ uint64,
) (embedding.Embedder, error) {
	return s.GetEmbeddingModel(ctx, id)
}

// stubEmbeddingBudgets serves a fallback chain.
type stubEmbeddingBudgets struct {
	interfaces.TokenBudgetService

	candidates []string
	// scenes records requested scenes.
	scenes []string
}

func (s *stubEmbeddingBudgets) Candidates(
	_ context.Context, _ uint64, scene, primaryModelID string,
) []string {
	s.scenes = append(s.scenes, scene)
	if len(s.candidates) == 0 {
		return []string{primaryModelID}
	}
	return s.candidates
}

// embeddingFixture builds a service around one knowledge base.
func embeddingFixture(
	models *fallbackTestModels, budgets interfaces.TokenBudgetService,
) (*knowledgeBaseService, context.Context) {
	svc := &knowledgeBaseService{modelService: models, tokenBudgets: budgets}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(1))
	return svc, ctx
}

// TestGetQueryEmbeddingWithoutChainUsesTheIndexModel is the compatibility rule:
// an unconfigured deployment must behave exactly as before.
func TestGetQueryEmbeddingWithoutChainUsesTheIndexModel(t *testing.T) {
	models := &fallbackTestModels{models: map[string]embedding.Embedder{
		"index-model": dimensionTestEmbedder{name: "index", dimensions: 768},
	}}
	svc, ctx := embeddingFixture(models, nil)
	svc.repo = &embeddingTestKBRepo{kb: &types.KnowledgeBase{
		ID: "kb-1", TenantID: 1, EmbeddingModelID: "index-model",
	}}

	vector, err := svc.GetQueryEmbedding(ctx, "kb-1", "q")
	require.NoError(t, err)
	assert.Len(t, vector, 768)
	assert.Equal(t, []string{"index-model"}, models.requested)
}

// TestGetQueryEmbeddingFallsBackWhenTheIndexModelFails covers the headline
// behaviour: a same-width backup serves the query when the primary cannot load.
func TestGetQueryEmbeddingFallsBackWhenTheIndexModelFails(t *testing.T) {
	failingEmbedCalls := 0
	models := &fallbackTestModels{models: map[string]embedding.Embedder{
		"index-model": fallbackTestEmbedder{
			name: "index", dimensions: 768, err: errors.New("provider 503"),
			embedCalls: &failingEmbedCalls,
		},
		"backup-model": dimensionTestEmbedder{name: "backup", dimensions: 768},
	}}
	budgets := &stubEmbeddingBudgets{candidates: []string{"index-model", "backup-model"}}
	svc, ctx := embeddingFixture(models, budgets)
	svc.repo = &embeddingTestKBRepo{kb: &types.KnowledgeBase{
		ID: "kb-1", TenantID: 1, EmbeddingModelID: "index-model",
	}}

	vector, err := svc.GetQueryEmbedding(ctx, "kb-1", "q")
	require.NoError(t, err)
	assert.Len(t, vector, 768)
	assert.Equal(t, []string{"index-model", "backup-model"}, models.requested,
		"the primary is tried first")
	assert.Equal(t, 1, failingEmbedCalls)
	assert.Equal(t, []string{types.EmbeddingFallbackScene}, budgets.scenes)
}

// TestGetQueryEmbeddingRefusesDimensionMismatch is the safety rule: a vector
// from a differently-sized model cannot be compared with the index.
func TestGetQueryEmbeddingRefusesDimensionMismatch(t *testing.T) {
	models := &fallbackTestModels{models: map[string]embedding.Embedder{
		"index-model": fallbackTestEmbedder{
			name: "index", dimensions: 1024, err: errors.New("provider 503"),
		},
		"narrow-model": dimensionTestEmbedder{name: "narrow", dimensions: 768},
	}}
	budgets := &stubEmbeddingBudgets{candidates: []string{"index-model", "narrow-model"}}
	svc, ctx := embeddingFixture(models, budgets)
	svc.repo = &embeddingTestKBRepo{kb: &types.KnowledgeBase{
		ID: "kb-1", TenantID: 1, EmbeddingModelID: "index-model",
	}}

	_, err := svc.GetQueryEmbedding(ctx, "kb-1", "q")
	require.Error(t, err, "an incompatible fallback must fail rather than return garbage")
}

// TestGetQueryEmbeddingTraceMarksLossyDegrade proves the caller can tell that
// results are approximate after a degrade.
func TestGetQueryEmbeddingTraceMarksLossyDegrade(t *testing.T) {
	models := &fallbackTestModels{models: map[string]embedding.Embedder{
		"index-model": fallbackTestEmbedder{
			name: "index", dimensions: 768, err: errors.New("provider 503"),
		},
		"backup-model": dimensionTestEmbedder{name: "backup", dimensions: 768},
	}}
	budgets := &stubEmbeddingBudgets{candidates: []string{"index-model", "backup-model"}}
	svc, ctx := embeddingFixture(models, budgets)
	svc.repo = &embeddingTestKBRepo{kb: &types.KnowledgeBase{
		ID: "kb-1", TenantID: 1, EmbeddingModelID: "index-model",
	}}

	_, trace, err := svc.getQueryEmbeddingWithTrace(ctx, "kb-1", "q")
	require.NoError(t, err)
	assert.True(t, trace.Degraded)
	assert.True(t, trace.Lossy, "a different model means a different vector space")
	assert.Equal(t, "index-model", trace.IndexModelID)
	assert.Equal(t, "backup-model", trace.ServedBy)
	assert.Len(t, trace.Attempts, 2)
	assert.NotEmpty(t, trace.Attempts[0].Err, "the failed primary's reason is recorded")
	assert.Empty(t, trace.Attempts[1].Err)
}

// TestGetQueryEmbeddingTraceMarksHealthyPath proves a normal call is not
// reported as degraded.
func TestGetQueryEmbeddingTraceMarksHealthyPath(t *testing.T) {
	models := &fallbackTestModels{models: map[string]embedding.Embedder{
		"index-model": dimensionTestEmbedder{name: "index", dimensions: 768},
	}}
	svc, ctx := embeddingFixture(models, nil)
	svc.repo = &embeddingTestKBRepo{kb: &types.KnowledgeBase{
		ID: "kb-1", TenantID: 1, EmbeddingModelID: "index-model",
	}}

	_, trace, err := svc.getQueryEmbeddingWithTrace(ctx, "kb-1", "q")
	require.NoError(t, err)
	assert.False(t, trace.Degraded)
	assert.False(t, trace.Lossy)
	assert.Equal(t, "index-model", trace.ServedBy)
}

// TestGetQueryEmbeddingReportsWhenEveryModelFails keeps the failure legible.
func TestGetQueryEmbeddingReportsWhenEveryModelFails(t *testing.T) {
	models := &fallbackTestModels{
		loadErr: errors.New("model service unavailable"),
	}
	budgets := &stubEmbeddingBudgets{candidates: []string{"m1", "m2"}}
	svc, ctx := embeddingFixture(models, budgets)
	svc.repo = &embeddingTestKBRepo{kb: &types.KnowledgeBase{
		ID: "kb-1", TenantID: 1, EmbeddingModelID: "m1",
	}}

	_, trace, err := svc.getQueryEmbeddingWithTrace(ctx, "kb-1", "q")
	require.Error(t, err)
	assert.False(t, trace.Degraded)
	assert.Len(t, trace.Attempts, 2, "every attempt is recorded for the audit")
}

// embeddingTestKBRepo serves one knowledge base by id.
type embeddingTestKBRepo struct {
	interfaces.KnowledgeBaseRepository

	kb  *types.KnowledgeBase
	err error
}

func (r *embeddingTestKBRepo) GetKnowledgeBaseByID(
	context.Context, string,
) (*types.KnowledgeBase, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.kb, nil
}
