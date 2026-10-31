package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Glossary audit actions (V3.0 §4.4: 术语治理是内容治理的一等事件). The
// deprecation-flavoured delete reuses term_updated with a structured payload
// so the action vocabulary stays created/updated/merged.
const (
	auditActionGlossaryTermCreated = "glossary.term_created"
	auditActionGlossaryTermUpdated = "glossary.term_updated"
	auditActionGlossaryTermMerged  = "glossary.term_merged"
)

// maxGlossaryStandardNameLen mirrors the migration's varchar(128).
const maxGlossaryStandardNameLen = 128

// maxGlossaryExpandTokens bounds one ExpandSynonyms call so a pathological
// query cannot blow up the OR-match (32 tokens ≈ 64 CJK chars / 32 words).
const maxGlossaryExpandTokens = 32

// glossaryService implements interfaces.GlossaryService.
type glossaryService struct {
	repo  interfaces.GlossaryRepository
	audit interfaces.AuditLogService
	now   func() time.Time
}

// NewGlossaryService constructs the glossary service.
func NewGlossaryService(
	repo interfaces.GlossaryRepository,
	audit interfaces.AuditLogService,
) interfaces.GlossaryService {
	return &glossaryService{repo: repo, audit: audit, now: time.Now}
}

// aliasesJSON marshals the alias list into a JSONB payload, falling back to
// an empty array on failure so a logging-shaped marshal error never blocks a
// term write. Kept local to this vertical (and deliberately not named like
// the lifecycle/feedback helpers) so the verticals stay independently
// maintainable.
func aliasesJSON(aliases []string) types.JSON {
	data, err := json.Marshal(aliases)
	if err != nil {
		return types.JSON([]byte("[]"))
	}
	return types.JSON(data)
}

// glossaryDetailsJSON marshals an audit detail payload, falling back to an
// empty object on failure so logging never blocks a term write. Local to this
// vertical and deliberately not named like the lifecycle/feedback helpers
// (same keep-verticals-independent rationale).
func glossaryDetailsJSON(v interface{}) types.JSON {
	data, err := json.Marshal(v)
	if err != nil {
		return types.JSON([]byte("{}"))
	}
	return types.JSON(data)
}

// CreateTerm creates one active term. New terms must start active: deprecated
// is only ever reached through delete/merge (状态机单向).
func (s *glossaryService) CreateTerm(ctx context.Context, term *types.GlossaryTerm) (*types.GlossaryTerm, error) {
	if term == nil {
		return nil, apperrors.NewBadRequestError("term body is required")
	}
	term.StandardName = strings.TrimSpace(term.StandardName)
	if term.StandardName == "" {
		return nil, apperrors.NewBadRequestError("standard_name is required")
	}
	if utf8.RuneCountInString(term.StandardName) > maxGlossaryStandardNameLen {
		return nil, apperrors.NewBadRequestError("standard_name is too long (max 128 chars)")
	}
	if term.Status == "" {
		term.Status = string(types.GlossaryTermStatusActive)
	}
	if types.GlossaryTermStatus(term.Status) != types.GlossaryTermStatusActive {
		return nil, apperrors.NewBadRequestError("a new term must start as active")
	}
	if len(term.Aliases) == 0 {
		// 归一空别名列表为 '[]'：NOT NULL 的 jsonb 列始终持有数组。
		term.Aliases = types.JSON([]byte("[]"))
	}
	if term.ID == "" {
		term.ID = uuid.NewString()
	}
	if existing, err := s.repo.GetTermByName(ctx, term.TenantID, term.StandardName); err == nil && existing != nil {
		return nil, apperrors.NewBadRequestError("standard_name already exists in this workspace")
	} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if err := s.repo.CreateTerm(ctx, term); err != nil {
		return nil, err
	}
	s.logAudit(ctx, term.TenantID, term.CreatedBy, auditActionGlossaryTermCreated,
		term.ID, map[string]interface{}{
			"standard_name": term.StandardName,
			"domain":        term.Domain,
		})
	return term, nil
}

// UpdateTerm patches the mutable fields of one term. Renames re-check the
// tenant-unique standard name (excluding self).
func (s *glossaryService) UpdateTerm(
	ctx context.Context, tenantID uint64, termID string,
	input *types.GlossaryTermUpdate, actorID string,
) (*types.GlossaryTerm, error) {
	if input == nil {
		return nil, apperrors.NewBadRequestError("update body is required")
	}
	term, err := s.loadTerm(ctx, tenantID, termID)
	if err != nil {
		return nil, err
	}
	if input.StandardName != nil {
		name := strings.TrimSpace(*input.StandardName)
		if name == "" {
			return nil, apperrors.NewBadRequestError("standard_name cannot be empty")
		}
		if utf8.RuneCountInString(name) > maxGlossaryStandardNameLen {
			return nil, apperrors.NewBadRequestError("standard_name is too long (max 128 chars)")
		}
		if name != term.StandardName {
			if existing, err := s.repo.GetTermByName(ctx, tenantID, name); err == nil && existing != nil && existing.ID != termID {
				return nil, apperrors.NewBadRequestError("standard_name already exists in this workspace")
			} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, err
			}
		}
		term.StandardName = name
	}
	if input.Aliases != nil {
		term.Aliases = aliasesJSON(*input.Aliases)
	}
	if input.Definition != nil {
		term.Definition = *input.Definition
	}
	if input.CounterExamples != nil {
		term.CounterExamples = *input.CounterExamples
	}
	if input.Domain != nil {
		term.Domain = strings.TrimSpace(*input.Domain)
	}
	if input.OwnerID != nil {
		term.OwnerID = strings.TrimSpace(*input.OwnerID)
	}
	if err := s.repo.UpdateTerm(ctx, term); err != nil {
		return nil, err
	}
	s.logAudit(ctx, tenantID, actorID, auditActionGlossaryTermUpdated,
		termID, map[string]interface{}{"standard_name": term.StandardName})
	return term, nil
}

// DeleteTerm deprecates one term without a physical delete: status→deprecated
// and superseded_by_id→redirect, so historical answers can re-point at the
// replacing term (deprecated 保留映射). Passing no redirect target is allowed.
func (s *glossaryService) DeleteTerm(ctx context.Context, tenantID uint64, termID, supersededByID, actorID string) error {
	if supersededByID == termID {
		return apperrors.NewBadRequestError("a term cannot supersede itself")
	}
	term, err := s.loadTerm(ctx, tenantID, termID)
	if err != nil {
		return err
	}
	if supersededByID != "" {
		if _, err := s.loadTerm(ctx, tenantID, supersededByID); err != nil {
			return apperrors.NewBadRequestError("superseded target not found in this workspace")
		}
	}
	if err := s.repo.DeprecateTerm(ctx, tenantID, termID, supersededByID); err != nil {
		return err
	}
	// 审计词表只有 created/updated/merged：删除是 deprecate，落 term_updated。
	s.logAudit(ctx, tenantID, actorID, auditActionGlossaryTermUpdated,
		termID, map[string]interface{}{
			"op":               "deprecate",
			"standard_name":    term.StandardName,
			"superseded_by_id": supersededByID,
		})
	return nil
}

// MergeTerms deprecates src with superseded_by_id=dst (合并=旧术语保留映射
// 指向新术语). dst must exist; re-merging an already-deprecated src just
// re-points the redirect.
func (s *glossaryService) MergeTerms(ctx context.Context, tenantID uint64, srcID, dstID, actorID string) (*types.GlossaryTerm, error) {
	if srcID == "" || dstID == "" {
		return nil, apperrors.NewBadRequestError("source_id and target_id are required")
	}
	if srcID == dstID {
		return nil, apperrors.NewBadRequestError("cannot merge a term into itself")
	}
	src, err := s.loadTerm(ctx, tenantID, srcID)
	if err != nil {
		return nil, err
	}
	dst, err := s.loadTerm(ctx, tenantID, dstID)
	if err != nil {
		return nil, err
	}
	if err := s.repo.DeprecateTerm(ctx, tenantID, srcID, dstID); err != nil {
		return nil, err
	}
	s.logAudit(ctx, tenantID, actorID, auditActionGlossaryTermMerged,
		srcID, map[string]interface{}{
			"target_id":       dstID,
			"source_name":     src.StandardName,
			"target_name":     dst.StandardName,
			"previous_status": src.Status,
		})
	return s.loadTerm(ctx, tenantID, dstID)
}

// SearchTerms fuzzy-searches terms; deprecated rows are included so the
// caller can present the redirect (status field is on every row).
func (s *glossaryService) SearchTerms(
	ctx context.Context, tenantID uint64, keyword, domain string,
) ([]*types.GlossaryTerm, error) {
	return s.repo.SearchTerms(ctx, tenantID, keyword, domain)
}

// ListByDomain lists the terms of one domain (empty domain = untagged).
func (s *glossaryService) ListByDomain(ctx context.Context, tenantID uint64, domain string) ([]*types.GlossaryTerm, error) {
	return s.repo.ListByDomain(ctx, tenantID, domain)
}

// GetTerm resolves one term by ID within the tenant.
func (s *glossaryService) GetTerm(ctx context.Context, tenantID uint64, termID string) (*types.GlossaryTerm, error) {
	return s.loadTerm(ctx, tenantID, termID)
}

// ExpandSynonyms tokenizes queryText, matches active glossary terms and
// returns the union of their aliases for retrieval expansion.
//
// 分词是刻意简化的（v1，注释说明）：ASCII 词按非字母数字切段（≥2 字符才算
// 词，避免单字母噪声）；中文等非 ASCII 连续段做 2-gram（"知识图谱" →
// "知识"/"识图"/"图谱"），单字残段也保留。精确分词需要 jieba/segment 之类的
// 中文分词器，等检索侧接入时再统一（本垂直不改检索代码）。命中术语只取
// active 的（deprecated 不参与扩展，其映射由上层重定向）。
func (s *glossaryService) ExpandSynonyms(ctx context.Context, tenantID uint64, queryText string) ([]string, error) {
	tokens := tokenizeForGlossaryExpansion(queryText)
	if len(tokens) == 0 {
		return nil, nil
	}
	if len(tokens) > maxGlossaryExpandTokens {
		tokens = tokens[:maxGlossaryExpandTokens]
	}
	terms, err := s.repo.MatchTerms(ctx, tenantID, tokens)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	synonyms := make([]string, 0, len(terms)*2)
	for _, term := range terms {
		var aliases []string
		if len(term.Aliases) > 0 {
			if err := json.Unmarshal(term.Aliases, &aliases); err != nil {
				// 单行别名损坏只降级该行，不让整个扩展失败。
				logger.Warnf(ctx, "glossary term %s has malformed aliases: %v", term.ID, err)
				continue
			}
		}
		for _, alias := range aliases {
			alias = strings.TrimSpace(alias)
			if alias == "" {
				continue
			}
			if _, dup := seen[alias]; dup {
				continue
			}
			seen[alias] = struct{}{}
			synonyms = append(synonyms, alias)
		}
	}
	return synonyms, nil
}

// tokenizeForGlossaryExpansion is the simplified tokenizer described on
// ExpandSynonyms: ASCII words (≥2 runes) split on non-letter/digit runs, and
// CJK runs expanded to 2-grams (lone single chars kept for recall).
func tokenizeForGlossaryExpansion(queryText string) []string {
	var tokens []string
	var ascii []rune
	var cjk []rune

	flushASCII := func() {
		if len(ascii) >= 2 {
			tokens = append(tokens, string(ascii))
		}
		ascii = ascii[:0]
	}
	flushCJK := func() {
		switch len(cjk) {
		case 0:
		case 1:
			tokens = append(tokens, string(cjk))
		default:
			for i := 0; i+1 < len(cjk); i++ {
				tokens = append(tokens, string(cjk[i:i+2]))
			}
		}
		cjk = cjk[:0]
	}
	flushAll := func() { flushASCII(); flushCJK() }

	for _, r := range queryText {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flushAll()
			continue
		}
		if r < 128 {
			flushCJK()
			ascii = append(ascii, r)
		} else {
			flushASCII()
			cjk = append(cjk, r)
		}
	}
	flushAll()
	return tokens
}

// loadTerm resolves a tenant-scoped term, translating record-not-found into
// the domain error the handler layer maps to 404.
func (s *glossaryService) loadTerm(ctx context.Context, tenantID uint64, termID string) (*types.GlossaryTerm, error) {
	term, err := s.repo.GetTerm(ctx, tenantID, termID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperrors.NewNotFoundError("glossary term not found")
		}
		return nil, err
	}
	return term, nil
}

// logAudit writes one glossary audit event; audit failures never block the
// business write (same policy as the other verticals).
func (s *glossaryService) logAudit(
	ctx context.Context, tenantID uint64, actorID, action, termID string,
	details map[string]interface{},
) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Log(ctx, &types.AuditLog{
		TenantID:    tenantID,
		ActorUserID: actorID,
		Action:      types.AuditAction(action),
		ScopeType:   "glossary",
		TargetType:  "glossary_term",
		TargetID:    termID,
		Outcome:     "success",
		Details:     glossaryDetailsJSON(details),
	})
}
