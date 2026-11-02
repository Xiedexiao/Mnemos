package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// KnowledgeTemplateRepository persists knowledge templates and their field
// definitions (KnowAxis V3.0 §3.2 / §4.7).
type KnowledgeTemplateRepository interface {
	// CreateVersion inserts a new template version plus its fields atomically.
	CreateVersion(
		ctx context.Context, template *types.KnowledgeTemplate, fields []*types.KnowledgeField,
	) (*types.KnowledgeTemplate, error)
	// GetByID returns one template version with its fields.
	GetByID(ctx context.Context, tenantID uint64, templateID string) (*types.KnowledgeTemplate, error)
	// GetActiveByKey returns the active version of a template key.
	GetActiveByKey(
		ctx context.Context, tenantID uint64, templateKey string,
	) (*types.KnowledgeTemplate, error)
	// List returns every active template of a tenant.
	List(ctx context.Context, tenantID uint64, limit int) ([]*types.KnowledgeTemplate, error)
	// ListFields returns a template version's fields in display order.
	ListFields(ctx context.Context, templateID string) ([]*types.KnowledgeField, error)
	// CreateMigration records one structural field change task.
	CreateMigration(ctx context.Context, migration *types.KnowledgeFieldMigration) error
	// ListMigrations returns the migration tasks of a tenant.
	ListMigrations(
		ctx context.Context, tenantID uint64, status types.KnowledgeFieldMigrationStatus, limit int,
	) ([]*types.KnowledgeFieldMigration, error)
	// UpdateMigrationStatus advances one migration task.
	UpdateMigrationStatus(
		ctx context.Context, tenantID uint64, migrationID string,
		status types.KnowledgeFieldMigrationStatus, note string,
	) (*types.KnowledgeFieldMigration, error)
	// CountKnowledgeBySpace counts entries in a space (migration sizing).
	CountKnowledgeBySpace(ctx context.Context, tenantID uint64, kbID string) (int64, error)
}

// KnowledgeTemplateService is the template-center surface: versioned templates,
// field validation and the structural-change migration tasks.
type KnowledgeTemplateService interface {
	// Save creates a new version of a template (or its first version) and
	// generates migration tasks for structural field changes.
	Save(
		ctx context.Context, tenantID uint64, template *types.KnowledgeTemplate,
		fields []*types.KnowledgeField, actorID, kbID string,
	) (*types.KnowledgeTemplate, error)
	// Get returns one template version.
	Get(ctx context.Context, tenantID uint64, templateID string) (*types.KnowledgeTemplate, error)
	// GetByKey returns a template key's active version.
	GetByKey(ctx context.Context, tenantID uint64, templateKey string) (*types.KnowledgeTemplate, error)
	// List returns the tenant's active templates.
	List(ctx context.Context, tenantID uint64, limit int) ([]*types.KnowledgeTemplate, error)
	// Validate checks values against a template version's fields.
	Validate(
		ctx context.Context, tenantID uint64, templateKey string, values map[string]interface{},
	) (map[string]interface{}, []types.FieldValidationError, error)
	// ListMigrations returns the field-migration task list.
	ListMigrations(
		ctx context.Context, tenantID uint64, status types.KnowledgeFieldMigrationStatus, limit int,
	) ([]*types.KnowledgeFieldMigration, error)
	// ResolveMigration closes or waives one migration task.
	ResolveMigration(
		ctx context.Context, tenantID uint64, migrationID string,
		status types.KnowledgeFieldMigrationStatus, note string,
	) (*types.KnowledgeFieldMigration, error)
}
