package types

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ============================================================================
// 自定义字段与模板中心（KnowAxis V3.0 §3.2 人工录入 / §4.7 模板与字段治理）。
//
// 平台此前只有自由 JSON 的 custom_metadata：能存，但无法校验、无法知道
// "这类知识该填哪些字段"，也谈不上"字段改名要生成迁移任务"。这里补齐
// 定义层：模板 = 一组带类型的字段；模板每次修改生成新版本，历史知识仍按
// 录入时的版本解释。
// ============================================================================

// KnowledgeFieldType is the value type of a custom field.
type KnowledgeFieldType string

const (
	// FieldTypeText 单行文本。
	FieldTypeText KnowledgeFieldType = "text"
	// FieldTypeTextarea 多行文本。
	FieldTypeTextarea KnowledgeFieldType = "textarea"
	// FieldTypeNumber 数字。
	FieldTypeNumber KnowledgeFieldType = "number"
	// FieldTypeDate 日期（YYYY-MM-DD）。
	FieldTypeDate KnowledgeFieldType = "date"
	// FieldTypeSelect 单选（值必须来自 options）。
	FieldTypeSelect KnowledgeFieldType = "select"
	// FieldTypeMultiselect 多选（每个值都必须来自 options）。
	FieldTypeMultiselect KnowledgeFieldType = "multiselect"
	// FieldTypeAttachment 附件引用。
	FieldTypeAttachment KnowledgeFieldType = "attachment"
	// FieldTypeReference 关联知识/实体。
	FieldTypeReference KnowledgeFieldType = "reference"
)

// Valid reports whether the field type is known.
func (t KnowledgeFieldType) Valid() bool {
	switch t {
	case FieldTypeText, FieldTypeTextarea, FieldTypeNumber, FieldTypeDate,
		FieldTypeSelect, FieldTypeMultiselect, FieldTypeAttachment, FieldTypeReference:
		return true
	default:
		return false
	}
}

// KnowledgeTemplate is one versioned knowledge template.
type KnowledgeTemplate struct {
	ID       string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID uint64 `json:"tenant_id" gorm:"not null;uniqueIndex:uq_knowledge_templates_key_version,priority:1"`
	// TemplateKey is stable across versions.
	TemplateKey string `json:"template_key" gorm:"type:varchar(64);not null;uniqueIndex:uq_knowledge_templates_key_version,priority:2"`
	Version     int    `json:"version" gorm:"not null;uniqueIndex:uq_knowledge_templates_key_version,priority:3"`
	Name        string `json:"name" gorm:"type:varchar(128);not null"`
	Description string `json:"description" gorm:"type:text;not null;default:''"`
	// KnowledgeType scopes the template to one ingestion type; empty = any.
	KnowledgeType string `json:"knowledge_type" gorm:"type:varchar(32);not null;default:''"`
	// Active marks the version used for new entries. Superseded versions stay
	// readable so historical knowledge keeps its schema.
	Active    bool      `json:"active" gorm:"not null;default:true"`
	CreatedBy string    `json:"created_by" gorm:"type:varchar(36);not null;default:''"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// Fields is not persisted on this row; it is filled by the service.
	Fields []*KnowledgeField `json:"fields,omitempty" gorm:"-"`
}

// TableName pins the GORM table name.
func (KnowledgeTemplate) TableName() string { return "knowledge_templates" }

// BeforeCreate assigns an ID before persistence.
func (t *KnowledgeTemplate) BeforeCreate(_ *gorm.DB) error {
	if t.ID == "" {
		t.ID = uuid.NewString()
	}
	return nil
}

// KnowledgeField is one custom field definition.
type KnowledgeField struct {
	ID         string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID   uint64 `json:"tenant_id" gorm:"not null"`
	TemplateID string `json:"template_id" gorm:"type:varchar(36);not null;uniqueIndex:uq_knowledge_fields_key,priority:1"`
	FieldKey   string `json:"field_key" gorm:"type:varchar(64);not null;uniqueIndex:uq_knowledge_fields_key,priority:2"`
	Label      string `json:"label" gorm:"type:varchar(128);not null"`
	FieldType  string `json:"field_type" gorm:"type:varchar(24);not null;default:'text'"`
	Required   bool   `json:"required" gorm:"not null;default:false"`
	// DefaultValue is the prefill, interpreted by FieldType.
	DefaultValue string `json:"default_value" gorm:"type:varchar(512);not null;default:''"`
	// Rules carries {"min","max","pattern","options"}.
	Rules JSON `json:"rules" gorm:"type:json;not null;default:'{}'"`
	// EditableRole is the minimum role allowed to edit this field.
	EditableRole string `json:"editable_role" gorm:"type:varchar(32);not null;default:'contributor'"`
	// RecallField marks a field used for precise retrieval and ranking (§3.2).
	RecallField bool      `json:"recall_field" gorm:"not null;default:false"`
	SortOrder   int       `json:"sort_order" gorm:"not null;default:0"`
	CreatedAt   time.Time `json:"created_at"`
}

// TableName pins the GORM table name.
func (KnowledgeField) TableName() string { return "knowledge_fields" }

// BeforeCreate assigns an ID before persistence.
func (f *KnowledgeField) BeforeCreate(_ *gorm.DB) error {
	if f.ID == "" {
		f.ID = uuid.NewString()
	}
	return nil
}

// KnowledgeFieldRules is the decoded Rules blob.
type KnowledgeFieldRules struct {
	// Options restricts select/multiselect values.
	Options []string `json:"options,omitempty"`
	// Min/Max bound numbers (and string lengths for text fields).
	Min *float64 `json:"min,omitempty"`
	Max *float64 `json:"max,omitempty"`
	// Pattern is a regular expression the value must match.
	Pattern string `json:"pattern,omitempty"`
}

// ParsedRules decodes Rules, treating bad JSON as no constraints.
func (f *KnowledgeField) ParsedRules() KnowledgeFieldRules {
	var rules KnowledgeFieldRules
	if f == nil || len(f.Rules) == 0 {
		return rules
	}
	_ = json.Unmarshal(f.Rules, &rules)
	return rules
}

// FieldValidationError describes one rejected value.
type FieldValidationError struct {
	FieldKey string `json:"field_key"`
	Message  string `json:"message"`
}

// Error renders the first problem for error strings.
func (e *FieldValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.FieldKey, e.Message)
}

// ValidateValues checks user-supplied values against a template's fields
// (KnowAxis V3.0 §3.2 字段校验规则).
//
// Unknown keys are rejected rather than ignored: silently dropping a typo'd
// field would let an editor believe they recorded something they did not.
func ValidateFieldValues(fields []*KnowledgeField, values map[string]interface{}) []FieldValidationError {
	if len(fields) == 0 {
		return nil
	}
	known := make(map[string]*KnowledgeField, len(fields))
	var problems []FieldValidationError
	for _, field := range fields {
		if field != nil {
			known[field.FieldKey] = field
		}
	}
	for key := range values {
		if _, ok := known[key]; !ok {
			problems = append(problems, FieldValidationError{
				FieldKey: key, Message: "字段未在模板中定义",
			})
		}
	}
	for _, field := range fields {
		if field == nil {
			continue
		}
		value, present := values[field.FieldKey]
		if !present || value == nil || strings.TrimSpace(fmt.Sprintf("%v", value)) == "" {
			if field.Required {
				problems = append(problems, FieldValidationError{
					FieldKey: field.FieldKey, Message: "必填字段",
				})
			}
			continue
		}
		if problem := validateFieldValue(field, value); problem != "" {
			problems = append(problems, FieldValidationError{FieldKey: field.FieldKey, Message: problem})
		}
	}
	return problems
}

// validateFieldValue applies one field's type and rule constraints.
func validateFieldValue(field *KnowledgeField, value interface{}) string {
	rules := field.ParsedRules()
	text := strings.TrimSpace(fmt.Sprintf("%v", value))
	switch KnowledgeFieldType(field.FieldType) {
	case FieldTypeNumber:
		number, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return "必须是数字"
		}
		if rules.Min != nil && number < *rules.Min {
			return fmt.Sprintf("不得小于 %v", *rules.Min)
		}
		if rules.Max != nil && number > *rules.Max {
			return fmt.Sprintf("不得大于 %v", *rules.Max)
		}
	case FieldTypeDate:
		if _, err := time.Parse("2006-01-02", text); err != nil {
			return "日期格式应为 YYYY-MM-DD"
		}
	case FieldTypeSelect:
		if len(rules.Options) > 0 && !containsOption(rules.Options, text) {
			return "取值不在允许范围内"
		}
	case FieldTypeMultiselect:
		items, ok := toSelectionList(value)
		if !ok {
			return "多选字段应为数组"
		}
		for _, item := range items {
			if len(rules.Options) > 0 && !containsOption(rules.Options, item) {
				return fmt.Sprintf("%q 不在允许范围内", item)
			}
		}
	case FieldTypeText, FieldTypeTextarea:
		if rules.Pattern != "" {
			re, err := regexp.Compile(rules.Pattern)
			if err != nil {
				// 配置错误不该拦用户：无效正则在写入期已被拒绝，这里放行。
				return ""
			}
			if !re.MatchString(text) {
				return "格式不符合要求"
			}
		}
		length := float64(len([]rune(text)))
		if rules.Min != nil && length < *rules.Min {
			return fmt.Sprintf("长度不得少于 %v", *rules.Min)
		}
		if rules.Max != nil && length > *rules.Max {
			return fmt.Sprintf("长度不得超过 %v", *rules.Max)
		}
	}
	return ""
}

// toSelectionList normalises a multiselect value.
func toSelectionList(value interface{}) ([]string, bool) {
	switch typed := value.(type) {
	case []string:
		return typed, true
	case []interface{}:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			out = append(out, fmt.Sprintf("%v", item))
		}
		return out, true
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil, true
		}
		return strings.Split(typed, ","), true
	default:
		return nil, false
	}
}

// containsOption reports whether want is one of options.
func containsOption(options []string, want string) bool {
	for _, option := range options {
		if option == want {
			return true
		}
	}
	return false
}

// ApplyFieldDefaults fills missing values from each field's DefaultValue.
// Callers use this before validation so "empty" means "use the default".
func ApplyFieldDefaults(fields []*KnowledgeField, values map[string]interface{}) map[string]interface{} {
	if values == nil {
		values = map[string]interface{}{}
	}
	for _, field := range fields {
		if field == nil || field.DefaultValue == "" {
			continue
		}
		if existing, ok := values[field.FieldKey]; ok &&
			strings.TrimSpace(fmt.Sprintf("%v", existing)) != "" {
			continue
		}
		values[field.FieldKey] = field.DefaultValue
	}
	return values
}

// RecallFieldKeys returns the field keys marked as recall fields (§3.2).
// Changing these requires an index rebuild, which is why the definition flags
// them instead of treating them as ordinary metadata.
func RecallFieldKeys(fields []*KnowledgeField) []string {
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if field != nil && field.RecallField {
			out = append(out, field.FieldKey)
		}
	}
	return out
}

// EditableFieldsForRole filters to the fields a role may edit.
func EditableFieldsForRole(fields []*KnowledgeField, role TenantRole) []*KnowledgeField {
	out := make([]*KnowledgeField, 0, len(fields))
	for _, field := range fields {
		if field == nil {
			continue
		}
		required := TenantRole(field.EditableRole)
		if !required.IsValid() || role.HasPermission(required) {
			out = append(out, field)
		}
	}
	return out
}

// KnowledgeFieldMigration is one structural field change and the work it implies
// for existing content (§4.7 字段变更生成迁移任务).
type KnowledgeFieldMigration struct {
	ID         string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID   uint64 `json:"tenant_id" gorm:"not null"`
	TemplateID string `json:"template_id" gorm:"type:varchar(36);not null"`
	// ChangeKind is add | remove | rename | retype.
	ChangeKind  string `json:"change_kind" gorm:"type:varchar(16);not null"`
	FieldKey    string `json:"field_key" gorm:"type:varchar(64);not null"`
	PreviousKey string `json:"previous_key" gorm:"type:varchar(64);not null;default:''"`
	// AffectedCount snapshots how many entries existed when the change was made.
	AffectedCount int       `json:"affected_count" gorm:"not null;default:0"`
	Status        string    `json:"status" gorm:"type:varchar(16);not null;default:'pending'"`
	Note          string    `json:"note" gorm:"type:text;not null;default:''"`
	CreatedBy     string    `json:"created_by" gorm:"type:varchar(36);not null;default:''"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// TableName pins the GORM table name.
func (KnowledgeFieldMigration) TableName() string { return "knowledge_field_migrations" }

// BeforeCreate assigns an ID before persistence.
func (m *KnowledgeFieldMigration) BeforeCreate(_ *gorm.DB) error {
	if m.ID == "" {
		m.ID = uuid.NewString()
	}
	return nil
}

// KnowledgeFieldMigrationStatus is the task lifecycle.
type KnowledgeFieldMigrationStatus string

const (
	// FieldMigrationPending still needs operator work.
	FieldMigrationPending KnowledgeFieldMigrationStatus = "pending"
	// FieldMigrationDone backfill completed.
	FieldMigrationDone KnowledgeFieldMigrationStatus = "done"
	// FieldMigrationWaived explicitly accepted without backfill (§4.7 允许豁免).
	FieldMigrationWaived KnowledgeFieldMigrationStatus = "waived"
)

// Valid reports whether the status is known.
func (s KnowledgeFieldMigrationStatus) Valid() bool {
	switch s {
	case FieldMigrationPending, FieldMigrationDone, FieldMigrationWaived:
		return true
	default:
		return false
	}
}

// DiffTemplateFields reports the structural changes between two field sets, so
// saving a template version can generate the matching migration tasks.
func DiffTemplateFields(previous, next []*KnowledgeField) []*KnowledgeFieldMigration {
	prevByKey := make(map[string]*KnowledgeField, len(previous))
	for _, field := range previous {
		if field != nil {
			prevByKey[field.FieldKey] = field
		}
	}
	nextByKey := make(map[string]*KnowledgeField, len(next))
	for _, field := range next {
		if field != nil {
			nextByKey[field.FieldKey] = field
		}
	}
	var changes []*KnowledgeFieldMigration
	for key, prev := range prevByKey {
		nextField, ok := nextByKey[key]
		if !ok {
			changes = append(changes, &KnowledgeFieldMigration{
				ChangeKind: "remove", FieldKey: key,
			})
			continue
		}
		if prev.FieldType != nextField.FieldType {
			changes = append(changes, &KnowledgeFieldMigration{
				ChangeKind: "retype", FieldKey: key, PreviousKey: prev.FieldType,
			})
		}
	}
	for key, nextField := range nextByKey {
		if _, ok := prevByKey[key]; ok {
			continue
		}
		changes = append(changes, &KnowledgeFieldMigration{
			ChangeKind: "add", FieldKey: key, Note: nextField.Label,
		})
	}
	return changes
}
