package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

func field(key string, fieldType types.KnowledgeFieldType, rules string) *types.KnowledgeField {
	f := &types.KnowledgeField{FieldKey: key, Label: key, FieldType: string(fieldType)}
	if rules != "" {
		f.Rules = types.JSON(rules)
	}
	return f
}

// TestValidateFieldValuesRequiredAndTypes covers the §3.2 contract: required
// fields are enforced and values are type-checked before they are stored.
func TestValidateFieldValuesRequiredAndTypes(t *testing.T) {
	fields := []*types.KnowledgeField{
		{FieldKey: "equipment", Label: "设备", FieldType: string(types.FieldTypeText), Required: true},
		field("hours", types.FieldTypeNumber, `{"min":1,"max":720}`),
		field("date", types.FieldTypeDate, ""),
		field("severity", types.FieldTypeSelect, `{"options":["low","high"]}`),
	}

	problems := types.ValidateFieldValues(fields, map[string]interface{}{
		"hours":    "0",          // below min
		"date":     "2026/11/02", // wrong format
		"severity": "critical",   // not an option
	})
	byKey := map[string]string{}
	for _, problem := range problems {
		byKey[problem.FieldKey] = problem.Message
	}
	assert.Contains(t, byKey, "equipment", "a missing required field must be reported")
	assert.Contains(t, byKey, "hours")
	assert.Contains(t, byKey, "date")
	assert.Contains(t, byKey, "severity")

	// A well-formed set passes cleanly.
	assert.Empty(t, types.ValidateFieldValues(fields, map[string]interface{}{
		"equipment": "EQ-2000",
		"hours":     48,
		"date":      "2026-11-02",
		"severity":  "high",
	}))
}

// TestValidateFieldValuesRejectsUnknownFields keeps a typo from being silently
// dropped: an editor must not believe they recorded something they did not.
func TestValidateFieldValuesRejectsUnknownFields(t *testing.T) {
	fields := []*types.KnowledgeField{
		{FieldKey: "equipment", Label: "设备", FieldType: string(types.FieldTypeText)},
	}
	problems := types.ValidateFieldValues(fields, map[string]interface{}{
		"equpment": "EQ-2000",
	})
	require.Len(t, problems, 1)
	assert.Equal(t, "equpment", problems[0].FieldKey)
}

// TestValidateFieldValuesMultiselect covers the array-valued type.
func TestValidateFieldValuesMultiselect(t *testing.T) {
	fields := []*types.KnowledgeField{
		field("tags", types.FieldTypeMultiselect, `{"options":["a","b","c"]}`),
	}
	assert.Empty(t, types.ValidateFieldValues(fields, map[string]interface{}{
		"tags": []interface{}{"a", "b"},
	}))
	problems := types.ValidateFieldValues(fields, map[string]interface{}{
		"tags": []interface{}{"a", "zzz"},
	})
	require.Len(t, problems, 1)

	// A bare string is accepted as a comma-separated selection for convenience.
	assert.Empty(t, types.ValidateFieldValues(fields, map[string]interface{}{"tags": "a,b"}))
}

// TestValidateFieldValuesPattern covers the regex rule.
func TestValidateFieldValuesPattern(t *testing.T) {
	fields := []*types.KnowledgeField{
		field("sop", types.FieldTypeText, `{"pattern":"^SOP-[A-Z]{2}-[0-9]{3}$"}`),
	}
	assert.Empty(t, types.ValidateFieldValues(fields, map[string]interface{}{"sop": "SOP-EQ-012"}))
	problems := types.ValidateFieldValues(fields, map[string]interface{}{"sop": "sop-1"})
	require.Len(t, problems, 1)
}

// TestApplyFieldDefaults covers prefill behaviour.
func TestApplyFieldDefaults(t *testing.T) {
	fields := []*types.KnowledgeField{
		{FieldKey: "severity", FieldType: string(types.FieldTypeText), DefaultValue: "medium"},
		{FieldKey: "owner", FieldType: string(types.FieldTypeText)},
	}
	filled := types.ApplyFieldDefaults(fields, map[string]interface{}{})
	assert.Equal(t, "medium", filled["severity"])
	assert.NotContains(t, filled, "owner")

	// An explicit value wins over the default.
	filled = types.ApplyFieldDefaults(fields, map[string]interface{}{"severity": "high"})
	assert.Equal(t, "high", filled["severity"])
}

// TestValidateFieldDefinitionsRejectsUnenforceableConfig covers save-time
// validation: a select without options, or a broken regex, would silently skip
// enforcement on every later entry.
func TestValidateFieldDefinitionsRejectsUnenforceableConfig(t *testing.T) {
	require.NoError(t, validateFieldDefinitions([]*types.KnowledgeField{
		{FieldKey: "a", Label: "A", FieldType: string(types.FieldTypeText)},
	}))
	require.Error(t, validateFieldDefinitions([]*types.KnowledgeField{
		{FieldKey: "", Label: "A"},
	}))
	require.Error(t, validateFieldDefinitions([]*types.KnowledgeField{
		{FieldKey: "a", Label: "A", FieldType: string(types.KnowledgeFieldType("bogus"))},
	}))
	// duplicate keys
	require.Error(t, validateFieldDefinitions([]*types.KnowledgeField{
		{FieldKey: "a", Label: "A"}, {FieldKey: "a", Label: "B"},
	}))
	// select without options
	require.Error(t, validateFieldDefinitions([]*types.KnowledgeField{
		{FieldKey: "s", Label: "S", FieldType: string(types.FieldTypeSelect)},
	}))
	// invalid regex
	require.Error(t, validateFieldDefinitions([]*types.KnowledgeField{
		{FieldKey: "t", Label: "T", FieldType: string(types.FieldTypeText),
			Rules: types.JSON(`{"pattern":"([a-z"}`)},
	}))
}

// TestDiffTemplateFields covers §4.7 change detection.
func TestDiffTemplateFields(t *testing.T) {
	previous := []*types.KnowledgeField{
		{FieldKey: "keep", FieldType: string(types.FieldTypeText)},
		{FieldKey: "drop", FieldType: string(types.FieldTypeText)},
		{FieldKey: "retype", FieldType: string(types.FieldTypeText)},
	}
	next := []*types.KnowledgeField{
		{FieldKey: "keep", FieldType: string(types.FieldTypeText)},
		{FieldKey: "retype", FieldType: string(types.FieldTypeNumber)},
		{FieldKey: "added", Label: "新字段", FieldType: string(types.FieldTypeText)},
	}
	changes := types.DiffTemplateFields(previous, next)
	kinds := map[string]string{}
	for _, change := range changes {
		kinds[change.FieldKey] = change.ChangeKind
	}
	assert.Equal(t, "remove", kinds["drop"])
	assert.Equal(t, "retype", kinds["retype"])
	assert.Equal(t, "add", kinds["added"])
	assert.NotContains(t, kinds, "keep", "unchanged fields generate no task")
}

// TestRecallFieldKeys covers the §3.2 recall-field flag.
func TestRecallFieldKeys(t *testing.T) {
	fields := []*types.KnowledgeField{
		{FieldKey: "equipment", RecallField: true},
		{FieldKey: "notes"},
		{FieldKey: "error_code", RecallField: true},
	}
	assert.Equal(t, []string{"equipment", "error_code"}, types.RecallFieldKeys(fields))
}

// TestEditableFieldsForRole covers field-level visibility.
func TestEditableFieldsForRole(t *testing.T) {
	fields := []*types.KnowledgeField{
		{FieldKey: "public_note", EditableRole: string(types.TenantRoleContributor)},
		{FieldKey: "cost", EditableRole: string(types.TenantRoleAdmin)},
	}
	contributor := types.EditableFieldsForRole(fields, types.TenantRoleContributor)
	require.Len(t, contributor, 1)
	assert.Equal(t, "public_note", contributor[0].FieldKey)

	admin := types.EditableFieldsForRole(fields, types.TenantRoleAdmin)
	assert.Len(t, admin, 2)
}

// stubTemplateRepo records versions in memory.
type stubTemplateRepo struct {
	interfaces.KnowledgeTemplateRepository
	versions map[string][]*types.KnowledgeTemplate
	fields   map[string][]*types.KnowledgeField
	migrated []*types.KnowledgeFieldMigration
	nextID   int
}

func newStubTemplateRepo() *stubTemplateRepo {
	return &stubTemplateRepo{
		versions: map[string][]*types.KnowledgeTemplate{},
		fields:   map[string][]*types.KnowledgeField{},
	}
}

func (s *stubTemplateRepo) CreateVersion(
	_ context.Context, template *types.KnowledgeTemplate, fields []*types.KnowledgeField,
) (*types.KnowledgeTemplate, error) {
	s.nextID++
	template.ID = "tmpl-" + itoaSmall(s.nextID)
	template.Version = len(s.versions[template.TemplateKey]) + 1
	template.Active = true
	for _, existing := range s.versions[template.TemplateKey] {
		existing.Active = false
	}
	s.versions[template.TemplateKey] = append(s.versions[template.TemplateKey], template)
	copied := make([]*types.KnowledgeField, 0, len(fields))
	for _, field := range fields {
		clone := *field
		clone.TemplateID = template.ID
		copied = append(copied, &clone)
	}
	s.fields[template.ID] = copied
	return template, nil
}

func (s *stubTemplateRepo) GetByID(
	_ context.Context, _ uint64, templateID string,
) (*types.KnowledgeTemplate, error) {
	for _, list := range s.versions {
		for _, template := range list {
			if template.ID == templateID {
				template.Fields = s.fields[templateID]
				return template, nil
			}
		}
	}
	return nil, assert.AnError
}

func (s *stubTemplateRepo) GetActiveByKey(
	_ context.Context, _ uint64, templateKey string,
) (*types.KnowledgeTemplate, error) {
	list := s.versions[templateKey]
	if len(list) == 0 {
		return nil, assert.AnError
	}
	template := list[len(list)-1]
	template.Fields = s.fields[template.ID]
	return template, nil
}

func (s *stubTemplateRepo) List(
	_ context.Context, _ uint64, _ int,
) ([]*types.KnowledgeTemplate, error) {
	var out []*types.KnowledgeTemplate
	for _, list := range s.versions {
		if len(list) > 0 {
			out = append(out, list[len(list)-1])
		}
	}
	return out, nil
}

func (s *stubTemplateRepo) ListFields(
	_ context.Context, templateID string,
) ([]*types.KnowledgeField, error) {
	return s.fields[templateID], nil
}

func (s *stubTemplateRepo) CreateMigration(
	_ context.Context, migration *types.KnowledgeFieldMigration,
) error {
	if migration.ID == "" {
		migration.ID = "mig-" + itoaSmall(len(s.migrated)+1)
	}
	s.migrated = append(s.migrated, migration)
	return nil
}

func (s *stubTemplateRepo) ListMigrations(
	_ context.Context, _ uint64, _ types.KnowledgeFieldMigrationStatus, _ int,
) ([]*types.KnowledgeFieldMigration, error) {
	return s.migrated, nil
}

func (s *stubTemplateRepo) UpdateMigrationStatus(
	_ context.Context, _ uint64, migrationID string,
	status types.KnowledgeFieldMigrationStatus, note string,
) (*types.KnowledgeFieldMigration, error) {
	for _, row := range s.migrated {
		if row.ID == migrationID {
			row.Status = string(status)
			row.Note = note
			return row, nil
		}
	}
	return nil, assert.AnError
}

func (s *stubTemplateRepo) CountKnowledgeBySpace(
	_ context.Context, _ uint64, _ string,
) (int64, error) {
	return 42, nil
}

// TestSaveTemplateVersionsAndMigrationTasks covers the §4.7 loop: the first save
// creates version 1 with no migration, and a second save that changes the schema
// generates tasks sized by the affected entry count.
func TestSaveTemplateVersionsAndMigrationTasks(t *testing.T) {
	repo := newStubTemplateRepo()
	svc := NewKnowledgeTemplateService(repo)
	ctx := context.Background()

	first, err := svc.Save(ctx, 7, &types.KnowledgeTemplate{TemplateKey: "fault_case", Name: "故障案例"},
		[]*types.KnowledgeField{
			{FieldKey: "equipment", Label: "设备", FieldType: string(types.FieldTypeText)},
		}, "admin", "kb-1")
	require.NoError(t, err)
	assert.Equal(t, 1, first.Version)
	assert.Empty(t, repo.migrated, "the first version has nothing to migrate")

	second, err := svc.Save(ctx, 7, &types.KnowledgeTemplate{TemplateKey: "fault_case", Name: "故障案例"},
		[]*types.KnowledgeField{
			{FieldKey: "equipment", Label: "设备", FieldType: string(types.FieldTypeText)},
			{FieldKey: "error_code", Label: "错误码", FieldType: string(types.FieldTypeText)},
		}, "admin", "kb-1")
	require.NoError(t, err)
	assert.Equal(t, 2, second.Version)
	require.Len(t, repo.migrated, 1)
	assert.Equal(t, "add", repo.migrated[0].ChangeKind)
	assert.Equal(t, "error_code", repo.migrated[0].FieldKey)
	assert.Equal(t, 42, repo.migrated[0].AffectedCount,
		"the task is sized by how many entries existed at the change")
}

// TestSaveTemplateRejectsEmptyFieldSet keeps a template from being saved without
// any structure, which would make it indistinguishable from "no template".
func TestSaveTemplateRejectsEmptyFieldSet(t *testing.T) {
	svc := NewKnowledgeTemplateService(newStubTemplateRepo())
	_, err := svc.Save(context.Background(), 7,
		&types.KnowledgeTemplate{TemplateKey: "x", Name: "X"}, nil, "admin", "")
	require.Error(t, err)
}

// TestResolveMigrationStatus covers the operator close-out path.
func TestResolveMigrationStatus(t *testing.T) {
	repo := newStubTemplateRepo()
	svc := NewKnowledgeTemplateService(repo)
	ctx := context.Background()
	_, err := svc.Save(ctx, 7, &types.KnowledgeTemplate{TemplateKey: "t", Name: "T"},
		[]*types.KnowledgeField{{FieldKey: "a", Label: "A", FieldType: string(types.FieldTypeText)}},
		"admin", "")
	require.NoError(t, err)
	_, err = svc.Save(ctx, 7, &types.KnowledgeTemplate{TemplateKey: "t", Name: "T"},
		[]*types.KnowledgeField{
			{FieldKey: "a", Label: "A", FieldType: string(types.FieldTypeText)},
			{FieldKey: "b", Label: "B", FieldType: string(types.FieldTypeText)},
		}, "admin", "")
	require.NoError(t, err)
	require.Len(t, repo.migrated, 1)

	// `pending` is not a resolution.
	_, err = svc.ResolveMigration(ctx, 7, repo.migrated[0].ID, types.FieldMigrationPending, "")
	require.Error(t, err)

	row, err := svc.ResolveMigration(ctx, 7, repo.migrated[0].ID, types.FieldMigrationWaived, "历史数据无需回填")
	require.NoError(t, err)
	assert.Equal(t, string(types.FieldMigrationWaived), row.Status)
	assert.Equal(t, "历史数据无需回填", row.Note)
}
