package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// stubRelationRepo keeps pairs in memory, mirroring the bidirectional store.
type stubRelationRepo struct {
	interfaces.KnowledgeRelationRepository
	rows []*types.KnowledgeRelation
}

func (s *stubRelationRepo) CreatePair(
	_ context.Context, forward, reverse *types.KnowledgeRelation,
) error {
	for _, relation := range []*types.KnowledgeRelation{forward, reverse} {
		if relation == nil {
			continue
		}
		duplicate := false
		for _, existing := range s.rows {
			if existing.FromKnowledgeID == relation.FromKnowledgeID &&
				existing.ToKnowledgeID == relation.ToKnowledgeID &&
				existing.RelationType == relation.RelationType {
				duplicate = true
				break
			}
		}
		if !duplicate {
			s.rows = append(s.rows, relation)
		}
	}
	return nil
}

func (s *stubRelationRepo) ListForKnowledge(
	_ context.Context, _ uint64, knowledgeID string,
) ([]*types.KnowledgeRelation, error) {
	var out []*types.KnowledgeRelation
	for _, relation := range s.rows {
		if relation.FromKnowledgeID == knowledgeID || relation.ToKnowledgeID == knowledgeID {
			out = append(out, relation)
		}
	}
	return out, nil
}

func (s *stubRelationRepo) DeletePair(
	_ context.Context, _ uint64, fromID, toID, relationType string,
) (int64, error) {
	pairKey := types.KnowledgeRelationPairKey(fromID, toID, relationType)
	kept := make([]*types.KnowledgeRelation, 0, len(s.rows))
	removed := int64(0)
	for _, relation := range s.rows {
		if relation.PairKey == pairKey {
			removed++
			continue
		}
		kept = append(kept, relation)
	}
	s.rows = kept
	return removed, nil
}

func (s *stubRelationRepo) DeleteAllForKnowledge(
	_ context.Context, _ uint64, knowledgeID string,
) (int64, error) {
	kept := make([]*types.KnowledgeRelation, 0, len(s.rows))
	removed := int64(0)
	for _, relation := range s.rows {
		if relation.FromKnowledgeID == knowledgeID || relation.ToKnowledgeID == knowledgeID {
			removed++
			continue
		}
		kept = append(kept, relation)
	}
	s.rows = kept
	return removed, nil
}

func (s *stubRelationRepo) CountForKnowledge(
	_ context.Context, _ uint64, knowledgeID string,
) (int64, error) {
	count := int64(0)
	for _, relation := range s.rows {
		if relation.FromKnowledgeID == knowledgeID || relation.ToKnowledgeID == knowledgeID {
			count++
		}
	}
	return count, nil
}

// stubRelationKnowledges serves document lookups.
type stubRelationKnowledges struct {
	interfaces.KnowledgeRepository
	docs map[string]*types.Knowledge
}

func (s *stubRelationKnowledges) GetKnowledgeByID(
	_ context.Context, _ uint64, id string,
) (*types.Knowledge, error) {
	if doc, ok := s.docs[id]; ok {
		return doc, nil
	}
	return nil, assert.AnError
}

func newRelationService() (*knowledgeRelationService, *stubRelationRepo) {
	repo := &stubRelationRepo{}
	knowledges := &stubRelationKnowledges{docs: map[string]*types.Knowledge{
		"doc-a": {ID: "doc-a", TenantID: 7},
		"doc-b": {ID: "doc-b", TenantID: 7},
		"doc-x": {ID: "doc-x", TenantID: 99}, // another tenant
	}}
	return &knowledgeRelationService{repo: repo, knowledges: knowledges}, repo
}

// TestLinkCreatesBidirectionalEdge is the §4.5 core: linking A→B must make the
// relation visible from B, which is what "谁引用了当前页面" depends on.
func TestLinkCreatesBidirectionalEdge(t *testing.T) {
	svc, repo := newRelationService()
	ctx := context.Background()

	_, err := svc.Link(ctx, 7, "doc-a", "doc-b", types.RelationBasedOn,
		"A 的结论依据 B", "user-1", types.RelationOriginManual)
	require.NoError(t, err)
	require.Len(t, repo.rows, 2, "one forward row plus its reverse mirror")

	// From B, the edge is visible as an incoming relation.
	viewsB, err := svc.List(ctx, 7, "doc-b")
	require.NoError(t, err)
	require.Len(t, viewsB, 1)
	assert.Equal(t, "doc-a", viewsB[0].PeerID)
	assert.False(t, viewsB[0].Outbound, "B is on the receiving end")
	assert.Equal(t, string(types.RelationBasedOn), viewsB[0].RelationType)
}

// TestListDeduplicatesMirrors keeps every relation from rendering twice.
func TestListDeduplicatesMirrors(t *testing.T) {
	svc, _ := newRelationService()
	ctx := context.Background()
	_, err := svc.Link(ctx, 7, "doc-a", "doc-b", types.RelationSupplements, "", "user-1",
		types.RelationOriginManual)
	require.NoError(t, err)

	viewsA, err := svc.List(ctx, 7, "doc-a")
	require.NoError(t, err)
	assert.Len(t, viewsA, 1, "the reverse mirror must not surface as a second row")
	assert.True(t, viewsA[0].Outbound)
	assert.Equal(t, "doc-b", viewsA[0].PeerID)
}

// TestLinkRejectsSelfLink covers the self-referential case.
func TestLinkRejectsSelfLink(t *testing.T) {
	svc, _ := newRelationService()
	_, err := svc.Link(context.Background(), 7, "doc-a", "doc-a", types.RelationRelated,
		"", "user-1", types.RelationOriginManual)
	require.Error(t, err)
}

// TestLinkRejectsCrossTenant is the §2.10 existence rule: linking across tenants
// would leak a title from one tenant to another.
func TestLinkRejectsCrossTenant(t *testing.T) {
	svc, _ := newRelationService()
	_, err := svc.Link(context.Background(), 7, "doc-a", "doc-x", types.RelationRelated,
		"", "user-1", types.RelationOriginManual)
	require.Error(t, err)
}

// TestLinkRejectsUnknownType keeps the type whitelist honest.
func TestLinkRejectsUnknownType(t *testing.T) {
	svc, _ := newRelationService()
	_, err := svc.Link(context.Background(), 7, "doc-a", "doc-b",
		types.KnowledgeRelationType("invented"), "", "user-1", types.RelationOriginManual)
	require.Error(t, err)
}

// TestUnlinkRemovesBothDirections covers pair deletion.
func TestUnlinkRemovesBothDirections(t *testing.T) {
	svc, repo := newRelationService()
	ctx := context.Background()
	_, err := svc.Link(ctx, 7, "doc-a", "doc-b", types.RelationPrerequisite, "", "user-1",
		types.RelationOriginManual)
	require.NoError(t, err)
	require.Len(t, repo.rows, 2)

	removed, err := svc.Unlink(ctx, 7, "doc-a", "doc-b", types.RelationPrerequisite)
	require.NoError(t, err)
	assert.EqualValues(t, 2, removed, "both directions go together")
	assert.Empty(t, repo.rows)
}

// TestDeleteImpactCountsEdges covers the pre-delete check (§4.5 删除时检查引用).
func TestDeleteImpactCountsEdges(t *testing.T) {
	svc, _ := newRelationService()
	ctx := context.Background()
	_, err := svc.Link(ctx, 7, "doc-a", "doc-b", types.RelationResponsible, "", "user-1",
		types.RelationOriginManual)
	require.NoError(t, err)

	count, err := svc.DeleteImpact(ctx, 7, "doc-a")
	require.NoError(t, err)
	assert.EqualValues(t, 2, count, "forward and reverse rows both touch the document")
}

// TestOnKnowledgeDeletedRemovesEdges keeps no edge pointing at a deleted row.
func TestOnKnowledgeDeletedRemovesEdges(t *testing.T) {
	svc, repo := newRelationService()
	ctx := context.Background()
	_, err := svc.Link(ctx, 7, "doc-a", "doc-b", types.RelationConflicts, "", "user-1",
		types.RelationOriginManual)
	require.NoError(t, err)

	removed, err := svc.OnKnowledgeDeleted(ctx, 7, "doc-b")
	require.NoError(t, err)
	assert.EqualValues(t, 2, removed)
	assert.Empty(t, repo.rows)
}

// TestRelationTypeWhitelist pins the seven §4.5 relation types.
func TestRelationTypeWhitelist(t *testing.T) {
	all := []types.KnowledgeRelationType{
		types.RelationBasedOn, types.RelationReplaces, types.RelationSupplements,
		types.RelationConflicts, types.RelationPrerequisite, types.RelationResponsible,
		types.RelationRelated,
	}
	for _, relationType := range all {
		assert.True(t, relationType.Valid(), "%s must be valid", relationType)
	}
	assert.False(t, types.KnowledgeRelationType("bogus").Valid())
	// Conflict is the only strictly symmetric relation.
	assert.True(t, types.RelationConflicts.Symmetric())
	assert.False(t, types.RelationBasedOn.Symmetric())
}

// TestPairKeyIsDirectionIndependent is what makes the uniqueness constraint and
// paired deletion work from both ends.
func TestPairKeyIsDirectionIndependent(t *testing.T) {
	assert.Equal(t,
		types.KnowledgeRelationPairKey("a", "b", "based_on"),
		types.KnowledgeRelationPairKey("b", "a", "based_on"))
	assert.NotEqual(t,
		types.KnowledgeRelationPairKey("a", "b", "based_on"),
		types.KnowledgeRelationPairKey("a", "b", "replaces"))
}

// TestBuildRelationViewsDirections covers the outbound/inbound projection.
func TestBuildRelationViewsDirections(t *testing.T) {
	relations := []*types.KnowledgeRelation{
		{FromKnowledgeID: "self", ToKnowledgeID: "other", RelationType: "based_on", Origin: "manual"},
	}
	views := types.BuildRelationViews("self", relations)
	require.Len(t, views, 1)
	assert.True(t, views[0].Outbound)
	assert.Equal(t, "other", views[0].PeerID)

	// The same row read from the other side is inbound.
	views = types.BuildRelationViews("other", relations)
	require.Len(t, views, 1)
	assert.False(t, views[0].Outbound)
	assert.Equal(t, "self", views[0].PeerID)
}
