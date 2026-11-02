package service

import (
	"context"
	"regexp"
	"strings"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// knowledgeTemplateService implements interfaces.KnowledgeTemplateService
// (KnowAxis V3.0 §3.2 人工录入 / §4.7 模板与字段治理).
type knowledgeTemplateService struct {
	repo interfaces.KnowledgeTemplateRepository
}

// NewKnowledgeTemplateService constructs the template service.
func NewKnowledgeTemplateService(
	repo interfaces.KnowledgeTemplateRepository,
) interfaces.KnowledgeTemplateService {
	return &knowledgeTemplateService{repo: repo}
}

// Save creates a new template version and records the structural changes as
// migration tasks (§4.7 字段新增/删除/改名/改类型需审批，生成迁移任务).
//
// A template is versioned rather than mutated because historical knowledge was
// authored against the old schema: rewriting a definition in place would
// silently reinterpret every existing entry.
func (s *knowledgeTemplateService) Save(
	ctx context.Context, tenantID uint64, template *types.KnowledgeTemplate,
	fields []*types.KnowledgeField, actorID, kbID string,
) (*types.KnowledgeTemplate, error) {
	if template == nil {
		return nil, apperrors.NewBadRequestError("template is required")
	}
	template.TemplateKey = strings.TrimSpace(template.TemplateKey)
	template.Name = strings.TrimSpace(template.Name)
	if template.TemplateKey == "" || template.Name == "" {
		return nil, apperrors.NewBadRequestError("template_key and name are required")
	}
	if len(fields) == 0 {
		return nil, apperrors.NewBadRequestError("a template needs at least one field")
	}
	if err := validateFieldDefinitions(fields); err != nil {
		return nil, err
	}

	// 上一版字段用于 diff；没有历史版本时（首次创建）不产生迁移任务。
	var previous []*types.KnowledgeField
	if existing, err := s.repo.GetActiveByKey(ctx, tenantID, template.TemplateKey); err == nil && existing != nil {
		previous = existing.Fields
	}

	template.TenantID = tenantID
	template.CreatedBy = actorID
	saved, err := s.repo.CreateVersion(ctx, template, fields)
	if err != nil {
		return nil, err
	}

	if len(previous) > 0 {
		s.recordFieldMigrations(ctx, tenantID, saved.ID, previous, fields, actorID, kbID)
	}
	return saved, nil
}

// recordFieldMigrations turns structural diffs into operator tasks.
//
// A failure here is logged, not returned: the template version is already
// published and rejecting it would leave the caller unable to proceed, while
// the migration list is a work queue that can be regenerated.
func (s *knowledgeTemplateService) recordFieldMigrations(
	ctx context.Context, tenantID uint64, templateID string,
	previous, next []*types.KnowledgeField, actorID, kbID string,
) {
	changes := types.DiffTemplateFields(previous, next)
	if len(changes) == 0 {
		return
	}
	affected, err := s.repo.CountKnowledgeBySpace(ctx, tenantID, kbID)
	if err != nil {
		logger.Warnf(ctx, "knowledge template: count affected knowledge failed: %v", err)
	}
	for _, change := range changes {
		change.TenantID = tenantID
		change.TemplateID = templateID
		change.AffectedCount = int(affected)
		change.CreatedBy = actorID
		if err := s.repo.CreateMigration(ctx, change); err != nil {
			logger.Warnf(ctx, "knowledge template: record field migration failed: %v", err)
		}
	}
}

// validateFieldDefinitions rejects definitions that cannot be enforced later.
func validateFieldDefinitions(fields []*types.KnowledgeField) error {
	seen := make(map[string]bool, len(fields))
	for _, field := range fields {
		if field == nil {
			continue
		}
		field.FieldKey = strings.TrimSpace(field.FieldKey)
		field.Label = strings.TrimSpace(field.Label)
		if field.FieldKey == "" || field.Label == "" {
			return apperrors.NewBadRequestError("every field needs a key and a label")
		}
		if seen[field.FieldKey] {
			return apperrors.NewBadRequestError("duplicate field key: " + field.FieldKey)
		}
		seen[field.FieldKey] = true
		fieldType := types.KnowledgeFieldType(field.FieldType)
		if field.FieldType == "" {
			field.FieldType = string(types.FieldTypeText)
			fieldType = types.FieldTypeText
		}
		if !fieldType.Valid() {
			return apperrors.NewBadRequestError("unknown field type: " + field.FieldType)
		}
		if field.Rules != nil {
			rules := field.ParsedRules()
			// 无效正则在提交期拒绝：否则每次录入都会静默跳过校验。
			if rules.Pattern != "" {
				if _, err := regexp.Compile(rules.Pattern); err != nil {
					return apperrors.NewBadRequestError(
						"field " + field.FieldKey + " has an invalid pattern")
				}
			}
			if (fieldType == types.FieldTypeSelect || fieldType == types.FieldTypeMultiselect) &&
				len(rules.Options) == 0 {
				return apperrors.NewBadRequestError(
					"select field " + field.FieldKey + " needs options")
			}
		}
	}
	return nil
}

// Get returns one template version.
func (s *knowledgeTemplateService) Get(
	ctx context.Context, tenantID uint64, templateID string,
) (*types.KnowledgeTemplate, error) {
	if templateID == "" {
		return nil, apperrors.NewBadRequestError("template_id is required")
	}
	return s.repo.GetByID(ctx, tenantID, templateID)
}

// GetByKey returns a template key's active version.
func (s *knowledgeTemplateService) GetByKey(
	ctx context.Context, tenantID uint64, templateKey string,
) (*types.KnowledgeTemplate, error) {
	if strings.TrimSpace(templateKey) == "" {
		return nil, apperrors.NewBadRequestError("template_key is required")
	}
	return s.repo.GetActiveByKey(ctx, tenantID, templateKey)
}

// List returns the tenant's active templates.
func (s *knowledgeTemplateService) List(
	ctx context.Context, tenantID uint64, limit int,
) ([]*types.KnowledgeTemplate, error) {
	templates, err := s.repo.List(ctx, tenantID, limit)
	if err != nil {
		return nil, err
	}
	// 列表带上字段定义，前端才能直接渲染录入表单（无需 N+1 次请求）。
	for _, template := range templates {
		if template == nil {
			continue
		}
		if fields, ferr := s.repo.ListFields(ctx, template.ID); ferr == nil {
			template.Fields = fields
		}
	}
	return templates, nil
}

// Validate applies the template's defaults then checks every value.
func (s *knowledgeTemplateService) Validate(
	ctx context.Context, tenantID uint64, templateKey string, values map[string]interface{},
) (map[string]interface{}, []types.FieldValidationError, error) {
	template, err := s.repo.GetActiveByKey(ctx, tenantID, templateKey)
	if err != nil {
		return values, nil, err
	}
	filled := types.ApplyFieldDefaults(template.Fields, values)
	problems := types.ValidateFieldValues(template.Fields, filled)
	return filled, problems, nil
}

// ListMigrations returns the field-migration task list.
func (s *knowledgeTemplateService) ListMigrations(
	ctx context.Context, tenantID uint64, status types.KnowledgeFieldMigrationStatus, limit int,
) ([]*types.KnowledgeFieldMigration, error) {
	if status != "" && !status.Valid() {
		return nil, apperrors.NewBadRequestError("unknown migration status")
	}
	return s.repo.ListMigrations(ctx, tenantID, status, limit)
}

// ResolveMigration closes or waives one migration task.
func (s *knowledgeTemplateService) ResolveMigration(
	ctx context.Context, tenantID uint64, migrationID string,
	status types.KnowledgeFieldMigrationStatus, note string,
) (*types.KnowledgeFieldMigration, error) {
	if migrationID == "" {
		return nil, apperrors.NewBadRequestError("migration_id is required")
	}
	// pending 不是"处置"结果：要处置只能用 done/waived。
	if status != types.FieldMigrationDone && status != types.FieldMigrationWaived {
		return nil, apperrors.NewBadRequestError("status must be done or waived")
	}
	return s.repo.UpdateMigrationStatus(ctx, tenantID, migrationID, status, note)
}
