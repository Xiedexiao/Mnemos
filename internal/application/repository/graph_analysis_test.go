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

// graphAnalysisTestDDL is the minimal schema the analysis queries need.
const graphAnalysisTestDDL = `
CREATE TABLE knowledges (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL,
    title VARCHAR(255) NOT NULL DEFAULT '',
    lifecycle_status VARCHAR(32) NOT NULL DEFAULT 'valid',
    deleted_at DATETIME
);
CREATE TABLE knowledge_relations (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    from_knowledge_id VARCHAR(36) NOT NULL,
    to_knowledge_id VARCHAR(36) NOT NULL,
    relation_type VARCHAR(24) NOT NULL DEFAULT 'related',
    pair_key VARCHAR(120) NOT NULL,
    direction VARCHAR(8) NOT NULL DEFAULT 'forward',
    origin VARCHAR(16) NOT NULL DEFAULT 'manual',
    created_at DATETIME
);
CREATE TABLE knowledge_versions (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    knowledge_id VARCHAR(36) NOT NULL,
    version INT NOT NULL,
    published_by VARCHAR(36) NOT NULL DEFAULT ''
);
CREATE TABLE knowledge_candidates (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    knowledge_id VARCHAR(36) NOT NULL DEFAULT '',
    requested_by VARCHAR(36) NOT NULL
);
CREATE TABLE community_questions (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    knowledge_id VARCHAR(36) NOT NULL DEFAULT ''
);
CREATE TABLE community_answers (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    question_id VARCHAR(36) NOT NULL,
    answered_by VARCHAR(36) NOT NULL,
    votes INTEGER NOT NULL DEFAULT 0,
    accepted BOOLEAN NOT NULL DEFAULT FALSE,
    created_at DATETIME
);
CREATE TABLE evidence_packs (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    refusal VARCHAR(32) NOT NULL DEFAULT '',
    citations TEXT NOT NULL DEFAULT '[]',
    created_at DATETIME
);
`

func setupGraphAnalysisDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(
		sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(graphAnalysisTestDDL).Error)
	return db
}

// TestAnalysisNodesExcludesUnsearchableLifecycles covers the lifecycle gate:
// drafts, expired and delisted documents never enter the graph, because they
// cannot be read through search either.
func TestAnalysisNodesExcludesUnsearchableLifecycles(t *testing.T) {
	db := setupGraphAnalysisDB(t)
	insert := func(id, lifecycle string, deleted bool) {
		var deletedAt interface{}
		if deleted {
			deletedAt = time.Now().UTC()
		}
		require.NoError(t, db.Exec(`
			INSERT INTO knowledges (id, tenant_id, knowledge_base_id, title,
				lifecycle_status, deleted_at)
			VALUES (?, 7, 'kb-1', ?, ?, ?)`, id, id, lifecycle, deletedAt).Error)
	}
	insert("valid", "valid", false)
	insert("review", "pending_review_cycle", false)
	insert("stale", "stale", false)
	insert("draft", "draft", false)
	insert("expired", "expired", false)
	insert("delisted", "delisted", false)
	insert("deleted", "valid", true)

	repo := NewGraphAnalysisRepository(db)
	nodes, err := repo.AnalysisNodes(context.Background(), 7, "kb-1", 100)
	require.NoError(t, err)
	ids := make([]string, 0, len(nodes))
	for _, node := range nodes {
		ids = append(ids, node.KnowledgeID)
	}
	assert.Equal(t, []string{"review", "stale", "valid"}, ids)
}

// TestAnalysisNodesScopesToSpace keeps two knowledge bases apart.
func TestAnalysisNodesScopesToSpace(t *testing.T) {
	db := setupGraphAnalysisDB(t)
	require.NoError(t, db.Exec(`
		INSERT INTO knowledges (id, tenant_id, knowledge_base_id, title, lifecycle_status)
		VALUES ('a', 7, 'kb-1', 'A', 'valid'),
		       ('b', 7, 'kb-2', 'B', 'valid'),
		       ('c', 8, 'kb-1', 'C', 'valid')`).Error)
	repo := NewGraphAnalysisRepository(db)
	nodes, err := repo.AnalysisNodes(context.Background(), 7, "kb-1", 100)
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	assert.Equal(t, "a", nodes[0].KnowledgeID)
}

// TestAnalysisEdgesRequiresBothEndsVisible is the §11.3 endpoint rule: an edge
// pointing outside the visible set must not be returned at all, because the
// edge alone reveals that the other document exists.
func TestAnalysisEdgesRequiresBothEndsVisible(t *testing.T) {
	db := setupGraphAnalysisDB(t)
	insertRelation := func(from, to string) {
		require.NoError(t, db.Exec(`
			INSERT INTO knowledge_relations (id, tenant_id, from_knowledge_id,
				to_knowledge_id, relation_type, pair_key, direction, origin)
			VALUES (?, 7, ?, ?, 'based_on', ?, 'forward', 'manual')`,
			from+"-"+to, from, to,
			types.KnowledgeRelationPairKey(from, to, "based_on")).Error)
	}
	insertRelation("a", "b")
	insertRelation("a", "hidden")

	repo := NewGraphAnalysisRepository(db)
	edges, err := repo.AnalysisEdges(context.Background(), 7, []string{"a", "b"})
	require.NoError(t, err)
	require.Len(t, edges, 1)
	assert.Equal(t, "a", edges[0].From)
	assert.Equal(t, "b", edges[0].To)
}

// TestAnalysisEdgesDedupesAcrossBatches covers the batching: an edge whose ends
// fall in different IN lists is returned by both queries and must appear once,
// otherwise community InternalEdges would double.
func TestAnalysisEdgesDedupesAcrossBatches(t *testing.T) {
	db := setupGraphAnalysisDB(t)
	// Two documents far apart in the sorted id list, so with a batch of 400
	// they cannot both fit in one batch's from-side list.
	low := "a-000"
	high := "z-999"
	require.NoError(t, db.Exec(`
		INSERT INTO knowledge_relations (id, tenant_id, from_knowledge_id,
			to_knowledge_id, relation_type, pair_key, direction, origin)
		VALUES ('edge', 7, ?, ?, 'related', 'pair', 'forward', 'manual')`,
		low, high).Error)

	ids := []string{low}
	for i := 0; i < analysisEdgeBatch; i++ {
		ids = append(ids, "m-"+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	ids = append(ids, high)

	repo := NewGraphAnalysisRepository(db)
	edges, err := repo.AnalysisEdges(context.Background(), 7, ids)
	require.NoError(t, err)
	require.Len(t, edges, 1, "the cross-batch edge must be returned exactly once")
	assert.Equal(t, low, edges[0].From)
	assert.Equal(t, high, edges[0].To)
}

// TestExpertAuthorshipPrefersCandidateSubmitter covers the precedence rule: a
// document published from an experience card is credited to the submitter, not
// to whoever happened to click publish.
func TestExpertAuthorshipPrefersCandidateSubmitter(t *testing.T) {
	db := setupGraphAnalysisDB(t)
	require.NoError(t, db.Exec(`
		INSERT INTO knowledge_versions (id, tenant_id, knowledge_id, version, published_by)
		VALUES ('v1', 7, 'doc', 1, 'publisher'),
		       ('v2', 7, 'doc', 2, 'editor'),
		       ('v3', 7, 'upload', 1, 'uploader')`).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO knowledge_candidates (id, tenant_id, knowledge_id, requested_by)
		VALUES ('c1', 7, 'doc', 'submitter')`).Error)

	repo := NewGraphAnalysisRepository(db)
	got, err := repo.ExpertAuthorship(context.Background(), 7, []string{"doc", "upload"})
	require.NoError(t, err)
	assert.Equal(t, "submitter", got["doc"], "the card submitter wins over the publisher")
	assert.Equal(t, "uploader", got["upload"], "documents without a card fall back to v1")
}

// TestExpertAuthorshipUsesEarliestVersion pins "first published version": the
// person who created the content, not the last editor.
func TestExpertAuthorshipUsesEarliestVersion(t *testing.T) {
	db := setupGraphAnalysisDB(t)
	require.NoError(t, db.Exec(`
		INSERT INTO knowledge_versions (id, tenant_id, knowledge_id, version, published_by)
		VALUES ('v2', 7, 'doc', 2, 'late-editor'),
		       ('v1', 7, 'doc', 1, 'original-author')`).Error)
	repo := NewGraphAnalysisRepository(db)
	got, err := repo.ExpertAuthorship(context.Background(), 7, []string{"doc"})
	require.NoError(t, err)
	assert.Equal(t, "original-author", got["doc"])
}

// TestExpertAnswerCountersScopesToQuestionsAboutTheDocuments is the relevance
// rule: someone answering HR questions should not rank as a equipment expert.
func TestExpertAnswerCountersScopesToQuestionsAboutTheDocuments(t *testing.T) {
	db := setupGraphAnalysisDB(t)
	now := time.Now().UTC()
	require.NoError(t, db.Exec(`
		INSERT INTO community_questions (id, tenant_id, knowledge_id)
		VALUES ('q-in', 7, 'doc'), ('q-out', 7, 'other')`).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO community_answers (id, tenant_id, question_id, answered_by, votes, accepted, created_at)
		VALUES ('a1', 7, 'q-in', 'ana', 4, TRUE, ?),
		       ('a2', 7, 'q-in', 'ana', 2, FALSE, ?),
		       ('a3', 7, 'q-out', 'ana', 50, TRUE, ?)`,
		now, now, now).Error)

	repo := NewGraphAnalysisRepository(db)
	counters, err := repo.ExpertAnswerCounters(
		context.Background(), 7, []string{"doc"}, now.AddDate(0, 0, -1))
	require.NoError(t, err)
	require.Len(t, counters, 1)
	assert.Equal(t, "ana", counters[0].UserID)
	assert.Equal(t, 1, counters[0].AcceptedAnswers, "the off-topic answer is excluded")
	assert.Equal(t, 6, counters[0].UsefulRatings)
}

// TestExpertCitationCountsDedupesWithinOneAnswer keeps a long answer that cites
// the same document ten times from inflating the citation count.
func TestExpertCitationCountsDedupesWithinOneAnswer(t *testing.T) {
	db := setupGraphAnalysisDB(t)
	now := time.Now().UTC()
	require.NoError(t, db.Exec(`
		INSERT INTO evidence_packs (id, tenant_id, refusal, citations, created_at)
		VALUES ('p1', 7, '', ?, ?),
		       ('p2', 7, '', ?, ?),
		       ('p3', 7, 'refused', '[]', ?),
		       ('p4', 7, '', 'not json', ?)`,
		`[{"knowledge_id":"doc"},{"knowledge_id":"doc"},{"knowledge_id":"other"}]`,
		now,
		`[{"knowledge_id":"doc"}]`,
		now, now, now).Error)

	repo := NewGraphAnalysisRepository(db)
	counts, err := repo.ExpertCitationCounts(
		context.Background(), 7, []string{"doc"}, now.AddDate(0, 0, -1), 0)
	require.NoError(t, err)
	assert.Equal(t, 2, counts["doc"], "two answering turns cited it, not three citations")
}
