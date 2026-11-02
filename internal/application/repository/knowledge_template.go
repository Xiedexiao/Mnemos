package repository

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// ErrKnowledgeTemplateNotFound is returned when a template lookup misses.
var ErrKnowledgeTemplateNotFound = errors.New("knowledge template not found")

// knowledgeTemplateRepository implements interfaces.KnowledgeTemplateRepository.
type knowledgeTemplateRepository struct {
	db *gorm.DB
}

// NewKnowledgeTemplateRepository constructs the template repository.
func NewKnowledgeTemplateRepository(db *gorm.DB) interfaces.KnowledgeTemplateRepository {
	return &knowledgeTemplateRepository{db: db}
}

// CreateVersion inserts a new template version plus its fields atomically.
//
// The version number is computed inside the transaction so two concurrent saves
// cannot both claim the same version (which the unique constraint would reject,
// but with an opaque error).
func (r *knowledgeTemplateRepository) CreateVersion(
	ctx context.Context, template *types.KnowledgeTemplate, fields []*types.KnowledgeField,
) (*types.KnowledgeTemplate, error) {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var latest int
		if err := tx.Model(&types.KnowledgeTemplate{}).
			Where("tenant_id = ? AND template_key = ?", template.TenantID, template.TemplateKey).
			Select("COALESCE(MAX(version), 0)").
			Scan(&latest).Error; err != nil {
			return err
		}
		template.Version = latest + 1
		template.Active = true
		// 上一版置为 inactive：新录入用最新版，历史知识仍按旧版解释。
		if err := tx.Model(&types.KnowledgeTemplate{}).
			Where("tenant_id = ? AND template_key = ? AND active = ?",
				template.TenantID, template.TemplateKey, true).
			Update("active", false).Error; err != nil {
			return err
		}
		if err := tx.Create(template).Error; err != nil {
			return err
		}
		for i, field := range fields {
			if field == nil {
				continue
			}
			field.TenantID = template.TenantID
			field.TemplateID = template.ID
			if field.SortOrder == 0 {
				field.SortOrder = i + 1
			}
			if len(field.Rules) == 0 {
				field.Rules = types.JSON("{}")
			}
			if err := tx.Create(field).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return r.GetByID(ctx, template.TenantID, template.ID)
}

// GetByID returns one template version with its fields.
func (r *knowledgeTemplateRepository) GetByID(
	ctx context.Context, tenantID uint64, templateID string,
) (*types.KnowledgeTemplate, error) {
	var template types.KnowledgeTemplate
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, templateID).
		First(&template).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrKnowledgeTemplateNotFound
	}
	if err != nil {
		return nil, err
	}
	fields, err := r.ListFields(ctx, templateID)
	if err != nil {
		return nil, err
	}
	template.Fields = fields
	return &template, nil
}

// GetActiveByKey returns the active version of a template key.
func (r *knowledgeTemplateRepository) GetActiveByKey(
	ctx context.Context, tenantID uint64, templateKey string,
) (*types.KnowledgeTemplate, error) {
	var template types.KnowledgeTemplate
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND template_key = ? AND active = ?", tenantID, templateKey, true).
		Order("version DESC").
		First(&template).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrKnowledgeTemplateNotFound
	}
	if err != nil {
		return nil, err
	}
	fields, err := r.ListFields(ctx, template.ID)
	if err != nil {
		return nil, err
	}
	template.Fields = fields
	return &template, nil
}

// List returns every active template of a tenant.
func (r *knowledgeTemplateRepository) List(
	ctx context.Context, tenantID uint64, limit int,
) ([]*types.KnowledgeTemplate, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	var templates []*types.KnowledgeTemplate
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND active = ?", tenantID, true).
		Order("template_key ASC").
		Limit(limit).
		Find(&templates).Error
	return templates, err
}

// ListFields returns a template version's fields in display order.
func (r *knowledgeTemplateRepository) ListFields(
	ctx context.Context, templateID string,
) ([]*types.KnowledgeField, error) {
	var fields []*types.KnowledgeField
	err := r.db.WithContext(ctx).
		Where("template_id = ?", templateID).
		Order("sort_order ASC").
		Find(&fields).Error
	return fields, err
}

// CreateMigration records one structural field change task.
func (r *knowledgeTemplateRepository) CreateMigration(
	ctx context.Context, migration *types.KnowledgeFieldMigration,
) error {
	return r.db.WithContext(ctx).Create(migration).Error
}

// ListMigrations returns the migration tasks of a tenant.
func (r *knowledgeTemplateRepository) ListMigrations(
	ctx context.Context, tenantID uint64, status types.KnowledgeFieldMigrationStatus, limit int,
) ([]*types.KnowledgeFieldMigration, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	query := r.db.WithContext(ctx).Where("tenant_id = ?", tenantID)
	if status != "" {
		query = query.Where("status = ?", string(status))
	}
	var rows []*types.KnowledgeFieldMigration
	err := query.Order("created_at DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

// UpdateMigrationStatus advances one migration task.
func (r *knowledgeTemplateRepository) UpdateMigrationStatus(
	ctx context.Context, tenantID uint64, migrationID string,
	status types.KnowledgeFieldMigrationStatus, note string,
) (*types.KnowledgeFieldMigration, error) {
	updates := map[string]interface{}{"status": string(status), "updated_at": gorm.Expr("NOW()")}
	if note != "" {
		updates["note"] = note
	}
	if err := r.db.WithContext(ctx).
		Model(&types.KnowledgeFieldMigration{}).
		Where("tenant_id = ? AND id = ?", tenantID, migrationID).
		Updates(updates).Error; err != nil {
		return nil, err
	}
	var row types.KnowledgeFieldMigration
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, migrationID).
		First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// CountKnowledgeByKnowledgeBase counts entries in a space, used to size the
// migration task at creation time.
func (r *knowledgeTemplateRepository) CountKnowledgeBySpace(
	ctx context.Context, tenantID uint64, kbID string,
) (int64, error) {
	var count int64
	query := r.db.WithContext(ctx).Model(&types.Knowledge{}).Where("tenant_id = ?", tenantID)
	if kbID != "" {
		query = query.Where("knowledge_base_id = ?", kbID)
	}
	err := query.Count(&count).Error
	return count, err
}
