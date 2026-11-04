package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
)

// queryRewriteTestDDL is the minimal schema the vocabulary queries need.
const queryRewriteTestDDL = `
CREATE TABLE knowledges (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL,
    title VARCHAR(255) NOT NULL DEFAULT '',
    lifecycle_status VARCHAR(32) NOT NULL DEFAULT 'valid',
    deleted_at DATETIME,
    updated_at DATETIME
);
CREATE TABLE glossary_terms (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    standard_name VARCHAR(128) NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'active'
);
`

func setupRewriteDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(
		sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(queryRewriteTestDDL).Error)
	return db
}

// TestVocabularyTermsKeepsSearchableLifecyclesOnly mirrors the retrieval gate.
func TestVocabularyTermsKeepsSearchableLifecyclesOnly(t *testing.T) {
	db := setupRewriteDB(t)
	insert := func(id, title, lifecycle string) {
		require.NoError(t, db.Exec(`
			INSERT INTO knowledges (id, tenant_id, knowledge_base_id, title,
				lifecycle_status, updated_at)
			VALUES (?, 7, 'kb-1', ?, ?, ?)`, id, title, lifecycle, time.Now().UTC()).Error)
	}
	insert("a", "报警处理", "valid")
	insert("b", "报警预案", "pending_review_cycle")
	insert("c", "报警草稿", "draft")
	insert("d", "报警旧版", "expired")
	insert("e", "备件清单", "valid")

	repo := NewQueryRewriteRepository(db)
	terms, err := repo.VocabularyTerms(context.Background(), 7, []string{"kb-1"}, 100)
	require.NoError(t, err)
	// pending_review_cycle 与 stale 仍可被检索（只是降权），所以它们的标题
	// 也可以作为纠错候选；草稿/过期/下架则彻底不出现。
	assert.ElementsMatch(t, []string{"报警处理", "报警预案", "备件清单"}, terms)
}

// TestVocabularyTermsTruncatesLongTitles keeps the candidate set to fragments.
func TestVocabularyTermsTruncatesLongTitles(t *testing.T) {
	db := setupRewriteDB(t)
	require.NoError(t, db.Exec(`
		INSERT INTO knowledges (id, tenant_id, knowledge_base_id, title, lifecycle_status, updated_at)
		VALUES ('a', 7, 'kb-1', '这是一个非常长的标题不该整段进入候选集', 'valid', ?)`,
		time.Now().UTC()).Error)
	repo := NewQueryRewriteRepository(db)
	terms, err := repo.VocabularyTerms(context.Background(), 7, []string{"kb-1"}, 100)
	require.NoError(t, err)
	require.Len(t, terms, 1)
	assert.Len(t, []rune(terms[0]), vocabularyFragmentRunes)
	assert.Equal(t, "这是一个非常长的标题不该", terms[0])
}

// TestVocabularyTermsStripsExtensions keeps "报警处理" out of "...报警处理.docx".
func TestVocabularyTermsStripsExtensions(t *testing.T) {
	db := setupRewriteDB(t)
	require.NoError(t, db.Exec(`
		INSERT INTO knowledges (id, tenant_id, knowledge_base_id, title, lifecycle_status, updated_at)
		VALUES ('a', 7, 'kb-1', 'E45 报警处理.docx', 'valid', ?),
		       ('b', 7, 'kb-1', '版本 2.0 说明', 'valid', ?)`,
		time.Now().UTC(), time.Now().UTC()).Error)
	repo := NewQueryRewriteRepository(db)
	terms, err := repo.VocabularyTerms(context.Background(), 7, []string{"kb-1"}, 100)
	require.NoError(t, err)
	assert.Contains(t, terms, "E45 报警处理")
	// "2.0" 不是扩展名（空格后还有内容），不能被截掉。
	assert.Contains(t, terms, "版本 2.0 说明")
}

// TestVocabularyTermsIncludesActiveGlossaryOnly covers the term half.
func TestVocabularyTermsIncludesActiveGlossaryOnly(t *testing.T) {
	db := setupRewriteDB(t)
	require.NoError(t, db.Exec(`
		INSERT INTO glossary_terms (id, tenant_id, standard_name, status)
		VALUES ('t1', 7, '报警', 'active'), ('t2', 7, '作废术语', 'deprecated')`).Error)
	repo := NewQueryRewriteRepository(db)
	terms, err := repo.VocabularyTerms(context.Background(), 7, nil, 100)
	require.NoError(t, err)
	assert.Equal(t, []string{"报警"}, terms)
}

// TestVocabularyTermsSurvivesMissingGlossaryTable keeps title candidates when
// the glossary query fails.
func TestVocabularyTermsSurvivesMissingGlossaryTable(t *testing.T) {
	db := setupRewriteDB(t)
	require.NoError(t, db.Exec(`DROP TABLE glossary_terms`).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO knowledges (id, tenant_id, knowledge_base_id, title, lifecycle_status, updated_at)
		VALUES ('a', 7, 'kb-1', '报警处理', 'valid', ?)`, time.Now().UTC()).Error)

	repo := NewQueryRewriteRepository(db)
	terms, err := repo.VocabularyTerms(context.Background(), 7, []string{"kb-1"}, 100)
	require.NoError(t, err)
	assert.Equal(t, []string{"报警处理"}, terms)
}

// TestVocabularyTermsWithoutBasesReturnsGlossaryOnly documents the scoping.
func TestVocabularyTermsWithoutBasesReturnsGlossaryOnly(t *testing.T) {
	db := setupRewriteDB(t)
	require.NoError(t, db.Exec(`
		INSERT INTO knowledges (id, tenant_id, knowledge_base_id, title, lifecycle_status, updated_at)
		VALUES ('a', 7, 'kb-1', '报警处理', 'valid', ?)`, time.Now().UTC()).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO glossary_terms (id, tenant_id, standard_name, status)
		VALUES ('t1', 7, '报警', 'active')`).Error)

	repo := NewQueryRewriteRepository(db)
	terms, err := repo.VocabularyTerms(context.Background(), 7, nil, 100)
	require.NoError(t, err)
	assert.Equal(t, []string{"报警"}, terms, "no bases means no title candidates")
}

// TestTitleFragmentResolvesOneDocument is the denial-subtraction input.
func TestTitleFragmentResolvesOneDocument(t *testing.T) {
	db := setupRewriteDB(t)
	require.NoError(t, db.Exec(`
		INSERT INTO knowledges (id, tenant_id, knowledge_base_id, title, lifecycle_status, updated_at)
		VALUES ('doc', 7, 'kb-1', 'E45 报警处理.docx', 'valid', ?)`, time.Now().UTC()).Error)
	repo := NewQueryRewriteRepository(db)

	fragment, err := repo.TitleFragment(context.Background(), 7, "doc")
	require.NoError(t, err)
	assert.Equal(t, "E45 报警处理", fragment)

	// Unknown id yields an empty fragment, not an error.
	missing, err := repo.TitleFragment(context.Background(), 7, "nope")
	require.NoError(t, err)
	assert.Equal(t, "", missing)

	// Cross-tenant lookup finds nothing.
	crossTenant, err := repo.TitleFragment(context.Background(), 8, "doc")
	require.NoError(t, err)
	assert.Equal(t, "", crossTenant)
}

// TestTitleFragmentDropsEmptyAndUnnamed covers the degenerate rows.
func TestTitleFragmentDropsEmptyAndUnnamed(t *testing.T) {
	db := setupRewriteDB(t)
	require.NoError(t, db.Exec(`
		INSERT INTO knowledges (id, tenant_id, knowledge_base_id, title, lifecycle_status, updated_at)
		VALUES ('empty', 7, 'kb-1', '   ', 'valid', ?)`, time.Now().UTC()).Error)
	repo := NewQueryRewriteRepository(db)
	fragment, err := repo.TitleFragment(context.Background(), 7, "empty")
	require.NoError(t, err)
	assert.Equal(t, "", fragment)
}

// TestGlossaryTermsScopesToTenant keeps two tenants apart.
func TestGlossaryTermsScopesToTenant(t *testing.T) {
	db := setupRewriteDB(t)
	require.NoError(t, db.Exec(`
		INSERT INTO glossary_terms (id, tenant_id, standard_name, status)
		VALUES ('t1', 7, '报警', 'active'), ('t2', 8, '别家术语', 'active')`).Error)
	repo := NewQueryRewriteRepository(db)
	terms, err := repo.GlossaryTerms(context.Background(), 7, 100)
	require.NoError(t, err)
	assert.Equal(t, []string{"报警"}, terms)
}

// TestTitleFragmentKeepsShortDotSuffix is the guard against stripping real text:
// "版本 2.0 说明" must keep its "2.0".
func TestTitleFragmentKeepsShortDotSuffix(t *testing.T) {
	assert.Equal(t, "版本 2.0 说明", titleFragment("版本 2.0 说明"))
	assert.Equal(t, "E45 报警处理", titleFragment("E45 报警处理.docx"))
	assert.Equal(t, "报告", titleFragment("报告.pdf"))
	assert.Equal(t, "", titleFragment("   "))
}

var _ = types.NormalizeSearchQuery
