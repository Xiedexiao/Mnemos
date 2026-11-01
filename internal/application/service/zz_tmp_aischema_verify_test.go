package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// 临时验证：落地前核对纯函数与兼容性/落地逻辑（验证后删除，不进入交付）。

func TestAISchemaExtractJSONObject(t *testing.T) {
	cases := map[string]string{
		"```json\n{\"types\":[{\"name\":\"A\"}]}\n```": "{\"types\":[{\"name\":\"A\"}]}",
		"prefix {\"a\":{\"b\":1}} suffix":              "{\"a\":{\"b\":1}}",
		// 字符串里的花括号不参与深度计数
		"{\"a\":\"} not a brace {\"}}": "{\"a\":\"} not a brace {\"}",
		"no json":                      "",
	}
	for input, want := range cases {
		if got := aischemaExtractJSONObject(input); got != want {
			t.Errorf("extract(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestProposalPayloadValidate(t *testing.T) {
	payload := &types.ProposalPayload{Types: []types.ProposalType{
		{Name: "Device", Properties: []types.ProposalProperty{{Name: "model", DataType: "string"}}},
	}, Relations: []types.ProposalRelation{
		{Name: "belongs_to", SourceType: "Device", TargetType: "Device"},
	}}
	payload.Normalize()
	if err := payload.Validate(); err != nil {
		t.Fatalf("valid payload rejected: %v", err)
	}
	if payload.Relations[0].Cardinality != "many_to_many" {
		t.Errorf("cardinality = %q, want many_to_many", payload.Relations[0].Cardinality)
	}
	if payload.Types[0].Properties[0].DataType != "string" {
		t.Errorf("data_type = %q", payload.Types[0].Properties[0].DataType)
	}
	if payload.Types[0].DisplayName != "Device" {
		t.Errorf("display_name = %q, want Device", payload.Types[0].DisplayName)
	}

	bad := &types.ProposalPayload{Types: []types.ProposalType{
		{Name: "Device", Properties: []types.ProposalProperty{{Name: "x", DataType: "nonsense"}}},
	}}
	bad.Normalize()
	if err := bad.Validate(); err == nil {
		t.Error("expected invalid data_type to be rejected")
	}

	dup := &types.ProposalPayload{Types: []types.ProposalType{{Name: "A"}, {Name: "a"}}}
	dup.Normalize()
	if err := dup.Validate(); err == nil {
		t.Error("expected duplicate type to be rejected")
	}

	spaced := &types.ProposalPayload{Types: []types.ProposalType{{Name: "Bad Name"}}}
	spaced.Normalize()
	if err := spaced.Validate(); err == nil {
		t.Error("expected whitespace in type name to be rejected")
	}
}

func TestAISchemaDetectRename(t *testing.T) {
	existing := map[string]*types.OntologyTypeDetail{
		"productionline": {OntologyType: types.OntologyType{Name: "ProductionLine", Aliases: types.JSON(`["产线"]`)}},
	}
	if r := aischemaDetectRename(types.ProposalType{Name: "Production_Line"}, existing); r == nil || r.From != "ProductionLine" {
		t.Errorf("expected rename detection for Production_Line, got %v", r)
	}
	if r := aischemaDetectRename(types.ProposalType{Name: "产线"}, existing); r == nil || r.From != "ProductionLine" {
		t.Errorf("expected alias rename detection, got %v", r)
	}
	if r := aischemaDetectRename(types.ProposalType{Name: "Defect"}, existing); r != nil {
		t.Errorf("unexpected rename: %v", r)
	}
}

func TestCompatibilityReportHasBlocker(t *testing.T) {
	if (&types.CompatibilityReport{Compatible: true}).HasBlocker() {
		t.Error("compatible report must not block")
	}
	if !(&types.CompatibilityReport{Compatible: false}).HasBlocker() {
		t.Error("incompatible report must block")
	}
}

// ============================================================================
// 兼容性检查 + 落地路径（stub 本体服务）
// ============================================================================

// stubOntology 是 interfaces.OntologyService 的最小内存实现，只覆盖
// schema/type/property/relation 的增查（提案服务用到的全部方法）。
type stubOntology struct {
	schemas   map[string]*types.OntologySchema
	types     map[string]*types.OntologyType
	props     map[string]*types.OntologyProperty
	relations map[string]*types.OntologyRelation
	seq       int

	addTypeErr error
}

func newStubOntology() *stubOntology {
	return &stubOntology{
		schemas:   map[string]*types.OntologySchema{},
		types:     map[string]*types.OntologyType{},
		props:     map[string]*types.OntologyProperty{},
		relations: map[string]*types.OntologyRelation{},
	}
}

func (s *stubOntology) nextID(prefix string) string {
	s.seq++
	return prefix + string(rune('a'+s.seq%26)) + string(rune('0'+s.seq/26%10))
}

func (s *stubOntology) CreateSchema(_ context.Context, schema *types.OntologySchema, _ string) (*types.OntologySchema, error) {
	if schema.ID == "" {
		schema.ID = s.nextID("schema-")
	}
	if schema.Version == 0 {
		schema.Version = 1
	}
	schema.Status = types.OntologySchemaStatusDraft
	clone := *schema
	s.schemas[clone.ID] = &clone
	return &clone, nil
}

func (s *stubOntology) UpdateSchema(context.Context, uint64, string, *types.OntologySchemaUpdate, string) (*types.OntologySchema, error) {
	return nil, errors.New("not implemented")
}

func (s *stubOntology) PublishSchema(context.Context, uint64, string, string) (*types.OntologySchema, error) {
	return nil, errors.New("not implemented")
}

func (s *stubOntology) DeleteSchema(context.Context, uint64, string, string) error {
	return errors.New("not implemented")
}

func (s *stubOntology) ListSchemas(context.Context, uint64) ([]*types.OntologySchema, error) {
	out := make([]*types.OntologySchema, 0, len(s.schemas))
	for _, schema := range s.schemas {
		out = append(out, schema)
	}
	return out, nil
}

func (s *stubOntology) GetSchema(_ context.Context, _ uint64, schemaID string) (*types.OntologySchemaDetail, error) {
	schema, ok := s.schemas[schemaID]
	if !ok {
		return nil, errors.New("schema not found")
	}
	detail := &types.OntologySchemaDetail{Schema: schema}
	for _, t := range s.types {
		if t.SchemaID != schemaID {
			continue
		}
		typeDetail := &types.OntologyTypeDetail{OntologyType: *t}
		for _, p := range s.props {
			if p.TypeID == t.ID {
				typeDetail.Properties = append(typeDetail.Properties, p)
			}
		}
		detail.Types = append(detail.Types, typeDetail)
	}
	for _, rel := range s.relations {
		if rel.SchemaID == schemaID {
			detail.Relations = append(detail.Relations, rel)
		}
	}
	return detail, nil
}

func (s *stubOntology) AddType(_ context.Context, _ uint64, t *types.OntologyType, _ string) (*types.OntologyType, error) {
	if s.addTypeErr != nil {
		return nil, s.addTypeErr
	}
	if t.ID == "" {
		t.ID = s.nextID("type-")
	}
	clone := *t
	s.types[clone.ID] = &clone
	return &clone, nil
}

func (s *stubOntology) UpdateType(context.Context, uint64, string, *types.OntologyTypeUpdate, string) (*types.OntologyType, error) {
	return nil, errors.New("not implemented")
}

func (s *stubOntology) DeleteType(context.Context, uint64, string, string) error {
	return errors.New("not implemented")
}

func (s *stubOntology) AddProperty(_ context.Context, _ uint64, p *types.OntologyProperty, _ string) (*types.OntologyProperty, error) {
	if p.ID == "" {
		p.ID = s.nextID("prop-")
	}
	clone := *p
	s.props[clone.ID] = &clone
	return &clone, nil
}

func (s *stubOntology) UpdateProperty(context.Context, uint64, string, *types.OntologyPropertyUpdate, string) (*types.OntologyProperty, error) {
	return nil, errors.New("not implemented")
}

func (s *stubOntology) DeleteProperty(context.Context, uint64, string, string) error {
	return errors.New("not implemented")
}

func (s *stubOntology) AddRelation(_ context.Context, _ uint64, rel *types.OntologyRelation, _ string) (*types.OntologyRelation, error) {
	if rel.ID == "" {
		rel.ID = s.nextID("rel-")
	}
	clone := *rel
	s.relations[clone.ID] = &clone
	return &clone, nil
}

func (s *stubOntology) UpdateRelation(context.Context, uint64, string, *types.OntologyRelationUpdate, string) (*types.OntologyRelation, error) {
	return nil, errors.New("not implemented")
}

func (s *stubOntology) DeleteRelation(context.Context, uint64, string, string) error {
	return errors.New("not implemented")
}

// stubProposalRepo 是 interfaces.SchemaProposalRepository 的内存实现。
type stubProposalRepo struct {
	items map[string]*types.SchemaProposal
}

func newStubProposalRepo() *stubProposalRepo {
	return &stubProposalRepo{items: map[string]*types.SchemaProposal{}}
}

func (r *stubProposalRepo) Create(_ context.Context, p *types.SchemaProposal) error {
	clone := *p
	r.items[clone.ID] = &clone
	return nil
}

func (r *stubProposalRepo) GetByID(_ context.Context, _ uint64, id string) (*types.SchemaProposal, error) {
	item, ok := r.items[id]
	if !ok {
		return nil, errors.New("schema proposal not found")
	}
	clone := *item
	return &clone, nil
}

func (r *stubProposalRepo) List(context.Context, uint64, types.SchemaProposalListFilter) ([]*types.SchemaProposal, int64, error) {
	out := make([]*types.SchemaProposal, 0, len(r.items))
	for _, item := range r.items {
		clone := *item
		out = append(out, &clone)
	}
	return out, int64(len(out)), nil
}

func (r *stubProposalRepo) Update(_ context.Context, p *types.SchemaProposal) error {
	clone := *p
	r.items[clone.ID] = &clone
	return nil
}

func (r *stubProposalRepo) UpdateCompatibilityReport(_ context.Context, p *types.SchemaProposal) error {
	if existing, ok := r.items[p.ID]; ok {
		existing.CompatibilityReport = p.CompatibilityReport
	}
	return nil
}

func newTestProposalService(onto *stubOntology, repo *stubProposalRepo) *schemaProposalService {
	return &schemaProposalService{
		repo:     repo,
		ontology: onto,
		now:      time.Now,
	}
}

// adminCtx 构造一个 Admin 调用者上下文。
func adminCtx(ctx context.Context, tenantID uint64, userID string) context.Context {
	return types.WithCaller(ctx, types.Caller{TenantID: tenantID, UserID: userID, Role: types.TenantRoleAdmin})
}

var _ interfaces.OntologyService = (*stubOntology)(nil)
var _ interfaces.SchemaProposalRepository = (*stubProposalRepo)(nil)
