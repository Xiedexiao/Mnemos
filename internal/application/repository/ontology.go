package repository

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

// ontologyRepository implements interfaces.OntologyRepository on the four
// ontology tables created by migration 000141. All schema reads/writes are
// tenant-scoped; sub-resources are addressed by ID after the service resolved
// them through their owning schema.
type ontologyRepository struct {
	db *gorm.DB
}

// NewOntologyRepository constructs the ontology repository.
func NewOntologyRepository(db *gorm.DB) interfaces.OntologyRepository {
	return &ontologyRepository{db: db}
}

// CreateSchema inserts one schema row (UNIQUE(tenant_id, name) guards dupes).
func (r *ontologyRepository) CreateSchema(ctx context.Context, schema *types.OntologySchema) error {
	return r.db.WithContext(ctx).Create(schema).Error
}

// GetSchema returns one tenant-scoped schema.
func (r *ontologyRepository) GetSchema(ctx context.Context, tenantID uint64, schemaID string) (*types.OntologySchema, error) {
	var schema types.OntologySchema
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, schemaID).
		First(&schema).Error; err != nil {
		return nil, err
	}
	return &schema, nil
}

// GetSchemaByName resolves a schema by its tenant-unique name.
func (r *ontologyRepository) GetSchemaByName(ctx context.Context, tenantID uint64, name string) (*types.OntologySchema, error) {
	var schema types.OntologySchema
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND name = ?", tenantID, name).
		First(&schema).Error; err != nil {
		return nil, err
	}
	return &schema, nil
}

// ListSchemas returns all schemas of the tenant, newest first.
func (r *ontologyRepository) ListSchemas(ctx context.Context, tenantID uint64) ([]*types.OntologySchema, error) {
	var schemas []*types.OntologySchema
	err := r.db.WithContext(ctx).
		Where("tenant_id = ?", tenantID).
		Order("created_at DESC").
		Find(&schemas).Error
	return schemas, err
}

// UpdateSchema persists the mutable fields of a draft schema.
func (r *ontologyRepository) UpdateSchema(ctx context.Context, schema *types.OntologySchema) error {
	return r.db.WithContext(ctx).
		Model(schema).
		Select("name", "description", "updated_at").
		Updates(schema).Error
}

// PublishSchema performs the atomic draft→published transition and bumps the
// version in one UPDATE. Zero rows affected means the row is gone or no
// longer a draft (concurrent publish); surfaced as ErrRecordNotFound.
func (r *ontologyRepository) PublishSchema(ctx context.Context, tenantID uint64, schemaID string) (*types.OntologySchema, error) {
	result := r.db.WithContext(ctx).
		Model(&types.OntologySchema{}).
		Where("tenant_id = ? AND id = ? AND status = ?",
			tenantID, schemaID, string(types.OntologySchemaStatusDraft)).
		Updates(map[string]interface{}{
			"status":     string(types.OntologySchemaStatusPublished),
			"version":    gorm.Expr("version + 1"),
			"updated_at": time.Now(),
		})
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return r.GetSchema(ctx, tenantID, schemaID)
}

// DeleteSchemaCascade removes the schema and every row hanging off it in one
// transaction (relations, then properties, then types, then the schema).
func (r *ontologyRepository) DeleteSchemaCascade(ctx context.Context, tenantID uint64, schemaID string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("schema_id = ?", schemaID).
			Delete(&types.OntologyRelation{}).Error; err != nil {
			return err
		}
		if err := tx.Where("type_id IN (?)",
			tx.Model(&types.OntologyType{}).Select("id").Where("schema_id = ?", schemaID),
		).Delete(&types.OntologyProperty{}).Error; err != nil {
			return err
		}
		if err := tx.Where("schema_id = ?", schemaID).
			Delete(&types.OntologyType{}).Error; err != nil {
			return err
		}
		return tx.Where("tenant_id = ? AND id = ?", tenantID, schemaID).
			Delete(&types.OntologySchema{}).Error
	})
}

// CreateType inserts one type row (UNIQUE(schema_id, name) guards dupes).
func (r *ontologyRepository) CreateType(ctx context.Context, t *types.OntologyType) error {
	return r.db.WithContext(ctx).Create(t).Error
}

// GetType returns one type by ID.
func (r *ontologyRepository) GetType(ctx context.Context, typeID string) (*types.OntologyType, error) {
	var t types.OntologyType
	if err := r.db.WithContext(ctx).
		Where("id = ?", typeID).
		First(&t).Error; err != nil {
		return nil, err
	}
	return &t, nil
}

// ListTypesBySchema returns the schema's types, oldest first (stable order
// for the aggregated read).
func (r *ontologyRepository) ListTypesBySchema(ctx context.Context, schemaID string) ([]*types.OntologyType, error) {
	var items []*types.OntologyType
	err := r.db.WithContext(ctx).
		Where("schema_id = ?", schemaID).
		Order("created_at ASC").
		Find(&items).Error
	return items, err
}

// CountTypesBySchema counts the schema's types.
func (r *ontologyRepository) CountTypesBySchema(ctx context.Context, schemaID string) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&types.OntologyType{}).
		Where("schema_id = ?", schemaID).
		Count(&count).Error
	return count, err
}

// UpdateType persists the mutable fields of one type.
func (r *ontologyRepository) UpdateType(ctx context.Context, t *types.OntologyType) error {
	return r.db.WithContext(ctx).
		Model(t).
		Select("display_name", "description", "aliases", "config", "updated_at").
		Updates(t).Error
}

// DeleteType removes the type and its properties in one transaction.
func (r *ontologyRepository) DeleteType(ctx context.Context, typeID string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("type_id = ?", typeID).
			Delete(&types.OntologyProperty{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", typeID).Delete(&types.OntologyType{}).Error
	})
}

// CreateProperty inserts one property row (UNIQUE(type_id, name) guards dupes).
func (r *ontologyRepository) CreateProperty(ctx context.Context, p *types.OntologyProperty) error {
	return r.db.WithContext(ctx).Create(p).Error
}

// GetProperty returns one property by ID.
func (r *ontologyRepository) GetProperty(ctx context.Context, propertyID string) (*types.OntologyProperty, error) {
	var p types.OntologyProperty
	if err := r.db.WithContext(ctx).
		Where("id = ?", propertyID).
		First(&p).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

// ListPropertiesByType returns the type's properties, oldest first.
func (r *ontologyRepository) ListPropertiesByType(ctx context.Context, typeID string) ([]*types.OntologyProperty, error) {
	var items []*types.OntologyProperty
	err := r.db.WithContext(ctx).
		Where("type_id = ?", typeID).
		Order("created_at ASC").
		Find(&items).Error
	return items, err
}

// UpdateProperty persists the mutable fields of one property.
func (r *ontologyRepository) UpdateProperty(ctx context.Context, p *types.OntologyProperty) error {
	return r.db.WithContext(ctx).
		Model(p).
		Select("data_type", "required", "config").
		Updates(p).Error
}

// DeleteProperty removes one property row.
func (r *ontologyRepository) DeleteProperty(ctx context.Context, propertyID string) error {
	return r.db.WithContext(ctx).
		Where("id = ?", propertyID).
		Delete(&types.OntologyProperty{}).Error
}

// CreateRelation inserts one relation row (UNIQUE(schema_id, name) guards dupes).
func (r *ontologyRepository) CreateRelation(ctx context.Context, rel *types.OntologyRelation) error {
	return r.db.WithContext(ctx).Create(rel).Error
}

// GetRelation returns one relation by ID.
func (r *ontologyRepository) GetRelation(ctx context.Context, relationID string) (*types.OntologyRelation, error) {
	var rel types.OntologyRelation
	if err := r.db.WithContext(ctx).
		Where("id = ?", relationID).
		First(&rel).Error; err != nil {
		return nil, err
	}
	return &rel, nil
}

// ListRelationsBySchema returns the schema's relations, oldest first.
func (r *ontologyRepository) ListRelationsBySchema(ctx context.Context, schemaID string) ([]*types.OntologyRelation, error) {
	var items []*types.OntologyRelation
	err := r.db.WithContext(ctx).
		Where("schema_id = ?", schemaID).
		Order("created_at ASC").
		Find(&items).Error
	return items, err
}

// UpdateRelation persists the mutable fields of one relation.
func (r *ontologyRepository) UpdateRelation(ctx context.Context, rel *types.OntologyRelation) error {
	return r.db.WithContext(ctx).
		Model(rel).
		Select("source_type_id", "target_type_id", "cardinality", "description").
		Updates(rel).Error
}

// DeleteRelation removes one relation row.
func (r *ontologyRepository) DeleteRelation(ctx context.Context, relationID string) error {
	return r.db.WithContext(ctx).
		Where("id = ?", relationID).
		Delete(&types.OntologyRelation{}).Error
}
