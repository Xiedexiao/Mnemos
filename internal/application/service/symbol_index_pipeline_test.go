package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// TestClassifySymbolDrawingAndPartNumbers covers §12.7's requirement that 图号
// and 物料号 form their own exact-match channels. Before this they all fell
// through to the generic "code" bucket, so an operator could not filter by
// drawing number or part number at all.
func TestClassifySymbolDrawingAndPartNumbers(t *testing.T) {
	cases := map[string]string{
		"D-100":         types.SymbolTypeDrawingNumber,
		"DWG-2024-01":   types.SymbolTypeDrawingNumber,
		"PN-4471":       types.SymbolTypePartNumber,
		"PART-88231":    types.SymbolTypePartNumber,
		"MAT-09":        types.SymbolTypePartNumber,
		"E023":          types.SymbolTypeErrorCode,
		"ERR-042":       types.SymbolTypeErrorCode,
		"XR-5000":       types.SymbolTypeModel,
		"SOP-EQ-012":    types.SymbolTypeDocumentNumber,
		"GB-50316-2016": types.SymbolTypeDocumentNumber,
	}
	for symbol, want := range cases {
		t.Run(symbol, func(t *testing.T) {
			assert.Equal(t, want, classifySymbol(symbol))
			assert.True(t, types.IsValidSymbolType(want), "%s must be a valid symbol_type", want)
		})
	}
}

// TestDrawingNumberOutranksDocumentShape pins the precedence decision: a drawing
// number also matches the multi-segment document shape, but "图号" is what an
// operator searches for.
func TestDrawingNumberOutranksDocumentShape(t *testing.T) {
	assert.True(t, symbolDocumentShape.MatchString("DWG-2024-01"),
		"precondition: the document shape matches too")
	assert.Equal(t, types.SymbolTypeDrawingNumber, classifySymbol("DWG-2024-01"))
}

// stubSymbolIndex records indexing calls so the post-process hook can be
// asserted without a database.
type stubSymbolIndex struct {
	interfaces.SymbolIndexService
	deleted   []string
	indexed   []string
	failChunk string
}

func (s *stubSymbolIndex) DeleteByKnowledge(_ context.Context, _ uint64, knowledgeID string) error {
	s.deleted = append(s.deleted, knowledgeID)
	return nil
}

func (s *stubSymbolIndex) IndexSymbols(
	_ context.Context, _ uint64, _, _, chunkID, _ string,
) ([]*types.SymbolIndex, error) {
	if chunkID == s.failChunk {
		return nil, assert.AnError
	}
	s.indexed = append(s.indexed, chunkID)
	return nil, nil
}

// TestPostProcessRebuildsSymbolIndex covers the missing producer: precise
// symbol search never worked because nothing indexed at ingest time.
func TestPostProcessRebuildsSymbolIndex(t *testing.T) {
	stub := &stubSymbolIndex{}
	svc := &KnowledgePostProcessService{symbols: stub}
	payload := types.KnowledgePostProcessPayload{
		TenantID: 7, KnowledgeID: "doc-1", KnowledgeBaseID: "kb-1",
	}
	chunks := []*types.Chunk{
		{ID: "chunk-1", Content: "见 SOP-EQ-012 第 5 条"},
		{ID: "chunk-2", Content: "   "}, // blank: skipped
		{ID: "chunk-3", Content: "图号 D-100 替代 DWG-2024-01"},
	}
	svc.indexSymbolsForKnowledge(context.Background(), payload, chunks)

	assert.Equal(t, []string{"doc-1"}, stub.deleted,
		"the old index is cleared first so stale numbers disappear")
	assert.Equal(t, []string{"chunk-1", "chunk-3"}, stub.indexed)
}

// TestPostProcessSymbolIndexIsBestEffort keeps one bad chunk from failing the
// whole post-process, which also drives summary/graph/wiki generation.
func TestPostProcessSymbolIndexIsBestEffort(t *testing.T) {
	stub := &stubSymbolIndex{failChunk: "chunk-1"}
	svc := &KnowledgePostProcessService{symbols: stub}
	payload := types.KnowledgePostProcessPayload{TenantID: 7, KnowledgeID: "doc-1", KnowledgeBaseID: "kb-1"}
	chunks := []*types.Chunk{
		{ID: "chunk-1", Content: "E023"},
		{ID: "chunk-2", Content: "SOP-EQ-012"},
	}
	svc.indexSymbolsForKnowledge(context.Background(), payload, chunks)
	assert.Equal(t, []string{"chunk-2"}, stub.indexed, "the remaining chunks still get indexed")
}

// TestPostProcessSymbolIndexNilServiceIsNoop keeps Lite mode working.
func TestPostProcessSymbolIndexNilServiceIsNoop(t *testing.T) {
	svc := &KnowledgePostProcessService{}
	require.NotPanics(t, func() {
		svc.indexSymbolsForKnowledge(context.Background(),
			types.KnowledgePostProcessPayload{TenantID: 7, KnowledgeID: "doc-1"},
			[]*types.Chunk{{ID: "chunk-1", Content: "E023"}})
	})
}
