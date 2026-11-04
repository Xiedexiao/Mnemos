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

// knowledgeMapTestDDL is the minimal schema the map queries need.
const knowledgeMapTestDDL = `
CREATE TABLE knowledges (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL,
    title VARCHAR(255) NOT NULL DEFAULT '',
    folder_path VARCHAR(1024) NOT NULL DEFAULT '',
    lifecycle_status VARCHAR(32) NOT NULL DEFAULT 'valid',
    classification VARCHAR(16) NOT NULL DEFAULT 'internal',
    deleted_at DATETIME,
    updated_at DATETIME
);
CREATE TABLE knowledge_tags (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL DEFAULT '',
    name VARCHAR(128) NOT NULL
);
CREATE TABLE knowledge_tag_relations (
    knowledge_id VARCHAR(36) NOT NULL,
    tag_id VARCHAR(36) NOT NULL,
    PRIMARY KEY (knowledge_id, tag_id)
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
    note VARCHAR(512) NOT NULL DEFAULT '',
    created_by VARCHAR(36) NOT NULL DEFAULT '',
    created_at DATETIME
);
`

func setupKnowledgeMapDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(knowledgeMapTestDDL).Error)
	return db
}

func seedMapKnowledge(
	t *testing.T, db *gorm.DB, id, kbID, folder, lifecycle string, deleted bool,
) {
	t.Helper()
	var deletedAt interface{}
	if deleted {
		deletedAt = time.Now().UTC()
	}
	require.NoError(t, db.Exec(`
		INSERT INTO knowledges (id, tenant_id, knowledge_base_id, title, folder_path,
			lifecycle_status, classification, deleted_at, updated_at)
		VALUES (?, 7, ?, ?, ?, ?, 'internal', ?, ?)`,
		id, kbID, id, folder, lifecycle, deletedAt, time.Now().UTC()).Error)
}

func seedMapTag(t *testing.T, db *gorm.DB, knowledgeID, tagName string) {
	t.Helper()
	tagID := "tag-" + tagName
	require.NoError(t, db.Exec(`
		INSERT OR IGNORE INTO knowledge_tags (id, tenant_id, knowledge_base_id, name)
		VALUES (?, 7, 'kb-1', ?)`, tagID, tagName).Error)
	require.NoError(t, db.Exec(`
		INSERT OR IGNORE INTO knowledge_tag_relations (knowledge_id, tag_id)
		VALUES (?, ?)`, knowledgeID, tagID).Error)
}

func seedMapRelation(t *testing.T, db *gorm.DB, from, to, relationType, direction string) {
	t.Helper()
	require.NoError(t, db.Exec(`
		INSERT INTO knowledge_relations (id, tenant_id, from_knowledge_id, to_knowledge_id,
			relation_type, pair_key, direction, origin, created_at)
		VALUES (?, 7, ?, ?, ?, ?, ?, 'manual', ?)`,
		from+"-"+to+"-"+direction, from, to, relationType,
		types.KnowledgeRelationPairKey(from, to, relationType), direction,
		time.Now().UTC()).Error)
}

// TestMapNodesExcludesNonSearchableLifecycles covers the lifecycle gate.
func TestMapNodesExcludesNonSearchableLifecycles(t *testing.T) {
	db := setupKnowledgeMapDB(t)
	seedMapKnowledge(t, db, "valid", "kb-1", "sop", "valid", false)
	seedMapKnowledge(t, db, "stale", "kb-1", "sop", "stale", false)
	seedMapKnowledge(t, db, "review", "kb-1", "sop", "pending_review_cycle", false)
	seedMapKnowledge(t, db, "draft", "kb-1", "sop", "draft", false)
	seedMapKnowledge(t, db, "expired", "kb-1", "sop", "expired", false)
	seedMapKnowledge(t, db, "delisted", "kb-1", "sop", "delisted", false)
	seedMapKnowledge(t, db, "deleted", "kb-1", "sop", "valid", true)
	seedMapKnowledge(t, db, "other-space", "kb-2", "sop", "valid", false)
	seedMapTag(t, db, "valid", "工艺")

	repo := NewKnowledgeMapRepository(db)
	nodes, err := repo.MapNodes(context.Background(), 7, "kb-1", 100)
	require.NoError(t, err)

	ids := make([]string, 0, len(nodes))
	for _, node := range nodes {
		ids = append(ids, node.KnowledgeID)
	}
	assert.ElementsMatch(t, []string{"valid", "stale", "review"}, ids,
		"only searchable lifecycles appear on the map")
	for _, node := range nodes {
		if node.KnowledgeID == "valid" {
			assert.Equal(t, []string{"工艺"}, node.Tags,
				"tags are attached in one extra query")
		}
	}
}

// TestMapEdgesRequiresBothEndsInside keeps an edge from leaking a document
// that was filtered out of the node set.
func TestMapEdgesRequiresBothEndsInside(t *testing.T) {
	db := setupKnowledgeMapDB(t)
	seedMapKnowledge(t, db, "a", "kb-1", "sop", "valid", false)
	seedMapKnowledge(t, db, "b", "kb-1", "sop", "valid", false)
	seedMapKnowledge(t, db, "hidden", "kb-1", "sop", "valid", false)
	seedMapRelation(t, db, "a", "b", "based_on", "forward")
	seedMapRelation(t, db, "b", "a", "based_on", "reverse")
	seedMapRelation(t, db, "b", "hidden", "related", "forward")
	seedMapRelation(t, db, "hidden", "a", "related", "forward")

	repo := NewKnowledgeMapRepository(db)
	edges, err := repo.MapEdges(context.Background(), 7, []string{"a", "b"})
	require.NoError(t, err)
	require.Len(t, edges, 1, "only the edge with both ends visible survives")
	assert.Equal(t, "a", edges[0].From)
	assert.Equal(t, "b", edges[0].To)
	assert.Equal(t, "based_on", edges[0].RelationType)
}

// TestMapEdgesSkipsReverseMirrors keeps each relation from appearing twice.
func TestMapEdgesSkipsReverseMirrors(t *testing.T) {
	db := setupKnowledgeMapDB(t)
	seedMapKnowledge(t, db, "a", "kb-1", "sop", "valid", false)
	seedMapKnowledge(t, db, "b", "kb-1", "sop", "valid", false)
	seedMapRelation(t, db, "a", "b", "based_on", "forward")
	seedMapRelation(t, db, "b", "a", "based_on", "reverse")

	repo := NewKnowledgeMapRepository(db)
	edges, err := repo.MapEdges(context.Background(), 7, []string{"a", "b"})
	require.NoError(t, err)
	assert.Len(t, edges, 1, "the reverse mirror is the same edge stored twice")
}

// TestCountSpaceKnowledgeUsesTheSameVisibility keeps the truncation count
// from disagreeing with the node list.
func TestCountSpaceKnowledgeUsesTheSameVisibility(t *testing.T) {
	db := setupKnowledgeMapDB(t)
	seedMapKnowledge(t, db, "valid", "kb-1", "sop", "valid", false)
	seedMapKnowledge(t, db, "draft", "kb-1", "sop", "draft", false)
	seedMapKnowledge(t, db, "deleted", "kb-1", "sop", "valid", true)

	repo := NewKnowledgeMapRepository(db)
	total, err := repo.CountSpaceKnowledge(context.Background(), 7, "kb-1")
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
}

// TestRoleMapNodesMatchesTagsCaseInsensitively covers the role query.
func TestRoleMapNodesMatchesTagsCaseInsensitively(t *testing.T) {
	db := setupKnowledgeMapDB(t)
	seedMapKnowledge(t, db, "a", "kb-1", "sop", "valid", false)
	seedMapKnowledge(t, db, "b", "kb-1", "manual", "valid", false)
	seedMapKnowledge(t, db, "c", "kb-1", "sop", "valid", false)
	seedMapTag(t, db, "a", "EHS")
	seedMapTag(t, db, "b", "工艺")
	seedMapTag(t, db, "c", "ehs")

	repo := NewKnowledgeMapRepository(db)
	nodes, err := repo.RoleMapNodes(context.Background(), 7, []string{"ehs"}, 100)
	require.NoError(t, err)
	ids := make([]string, 0, len(nodes))
	for _, node := range nodes {
		ids = append(ids, node.KnowledgeID)
	}
	assert.ElementsMatch(t, []string{"a", "c"}, ids,
		"tag matching ignores case and returns each document once")
}
