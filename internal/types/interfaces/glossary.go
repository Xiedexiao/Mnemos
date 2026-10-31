package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// GlossaryRepository persists glossary terms (KnowAxis V3.0 §4.4) and answers
// the fuzzy-match queries behind term search and synonym expansion.
type GlossaryRepository interface {
	// CreateTerm inserts one term (ID assigned by the model hook).
	CreateTerm(ctx context.Context, term *types.GlossaryTerm) error
	// UpdateTerm persists the full mutable state of a loaded term.
	UpdateTerm(ctx context.Context, term *types.GlossaryTerm) error
	// DeprecateTerm flips one term to deprecated with a redirect target in a
	// single UPDATE (used by both delete and merge). Returns
	// gorm.ErrRecordNotFound when the term is gone.
	DeprecateTerm(ctx context.Context, tenantID uint64, termID, supersededByID string) error
	// GetTerm resolves one term by ID within the tenant.
	GetTerm(ctx context.Context, tenantID uint64, termID string) (*types.GlossaryTerm, error)
	// GetTermByName resolves one term by its tenant-unique standard name
	// (duplicate-name guard and rename checks).
	GetTermByName(ctx context.Context, tenantID uint64, standardName string) (*types.GlossaryTerm, error)
	// SearchTerms fuzzy-matches keyword against standard_name, aliases (JSONB
	// cast to text) and definition; keyword/domain are optional filters.
	SearchTerms(ctx context.Context, tenantID uint64, keyword, domain string) ([]*types.GlossaryTerm, error)
	// ListByDomain returns the terms of one domain (empty domain = untagged).
	ListByDomain(ctx context.Context, tenantID uint64, domain string) ([]*types.GlossaryTerm, error)
	// MatchTerms returns the ACTIVE terms whose standard_name or aliases
	// contain any of the tokens (OR semantics; the caller dedupes).
	MatchTerms(ctx context.Context, tenantID uint64, tokens []string) ([]*types.GlossaryTerm, error)
}

// GlossaryService manages the controlled vocabulary and exposes the synonym
// expansion hook for retrieval (wiring the retrieval side is a later phase;
// this vertical only ships the capability).
type GlossaryService interface {
	// CreateTerm creates one active term (audit: glossary.term_created).
	CreateTerm(ctx context.Context, term *types.GlossaryTerm) (*types.GlossaryTerm, error)
	// UpdateTerm patches the mutable fields of one term
	// (audit: glossary.term_updated).
	UpdateTerm(
		ctx context.Context, tenantID uint64, termID string,
		input *types.GlossaryTermUpdate, actorID string,
	) (*types.GlossaryTerm, error)
	// DeleteTerm deprecates one term (no physical delete): status→deprecated,
	// superseded_by_id→redirect (audit: glossary.term_updated with a
	// deprecation payload — the action vocabulary is created/updated/merged).
	DeleteTerm(ctx context.Context, tenantID uint64, termID, supersededByID, actorID string) error
	// MergeTerms deprecates src with superseded_by_id=dst
	// (audit: glossary.term_merged).
	MergeTerms(ctx context.Context, tenantID uint64, srcID, dstID, actorID string) (*types.GlossaryTerm, error)
	// SearchTerms fuzzy-searches terms (keyword optional; domain optional).
	SearchTerms(ctx context.Context, tenantID uint64, keyword, domain string) ([]*types.GlossaryTerm, error)
	// ExpandSynonyms tokenizes queryText (simplified: ASCII words + CJK
	// 2-grams), matches active terms and returns the union of their aliases
	// for retrieval expansion.
	ExpandSynonyms(ctx context.Context, tenantID uint64, queryText string) ([]string, error)
	// ListByDomain lists the terms of one domain (empty domain = untagged).
	ListByDomain(ctx context.Context, tenantID uint64, domain string) ([]*types.GlossaryTerm, error)
	// GetTerm resolves one term by ID within the tenant.
	GetTerm(ctx context.Context, tenantID uint64, termID string) (*types.GlossaryTerm, error)
}
