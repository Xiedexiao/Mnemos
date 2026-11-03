package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
)

// stubImpactRelations serves a fixed relation graph shaped like
// views the reader would see: outbound = "this doc depends on the peer".
type stubImpactRelations struct {
	// byDoc maps a document to the views it sees.
	byDoc map[string][]types.KnowledgeRelationView
}

func (s *stubImpactRelations) Link(
	context.Context, uint64, string, string, types.KnowledgeRelationType, string, string,
	types.KnowledgeRelationOrigin,
) (*types.KnowledgeRelation, error) {
	return nil, nil
}

func (s *stubImpactRelations) Unlink(
	context.Context, uint64, string, string, types.KnowledgeRelationType,
) (int64, error) {
	return 0, nil
}

func (s *stubImpactRelations) List(
	_ context.Context, _ uint64, knowledgeID string,
) ([]types.KnowledgeRelationView, error) {
	return s.byDoc[knowledgeID], nil
}

func (s *stubImpactRelations) DeleteImpact(context.Context, uint64, string) (int64, error) {
	return 0, nil
}

func (s *stubImpactRelations) OnKnowledgeDeleted(context.Context, uint64, string) (int64, error) {
	return 0, nil
}

// TestCollectRelationImpactsWalksDependents covers §7.6: the impact set is the
// documents that depend on the changed one, not the documents it depends on.
func TestCollectRelationImpactsWalksDependents(t *testing.T) {
	relations := &stubImpactRelations{byDoc: map[string][]types.KnowledgeRelationView{
		// The SOP is based on the policy that changed → the SOP is impacted.
		"sop": {
			{PeerID: "policy", RelationType: string(types.RelationBasedOn), Outbound: true},
		},
		// The policy is depended on by the SOP and the checklist.
		"policy": {
			{PeerID: "sop", RelationType: string(types.RelationBasedOn), Outbound: false},
			{PeerID: "checklist", RelationType: string(types.RelationPrerequisite), Outbound: false},
		},
	}}
	svc := &changeRequestService{relations: relations}

	impacts, err := svc.collectRelationImpacts(context.Background(), 7, &types.Knowledge{ID: "policy"})
	require.NoError(t, err)

	byID := map[string]relationImpact{}
	for _, impact := range impacts {
		byID[impact.knowledgeID] = impact
	}
	assert.Contains(t, byID, "sop", "a document based on the changed one is impacted")
	assert.Contains(t, byID, "checklist", "a document requiring the changed one first is impacted")
	assert.NotContains(t, byID, "policy", "the source itself is never its own impact")
}

// TestImpactPriorityFollowsRelationType covers the §7.6 grading: hard
// dependencies are must-change, soft ones suggested, the rest FYI.
func TestImpactPriorityFollowsRelationType(t *testing.T) {
	cases := map[types.KnowledgeRelationType]types.ImpactTaskPriority{
		types.RelationBasedOn:      types.ImpactPriorityMustChange,
		types.RelationPrerequisite: types.ImpactPriorityMustChange,
		types.RelationReplaces:     types.ImpactPrioritySuggested,
		types.RelationSupplements:  types.ImpactPrioritySuggested,
		types.RelationConflicts:    types.ImpactPrioritySuggested,
		types.RelationRelated:      types.ImpactPriorityFYI,
		types.RelationResponsible:  types.ImpactPriorityFYI,
	}
	for relationType, want := range cases {
		t.Run(string(relationType), func(t *testing.T) {
			assert.Equal(t, want, relationImpactPriorityFor(relationType))
		})
	}
}

// TestCollectRelationImpactsStopsAtHopLimit keeps one change from expanding to
// the whole corpus on a densely linked knowledge base.
func TestCollectRelationImpactsStopsAtHopLimit(t *testing.T) {
	// A chain: source ← hop1 ← hop2 ← hop3. With a 2-hop budget, hop3 is out.
	relations := &stubImpactRelations{byDoc: map[string][]types.KnowledgeRelationView{
		"source": {{PeerID: "hop1", RelationType: "based_on", Outbound: false}},
		"hop1":   {{PeerID: "hop2", RelationType: "based_on", Outbound: false}},
		"hop2":   {{PeerID: "hop3", RelationType: "based_on", Outbound: false}},
		"hop3":   {{PeerID: "hop4", RelationType: "based_on", Outbound: false}},
	}}
	svc := &changeRequestService{relations: relations}

	impacts, err := svc.collectRelationImpacts(context.Background(), 7, &types.Knowledge{ID: "source"})
	require.NoError(t, err)

	ids := make([]string, 0, len(impacts))
	for _, impact := range impacts {
		ids = append(ids, impact.knowledgeID)
	}
	assert.Contains(t, ids, "hop1")
	assert.Contains(t, ids, "hop2")
	assert.NotContains(t, ids, "hop3", "traversal must stop at the hop limit")
}

// TestCollectRelationImpactsSkipsOutboundOnly keeps the direction rule: an
// outbound dependency does not make the peer impacted by my change.
func TestCollectRelationImpactsSkipsOutboundOnly(t *testing.T) {
	relations := &stubImpactRelations{byDoc: map[string][]types.KnowledgeRelationView{
		"source": {{PeerID: "upstream", RelationType: "based_on", Outbound: true}},
	}}
	svc := &changeRequestService{relations: relations}

	impacts, err := svc.collectRelationImpacts(context.Background(), 7, &types.Knowledge{ID: "source"})
	require.NoError(t, err)
	assert.Empty(t, impacts, "documents I depend on are not impacted by my change")
}

// TestCollectRelationImpactsWithoutServiceIsNoop keeps Lite mode working.
func TestCollectRelationImpactsWithoutServiceIsNoop(t *testing.T) {
	svc := &changeRequestService{}
	impacts, err := svc.collectRelationImpacts(context.Background(), 7, &types.Knowledge{ID: "doc"})
	require.NoError(t, err)
	assert.Empty(t, impacts)
}

// TestRelationLabelCoversAllTypes keeps operator notes readable.
func TestRelationLabelCoversAllTypes(t *testing.T) {
	for _, relationType := range []types.KnowledgeRelationType{
		types.RelationBasedOn, types.RelationReplaces, types.RelationSupplements,
		types.RelationConflicts, types.RelationPrerequisite, types.RelationResponsible,
		types.RelationRelated,
	} {
		assert.NotEmpty(t, relationLabelZH(relationType), "%s needs a label", relationType)
	}
	assert.Equal(t, "关联", relationLabelZH(types.KnowledgeRelationType("unknown")))
}
