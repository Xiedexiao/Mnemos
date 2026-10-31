package repository

import (
	"context"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

// glossaryRepository implements interfaces.GlossaryRepository.
type glossaryRepository struct {
	db *gorm.DB
}

// NewGlossaryRepository constructs the glossary repository.
func NewGlossaryRepository(db *gorm.DB) interfaces.GlossaryRepository {
	return &glossaryRepository{db: db}
}

// CreateTerm inserts one term. The (tenant_id, standard_name) uniqueness is
// enforced by the migration's constraint; the service pre-checks and
// translates the duplicate into a 400.
func (r *glossaryRepository) CreateTerm(ctx context.Context, term *types.GlossaryTerm) error {
	return r.db.WithContext(ctx).Create(term).Error
}

// UpdateTerm persists the full mutable state of a loaded term (the service
// loads → mutates → saves, so audit-visible fields stay consistent).
func (r *glossaryRepository) UpdateTerm(ctx context.Context, term *types.GlossaryTerm) error {
	return r.db.WithContext(ctx).Save(term).Error
}

// DeprecateTerm performs the atomic active→deprecated transition with the
// redirect target (删除=置 deprecated+superseded_by，不物理删). Zero rows
// affected means the term is gone; surfaced as ErrRecordNotFound.
func (r *glossaryRepository) DeprecateTerm(ctx context.Context, tenantID uint64, termID, supersededByID string) error {
	result := r.db.WithContext(ctx).
		Model(&types.GlossaryTerm{}).
		Where("tenant_id = ? AND id = ?", tenantID, termID).
		Updates(map[string]interface{}{
			"status":           string(types.GlossaryTermStatusDeprecated),
			"superseded_by_id": supersededByID,
			"updated_at":       time.Now(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// GetTerm resolves one term by ID within the tenant.
func (r *glossaryRepository) GetTerm(ctx context.Context, tenantID uint64, termID string) (*types.GlossaryTerm, error) {
	var term types.GlossaryTerm
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, termID).
		First(&term).Error; err != nil {
		return nil, err
	}
	return &term, nil
}

// GetTermByName resolves one term by its tenant-unique standard name.
func (r *glossaryRepository) GetTermByName(ctx context.Context, tenantID uint64, standardName string) (*types.GlossaryTerm, error) {
	var term types.GlossaryTerm
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND standard_name = ?", tenantID, standardName).
		First(&term).Error; err != nil {
		return nil, err
	}
	return &term, nil
}

// SearchTerms fuzzy-matches keyword against standard_name/aliases/definition.
// A deprecated term still matches (deprecated 保留映射): the caller decides
// how to present the redirect. Keyword and domain are optional filters.
//
// ILIKE is Postgres-only; SQLite (Lite build) lacks it, so mirror the
// message/session searches with LOWER() on both sides. ESCAPE ? pairs with
// escapeLikeKeyword: SQLite has no default LIKE escape character, so without
// it \% and \_ would never match a literal wildcard.
func (r *glossaryRepository) SearchTerms(
	ctx context.Context, tenantID uint64, keyword, domain string,
) ([]*types.GlossaryTerm, error) {
	query := r.db.WithContext(ctx).
		Model(&types.GlossaryTerm{}).
		Where("tenant_id = ?", tenantID)
	if keyword = strings.TrimSpace(keyword); keyword != "" {
		like := "%" + escapeLikeKeyword(keyword) + "%"
		if r.db.Name() == "postgres" {
			query = query.Where(
				"standard_name ILIKE ? ESCAPE ? OR aliases::text ILIKE ? ESCAPE ? OR definition ILIKE ? ESCAPE ?",
				like, likeEscapeChar, like, likeEscapeChar, like, likeEscapeChar,
			)
		} else {
			query = query.Where(
				"LOWER(standard_name) LIKE LOWER(?) ESCAPE ? OR LOWER(aliases) LIKE LOWER(?) ESCAPE ? OR LOWER(definition) LIKE LOWER(?) ESCAPE ?",
				like, likeEscapeChar, like, likeEscapeChar, like, likeEscapeChar,
			)
		}
	}
	if domain != "" {
		query = query.Where("domain = ?", domain)
	}
	var terms []*types.GlossaryTerm
	err := query.Order("updated_at DESC").Find(&terms).Error
	return terms, err
}

// ListByDomain returns the terms of one domain (empty domain = untagged).
func (r *glossaryRepository) ListByDomain(ctx context.Context, tenantID uint64, domain string) ([]*types.GlossaryTerm, error) {
	var terms []*types.GlossaryTerm
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND domain = ?", tenantID, domain).
		Order("standard_name ASC").
		Find(&terms).Error
	return terms, err
}

// MatchTerms returns the ACTIVE terms whose standard_name or aliases contain
// any of the tokens (OR semantics across tokens, AND-free — recall-first for
// synonym expansion). One term can match several tokens and appear more than
// once; the service dedupes by alias value.
func (r *glossaryRepository) MatchTerms(ctx context.Context, tenantID uint64, tokens []string) ([]*types.GlossaryTerm, error) {
	if len(tokens) == 0 {
		return nil, nil
	}
	conds := make([]string, 0, len(tokens))
	args := make([]interface{}, 0, len(tokens)*4)
	isPostgres := r.db.Name() == "postgres"
	for _, token := range tokens {
		like := "%" + escapeLikeKeyword(token) + "%"
		if isPostgres {
			conds = append(conds, "(standard_name ILIKE ? ESCAPE ? OR aliases::text ILIKE ? ESCAPE ?)")
		} else {
			conds = append(conds, "(LOWER(standard_name) LIKE LOWER(?) ESCAPE ? OR LOWER(aliases) LIKE LOWER(?) ESCAPE ?)")
		}
		args = append(args, like, likeEscapeChar, like, likeEscapeChar)
	}
	var terms []*types.GlossaryTerm
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND status = ?", tenantID, string(types.GlossaryTermStatusActive)).
		Where("("+strings.Join(conds, " OR ")+")", args...).
		Find(&terms).Error
	return terms, err
}
