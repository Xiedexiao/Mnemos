package repository

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// Temporary smoke test (deleted after run): validates upsert merge rules,
// commit apply/rollback, SQLite BFS path fallback and the extraction cache
// against the real gorm layer.
func TestGraphFactRepositorySmoke(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&types.GraphEntity{}, &types.GraphFact{}, &types.GraphCommit{}, &types.ChunkExtractionCache{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repo := NewGraphFactRepository(db)
	ctx := context.Background()
	const tenant = uint64(1)

	// --- entity create ---
	ents := []*types.GraphEntity{
		{EntityType: "Device", Name: "Router A", SchemaID: "s1", Confidence: types.GraphConfidenceExtracted, Attributes: types.JSON(`{"vendor":"cisco"}`)},
		{EntityType: "Device", Name: "Router B"},
	}
	added, updated, err := repo.UpsertEntities(ctx, tenant, ents)
	if err != nil || added != 2 || updated != 0 {
		t.Fatalf("create entities: added=%d updated=%d err=%v", added, updated, err)
	}
	idA, idB := ents[0].ID, ents[1].ID

	// --- entity merge: ambiguous must not downgrade; attributes key-merge ---
	ents2 := []*types.GraphEntity{{EntityType: "Device", Name: "router a", Confidence: types.GraphConfidenceAmbiguous, Attributes: types.JSON(`{"model":"xr"}`), CommitID: "c1"}}
	added, updated, err = repo.UpsertEntities(ctx, tenant, ents2)
	if err != nil || added != 0 || updated != 1 {
		t.Fatalf("merge entity: added=%d updated=%d err=%v", added, updated, err)
	}
	if ents2[0].ID != idA || ents2[0].Confidence != types.GraphConfidenceExtracted {
		t.Fatalf("identity/confidence: id=%s conf=%v", ents2[0].ID, ents2[0].Confidence)
	}
	if string(ents2[0].Attributes) != `{"model":"xr","vendor":"cisco"}` {
		t.Fatalf("attributes: %s", ents2[0].Attributes)
	}
	if ents2[0].CommitID != "c1" {
		t.Fatalf("commit backfill: %s", ents2[0].CommitID)
	}

	// --- fact create + duplicate merge ---
	facts := []*types.GraphFact{{RelationName: "depends_on", SourceEntityID: idA, TargetEntityID: idB, Evidence: types.JSON(`[{"knowledge_id":"k1","chunk_id":"c1","quote":"q1"}]`), CommitID: "c1"}}
	addedF, err := repo.UpsertFacts(ctx, tenant, facts)
	if err != nil || addedF != 1 {
		t.Fatalf("create fact: added=%d err=%v", addedF, err)
	}
	factID := facts[0].ID
	dup := []*types.GraphFact{{RelationName: "depends_on", SourceEntityID: idA, TargetEntityID: idB, Evidence: types.JSON(`[{"knowledge_id":"k2","chunk_id":"c2","quote":"q2"}]`), CommitID: "c2"}}
	addedF, err = repo.UpsertFacts(ctx, tenant, dup)
	if err != nil || addedF != 0 || dup[0].ID != factID {
		t.Fatalf("dedupe fact: added=%d id=%s/%s err=%v", addedF, dup[0].ID, factID, err)
	}
	var evidence []types.GraphFactEvidence
	if err := jsonUnmarshalForTest(dup[0].Evidence, &evidence); err != nil || len(evidence) != 2 {
		t.Fatalf("evidence merge: %s err=%v", dup[0].Evidence, err)
	}
	if dup[0].CommitID != "c2" {
		t.Fatalf("fact commit backfill: %s", dup[0].CommitID)
	}

	// --- chain for path query: A -depends_on-> B -connects_to-> C ---
	entC := &types.GraphEntity{EntityType: "Device", Name: "Router C"}
	if _, _, err := repo.UpsertEntities(ctx, tenant, []*types.GraphEntity{entC}); err != nil {
		t.Fatalf("entity C: %v", err)
	}
	facts2 := []*types.GraphFact{{RelationName: "connects_to", SourceEntityID: idB, TargetEntityID: entC.ID}}
	if _, err := repo.UpsertFacts(ctx, tenant, facts2); err != nil {
		t.Fatalf("fact B->C: %v", err)
	}
	paths, err := repo.FindFactIDPaths(ctx, tenant, idA, entC.ID, 3, 10)
	if err != nil {
		t.Fatalf("path query: %v", err)
	}
	if len(paths) != 1 || len(paths[0]) != 2 || paths[0][0] != factID || paths[0][1] != facts2[0].ID {
		t.Fatalf("path result: %v", paths)
	}
	if self, err := repo.FindFactIDPaths(ctx, tenant, idA, idA, 3, 10); err != nil || len(self) != 0 {
		t.Fatalf("self path: %v err=%v", self, err)
	}

	// --- apply commit + rollback ---
	commit := &types.GraphCommit{ID: uuid.NewString(), TenantID: tenant, CreatedBy: "u1", Description: "batch"}
	newEnt := &types.GraphEntity{EntityType: "SOP", Name: "SOP-1", CommitID: commit.ID}
	newFact := &types.GraphFact{RelationName: "derived_from", SourceEntityID: idB, TargetEntityID: idA, CommitID: commit.ID}
	applied, err := repo.ApplyCommit(ctx, commit, []*types.GraphEntity{newEnt}, []*types.GraphFact{newFact})
	if err != nil {
		t.Fatalf("apply commit: %v", err)
	}
	if applied.EntitiesAdded != 1 || applied.EntitiesUpdated != 0 || applied.FactsAdded != 1 || applied.ID != commit.ID {
		t.Fatalf("commit stats: %+v", applied)
	}
	eSup, fSup, err := repo.RollbackCommit(ctx, tenant, commit.ID)
	if err != nil || eSup != 1 || fSup != 1 {
		t.Fatalf("rollback: e=%d f=%d err=%v", eSup, fSup, err)
	}
	var count int64
	db.Model(&types.GraphEntity{}).Where("id = ? AND status = ?", newEnt.ID, "superseded").Count(&count)
	if count != 1 {
		t.Fatal("rollback did not supersede entity")
	}
	// idempotent second rollback
	if eSup, fSup, err = repo.RollbackCommit(ctx, tenant, commit.ID); err != nil || eSup != 0 || fSup != 0 {
		t.Fatalf("rollback twice: e=%d f=%d err=%v", eSup, fSup, err)
	}

	// --- extraction cache ---
	cache := &types.ChunkExtractionCache{TenantID: tenant, ChunkID: "ch1", ContentHash: "h1", SchemaVersion: 1, Extraction: types.JSON(`{"x":1}`)}
	if err := repo.LetExtractionCache(ctx, cache); err != nil {
		t.Fatalf("let cache: %v", err)
	}
	if cache.ID == "" {
		t.Fatal("cache id not assigned")
	}
	hit, err := repo.GetExtractionCache(ctx, tenant, "ch1", "h1", 1)
	if err != nil || hit == nil || hit.Extraction.ToString() != `{"x":1}` {
		t.Fatalf("cache hit: %v err=%v", hit, err)
	}
	if _, err := repo.GetExtractionCache(ctx, tenant, "ch1", "h2", 1); !errors.Is(err, ErrExtractionCacheMiss) {
		t.Fatalf("cache miss expected, got %v", err)
	}
	cache2 := &types.ChunkExtractionCache{TenantID: tenant, ChunkID: "ch1", ContentHash: "h2", SchemaVersion: 2, Extraction: types.JSON(`{"y":2}`)}
	if err := repo.LetExtractionCache(ctx, cache2); err != nil {
		t.Fatalf("overwrite cache: %v", err)
	}
	if cache2.ID != cache.ID {
		t.Fatalf("cache overwrite created new row: %s vs %s", cache2.ID, cache.ID)
	}
	peek, err := repo.PeekExtractionCache(ctx, tenant, "ch1")
	if err != nil || peek.SchemaVersion != 2 {
		t.Fatalf("peek: %v err=%v", peek, err)
	}

	// --- tenant isolation ---
	if other, err := repo.FindFactIDPaths(ctx, tenant+1, idA, entC.ID, 3, 10); err != nil || len(other) != 0 {
		t.Fatalf("tenant isolation path: %v err=%v", other, err)
	}
}

func jsonUnmarshalForTest(raw types.JSON, v any) error {
	return json.Unmarshal(raw, v)
}
