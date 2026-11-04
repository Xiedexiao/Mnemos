package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// stubOntologyRepo is a minimal in-memory schema store.
type stubOntologyRepo struct {
	interfaces.OntologyRepository
	schema  *types.OntologySchema
	types   []*types.OntologyType
	updated bool
}

func (s *stubOntologyRepo) GetSchema(
	_ context.Context, _ uint64, schemaID string,
) (*types.OntologySchema, error) {
	if s.schema == nil || s.schema.ID != schemaID {
		return nil, errors.New("not found")
	}
	return s.schema, nil
}

func (s *stubOntologyRepo) ListTypesBySchema(
	_ context.Context, _ string,
) ([]*types.OntologyType, error) {
	return s.types, nil
}

func (s *stubOntologyRepo) ListRelationsBySchema(
	_ context.Context, _ string,
) ([]*types.OntologyRelation, error) {
	return nil, nil
}

func (s *stubOntologyRepo) PublishSchema(
	_ context.Context, _ uint64, _ string,
) (*types.OntologySchema, error) {
	s.schema.Status = types.OntologySchemaStatusPublished
	s.schema.Version++
	return s.schema, nil
}

func (s *stubOntologyRepo) UpdateSchema(
	_ context.Context, _ *types.OntologySchema,
) error {
	s.updated = true
	return nil
}

// stubOntologyGolden serves the L4 regression gate.
type stubOntologyGolden struct {
	interfaces.GoldenSetService
	sets map[string]bool // setID -> pass
	err  error
}

func (s *stubOntologyGolden) ListSets(
	_ context.Context, _ uint64, domain string,
) ([]*types.GoldenSet, error) {
	if s.err != nil {
		return nil, s.err
	}
	sets := make([]*types.GoldenSet, 0, len(s.sets))
	for id := range s.sets {
		sets = append(sets, &types.GoldenSet{ID: id, Name: id, Domain: domain})
	}
	return sets, nil
}

func (s *stubOntologyGolden) CheckGate(
	_ context.Context, _ uint64, setID string,
) (bool, *types.GoldenSetRun, error) {
	return s.sets[setID], &types.GoldenSetRun{ID: "run-" + setID}, nil
}

// stubOntologyGovernance serves the L2+ approval gate.
type stubOntologyGovernance struct {
	interfaces.GovernanceApprovalService
	approved bool
	err      error
}

func (s *stubOntologyGovernance) RequireApproval(
	_ context.Context, _ uint64, _ types.ApprovalSubjectType, _ string,
) (*types.GovernanceApprovalDecision, error) {
	if s.err != nil {
		return nil, s.err
	}
	if !s.approved {
		return nil, ErrGovernanceNotApproved
	}
	return &types.GovernanceApprovalDecision{Approved: true}, nil
}

func newOntologyFixture(level, domain string) (*ontologyService, *stubOntologyRepo) {
	repo := &stubOntologyRepo{
		schema: &types.OntologySchema{
			ID: "schema-1", TenantID: 7, Name: "设备本体", Domain: domain,
			Status: types.OntologySchemaStatusDraft, Version: 1,
			GovernanceLevel: level,
		},
		types: []*types.OntologyType{{ID: "t-1", SchemaID: "schema-1", Name: "Device"}},
	}
	svc := &ontologyService{
		repo: repo,
		now:  func() time.Time { return time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC) },
	}
	return svc, repo
}

// TestOntologyPublishBlocksL4WithoutGoldenRegression covers the strongest gate:
// a critical ontology may not publish when its domain's evaluation fails.
func TestOntologyPublishBlocksL4WithoutGoldenRegression(t *testing.T) {
	svc, _ := newOntologyFixture("L4", "equipment")
	svc.goldenSets = &stubOntologyGolden{sets: map[string]bool{"set-1": false}}

	_, err := svc.PublishSchema(recommendationCaller(), 7, "schema-1", "admin")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "L4")
}

// TestOntologyPublishAllowsL4WithPassingRegression is the positive case.
func TestOntologyPublishAllowsL4WithPassingRegression(t *testing.T) {
	svc, repo := newOntologyFixture("L4", "equipment")
	svc.goldenSets = &stubOntologyGolden{sets: map[string]bool{"set-1": true}}

	published, err := svc.PublishSchema(recommendationCaller(), 7, "schema-1", "admin")
	require.NoError(t, err, "a passing regression must let the publish through")
	require.NotNil(t, published)
	assert.Equal(t, types.OntologySchemaStatusPublished, published.Status)
	assert.Equal(t, 2, published.Version, "publishing bumps the version")
	assert.False(t, repo.updated, "publish uses PublishSchema, not UpdateSchema")
}

// TestOntologyPublishBlocksL2WithoutApproval covers the dual-approval gate.
func TestOntologyPublishBlocksL2WithoutApproval(t *testing.T) {
	svc, _ := newOntologyFixture("L2", "general")
	svc.governance = &stubOntologyGovernance{approved: false}

	_, err := svc.PublishSchema(recommendationCaller(), 7, "schema-1", "admin")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "治理审批")
}

// TestOntologyPublishAllowsL1WithoutApproval keeps low levels unrestricted.
func TestOntologyPublishAllowsL1WithoutApproval(t *testing.T) {
	svc, _ := newOntologyFixture("L1", "general")
	svc.governance = &stubOntologyGovernance{approved: false}
	svc.goldenSets = &stubOntologyGolden{sets: map[string]bool{"s": false}}

	_, err := svc.PublishSchema(recommendationCaller(), 7, "schema-1", "admin")
	if err != nil {
		assert.NotContains(t, err.Error(), "治理审批")
		assert.NotContains(t, err.Error(), "黄金集回归评测")
	}
}

// TestOntologyPublishL4WithoutAnyGoldenSetFailsClosed: an L4 schema with no
// baseline at all cannot claim critical status.
func TestOntologyPublishL4WithoutAnyGoldenSetFailsClosed(t *testing.T) {
	svc, _ := newOntologyFixture("L4", "equipment")
	svc.goldenSets = &stubOntologyGolden{sets: map[string]bool{}}

	_, err := svc.PublishSchema(recommendationCaller(), 7, "schema-1", "admin")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "L4")
}

// TestOntologyPublishGateLookupFailureDoesNotBlock keeps a database blip from
// freezing every ontology publish: governance is an enhancement, not a
// security gate.
func TestOntologyPublishGateLookupFailureDoesNotBlock(t *testing.T) {
	svc, _ := newOntologyFixture("L4", "equipment")
	svc.goldenSets = &stubOntologyGolden{err: errors.New("db down")}

	_, err := svc.PublishSchema(recommendationCaller(), 7, "schema-1", "admin")
	if err != nil {
		assert.NotContains(t, err.Error(), "黄金集回归评测")
	}
}
