package service

import (
	"context"
	"regexp"
	"sort"
	"strings"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// Recall caps bounding one extraction/lookup round-trip.
const (
	// maxSymbolsPerIndexBatch bounds one IndexSymbols call so a
	// pathological text cannot fan out into an unbounded batch insert
	// (256 rows ≈ a symbol-dense specification page, far above typical).
	maxSymbolsPerIndexBatch = 256
	// maxSymbolLookupTerms bounds one ExactLookup query extraction
	// (32 symbols ≈ the glossary expansion cap; a query rarely carries
	// more identifiers than that).
	maxSymbolLookupTerms = 32
	// maxSymbolLen mirrors the migration's varchar(255); longer tokens
	// are prose, not identifiers.
	maxSymbolLen = 255
	// minSymbolLen: ≥2 letters + ≥1 digit (the loosest qualifying shape).
	minSymbolLen = 3
)

// Symbol extraction/shapes. See extractSymbolCandidates for the recall
// scope these encode.
var (
	// symbolSpaceAroundSeparator folds whitespace glued onto a [-_]
	// separator so "SOP - EQ - 012" normalizes to "SOP-EQ-012". Plain
	// whitespace stays a delimiter — it never merges two separate words.
	symbolSpaceAroundSeparator = regexp.MustCompile(`\s*([_-])\s*`)
	// symbolTokenRun matches a maximal run of the symbol alphabet;
	// leading/trailing separators are trimmed afterwards.
	symbolTokenRun = regexp.MustCompile(`[A-Z0-9][A-Z0-9_-]*`)
	// symbolErrorCodeShape is the single-letter error-code shape
	// (E023 / E-23) that the ≥2-letters rule misses.
	symbolErrorCodeShape = regexp.MustCompile(`^E-?[0-9]{2,}$`)
	// symbolErrorCodeNameShape is the named error-code shape
	// (ERR-042 / ERR042 / ERROR42) — 前缀明示类别，压过 model 形状。
	symbolErrorCodeNameShape = regexp.MustCompile(`^(?:ERR|ERROR)[-_.]?[0-9]{2,}$`)
	// symbolModelShape is the model shape: a short letter segment then a
	// digit segment (XR-5000 / XR5000 / AB12C).
	symbolModelShape = regexp.MustCompile(`^[A-Z]{1,5}[-_]?[0-9]{2,}[A-Z]{0,2}$`)
	// symbolDocumentShape is the multi-segment document number:
	// ≥3 separator-joined segments (SOP-EQ-012, GB-50316-2016).
	symbolDocumentShape = regexp.MustCompile(`^[A-Z][A-Z0-9]*(?:[-_][A-Z0-9]+){2,}$`)
)

// symbolIndexService implements interfaces.SymbolIndexService.
type symbolIndexService struct {
	repo interfaces.SymbolIndexRepository
}

// NewSymbolIndexService constructs the symbol index service.
func NewSymbolIndexService(repo interfaces.SymbolIndexRepository) interfaces.SymbolIndexService {
	return &symbolIndexService{repo: repo}
}

// IndexSymbols extracts symbol candidates from text and batch-upserts
// them for (kbID, knowledgeID, chunkID). chunkID=="" indexes
// document-level rows (整篇).
//
// 幂等性：同一 (knowledge, chunk) 重复建索引走 ON CONFLICT 刷新
// symbol_type/locator；chunk 内容改写后旧符号不自动清除（v1 不做
// diff-清理），需要重建时由调用方先 DeleteByKnowledge 再重灌——
// chunk 级更新钩子留待后续。
func (s *symbolIndexService) IndexSymbols(
	ctx context.Context, tenantID uint64, kbID, knowledgeID, chunkID, text string,
) ([]*types.SymbolIndex, error) {
	kbID = strings.TrimSpace(kbID)
	knowledgeID = strings.TrimSpace(knowledgeID)
	chunkID = strings.TrimSpace(chunkID)
	if kbID == "" || knowledgeID == "" {
		return nil, apperrors.NewBadRequestError("kb_id and knowledge_id are required")
	}
	if strings.TrimSpace(text) == "" {
		return nil, apperrors.NewBadRequestError("text is required")
	}
	candidates := extractSymbolCandidates(text)
	if len(candidates) == 0 {
		return []*types.SymbolIndex{}, nil
	}
	rows := make([]*types.SymbolIndex, 0, len(candidates))
	for _, sym := range candidates {
		rows = append(rows, &types.SymbolIndex{
			TenantID:        tenantID,
			KnowledgeBaseID: kbID,
			KnowledgeID:     knowledgeID,
			ChunkID:         chunkID,
			Symbol:          sym,
			SymbolType:      classifySymbol(sym),
			// locator：v1 文本通道拿不到页码/段落，落 NULL；文档解析侧
			// （source blocks 按字符区间回填）接入时再补。
		})
	}
	if err := s.repo.UpsertSymbols(ctx, rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// ExactLookup extracts symbol candidates from query (same extraction as
// indexing, so both sides normalize identically) and returns the exact
// hits as [{knowledge_id, chunk_id, symbol, locator}], longest symbol
// first — a longer match is a more specific identifier
// (SOP-EQ-012 outranks its E023 tail).
func (s *symbolIndexService) ExactLookup(
	ctx context.Context, tenantID uint64, kbIDs []string, query string,
) ([]*types.SymbolHit, error) {
	candidates := extractSymbolCandidates(query)
	if len(candidates) == 0 {
		return []*types.SymbolHit{}, nil
	}
	if len(candidates) > maxSymbolLookupTerms {
		candidates = candidates[:maxSymbolLookupTerms]
	}
	rows, err := s.repo.FindBySymbols(ctx, tenantID, kbIDs, candidates)
	if err != nil {
		return nil, err
	}
	hits := make([]*types.SymbolHit, 0, len(rows))
	for _, row := range rows {
		hits = append(hits, &types.SymbolHit{
			KnowledgeID: row.KnowledgeID,
			ChunkID:     row.ChunkID,
			Symbol:      row.Symbol,
			Locator:     row.Locator,
		})
	}
	// 越具体越优先：符号更长者更具体；等长按字典序保证确定性。仓储已按
	// 同序返回，这里再排一次让排序契约不依赖方言的 LENGTH 语义。
	sort.SliceStable(hits, func(i, j int) bool {
		li, lj := len(hits[i].Symbol), len(hits[j].Symbol)
		if li != lj {
			return li > lj
		}
		return hits[i].Symbol < hits[j].Symbol
	})
	return hits, nil
}

// ListByKnowledge lists the indexed symbols of one knowledge document.
func (s *symbolIndexService) ListByKnowledge(
	ctx context.Context, tenantID uint64, knowledgeID string,
) ([]*types.SymbolIndex, error) {
	return s.repo.ListByKnowledge(ctx, tenantID, knowledgeID)
}

// DeleteByKnowledge removes all symbol rows of one knowledge document.
//
// 知识删除联动（接线点预留，本垂直不改既有文件）：应在 knowledge 删除/
// 回收站物理清除成功后调用（service.KnowledgeService 的删除路径或对应
// 生命周期钩子），保证符号索引不残留死文档行。删除失败不阻断知识删除
// 本身（幂等，可由巡检重放）。
func (s *symbolIndexService) DeleteByKnowledge(ctx context.Context, tenantID uint64, knowledgeID string) error {
	return s.repo.DeleteByKnowledge(ctx, tenantID, knowledgeID)
}

// extractSymbolCandidates returns the deduped, normalized symbols in
// text, most-significant order preserved by first occurrence.
//
// 召回范围（v1 刻意宽进，容错说明）：
//   - 主规则：归一后 token 含 ≥2 个大写字母且 ≥1 个数字（任务给定正则
//     [A-Z]{2,}[-_]?[0-9][A-Z0-9-]* 的宽化——数字允许出现在任意位置，
//     以罩住 SOP-EQ-012 这类"字母-段-数字"交替的多段编号）；
//   - 例外：单字母 E 前缀错误码形 ^E-?[0-9]{2,}$（E023/E-23），规格把
//     E023 列为索引符号而主规则罩不住它；仅放行 E 前缀避免 "A1/B2"
//     级噪声；
//   - 归一：大写 + 折叠紧贴 [-_] 的空白（"sop - eq-012" → "SOP-EQ-012"）；
//     普通空格仍是词界，"SEE SOP 12" 不会被拼成一个符号。
//
// 已知误报（接受，检索上无害——查询侧同规则抽取，只有再次出现同形
// token 才命中）：USB3/HDMI2 等缩写+代数、UTF8、十六进制串、以及
// "PAGE 3-PAGE 5" 折叠出的 "3-PAGE" 类边界产物。已知漏报：纯数字编号
// （"50316"）、无数字纯字母代号（"HTTP"）、小写编号（归一后若原文
// 全小写仍可命中，但 "sop-eq-012" 与 "SOP-EQ-012" 同行——归一大写
// 吸收了大小写差异）、非 ASCII 符号（中文标准号 "GB 50316" 的两段
// 因空格切分不合并，v1 不做跨空格拼接）。精确分词/别名拼接等收紧
// 等检索侧接入后统一。
func extractSymbolCandidates(text string) []string {
	normalized := symbolSpaceAroundSeparator.ReplaceAllString(strings.ToUpper(text), "$1")
	raw := symbolTokenRun.FindAllString(normalized, -1)
	seen := make(map[string]struct{}, len(raw))
	candidates := make([]string, 0, len(raw))
	for _, tok := range raw {
		tok = strings.Trim(tok, "-_")
		if !isSymbolCandidate(tok) {
			continue
		}
		if _, dup := seen[tok]; dup {
			continue
		}
		seen[tok] = struct{}{}
		candidates = append(candidates, tok)
		if len(candidates) >= maxSymbolsPerIndexBatch {
			break
		}
	}
	return candidates
}

// isSymbolCandidate applies the recall rules documented on
// extractSymbolCandidates to one normalized token.
func isSymbolCandidate(tok string) bool {
	if len(tok) < minSymbolLen || len(tok) > maxSymbolLen {
		return false
	}
	letters, digits := 0, 0
	for i := 0; i < len(tok); i++ {
		switch {
		case tok[i] >= 'A' && tok[i] <= 'Z':
			letters++
		case tok[i] >= '0' && tok[i] <= '9':
			digits++
		}
	}
	if letters >= 2 && digits >= 1 {
		return true
	}
	// 单字母错误码形（E023 / E-23）——见 extractSymbolCandidates 的例外。
	return symbolErrorCodeShape.MatchString(tok)
}

// classifySymbol assigns symbol_type by shape. v1 启发式，仅信息性：
// 命中匹配只看 symbol 等值，type 误判不影响召回；part_number 形状与
// code 难区分，v1 不判定（枚举保留，人工标注走 custom）。
func classifySymbol(tok string) string {
	switch {
	case symbolErrorCodeShape.MatchString(tok), symbolErrorCodeNameShape.MatchString(tok):
		return types.SymbolTypeErrorCode
	case symbolModelShape.MatchString(tok):
		return types.SymbolTypeModel
	case symbolDocumentShape.MatchString(tok):
		return types.SymbolTypeDocumentNumber
	default:
		return types.SymbolTypeCode
	}
}
