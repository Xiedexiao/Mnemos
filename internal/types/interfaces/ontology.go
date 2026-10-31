package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// OntologyRepository persists the four ontology schema tables created by
// migration 000141 (KnowAxis V3.0 §8). Reads/writes are tenant-scoped at the
// schema level; types/properties/relations are addressed by ID after the
// caller resolved them through their owning (tenant-scoped) schema.
type OntologyRepository interface {
	// CreateSchema inserts one schema row (name is unique per tenant).
	CreateSchema(ctx context.Context, schema *types.OntologySchema) error
	// GetSchema returns one schema of the tenant.
	GetSchema(ctx context.Context, tenantID uint64, schemaID string) (*types.OntologySchema, error)
	// GetSchemaByName resolves a schema by its tenant-unique name.
	GetSchemaByName(ctx context.Context, tenantID uint64, name string) (*types.OntologySchema, error)
	// ListSchemas returns all schemas of the tenant, newest first.
	ListSchemas(ctx context.Context, tenantID uint64) ([]*types.OntologySchema, error)
	// UpdateSchema persists the mutable fields (name/description) of a draft.
	UpdateSchema(ctx context.Context, schema *types.OntologySchema) error
	// PublishSchema performs the atomic draft→published transition and bumps
	// version (version = version + 1) in one UPDATE; it fails when the row is
	// missing or no longer a draft.
	PublishSchema(ctx context.Context, tenantID uint64, schemaID string) (*types.OntologySchema, error)
	// DeleteSchemaCascade removes the schema together with its relations,
	// types and properties in one transaction. Callers must have verified the
	// schema is an empty draft.
	DeleteSchemaCascade(ctx context.Context, tenantID uint64, schemaID string) error

	// CreateType inserts one type (name unique per schema).
	CreateType(ctx context.Context, t *types.OntologyType) error
	// GetType returns one type by ID.
	GetType(ctx context.Context, typeID string) (*types.OntologyType, error)
	// ListTypesBySchema returns the schema's types, oldest first.
	ListTypesBySchema(ctx context.Context, schemaID string) ([]*types.OntologyType, error)
	// CountTypesBySchema counts the schema's types.
	CountTypesBySchema(ctx context.Context, schemaID string) (int64, error)
	// UpdateType persists the mutable fields of one type.
	UpdateType(ctx context.Context, t *types.OntologyType) error
	// DeleteType removes the type and its properties in one transaction.
	DeleteType(ctx context.Context, typeID string) error

	// CreateProperty inserts one property (name unique per type).
	CreateProperty(ctx context.Context, p *types.OntologyProperty) error
	// GetProperty returns one property by ID.
	GetProperty(ctx context.Context, propertyID string) (*types.OntologyProperty, error)
	// ListPropertiesByType returns the type's properties, oldest first.
	ListPropertiesByType(ctx context.Context, typeID string) ([]*types.OntologyProperty, error)
	// UpdateProperty persists the mutable fields of one property.
	UpdateProperty(ctx context.Context, p *types.OntologyProperty) error
	// DeleteProperty removes one property row.
	DeleteProperty(ctx context.Context, propertyID string) error

	// CreateRelation inserts one relation (name unique per schema).
	CreateRelation(ctx context.Context, rel *types.OntologyRelation) error
	// GetRelation returns one relation by ID.
	GetRelation(ctx context.Context, relationID string) (*types.OntologyRelation, error)
	// ListRelationsBySchema returns the schema's relations, oldest first.
	ListRelationsBySchema(ctx context.Context, schemaID string) ([]*types.OntologyRelation, error)
	// UpdateRelation persists the mutable fields of one relation.
	UpdateRelation(ctx context.Context, rel *types.OntologyRelation) error
	// DeleteRelation removes one relation row.
	DeleteRelation(ctx context.Context, relationID string) error
}

// OntologyService implements the schema governance rules on top of
// OntologyRepository: only drafts are editable, publish requires at least one
// type and referentially intact relations, and every mutation is audited
// (actions prefixed ontology.*, defined in types/ontology.go).
type OntologyService interface {
	// CreateSchema creates a draft schema (version 1).
	CreateSchema(ctx context.Context, schema *types.OntologySchema, actorID string) (*types.OntologySchema, error)
	// UpdateSchema renames/describes a draft schema.
	UpdateSchema(ctx context.Context, tenantID uint64, schemaID string, input *types.OntologySchemaUpdate, actorID string) (*types.OntologySchema, error)
	// PublishSchema validates the draft (≥1 type, relation endpoints exist),
	// then flips draft→published and bumps version.
	PublishSchema(ctx context.Context, tenantID uint64, schemaID, actorID string) (*types.OntologySchema, error)
	// DeleteSchema deletes an empty draft schema (no types).
	DeleteSchema(ctx context.Context, tenantID uint64, schemaID, actorID string) error
	// ListSchemas returns the tenant's schemas.
	ListSchemas(ctx context.Context, tenantID uint64) ([]*types.OntologySchema, error)
	// GetSchema returns the schema aggregated with its types, per-type
	// properties and relations.
	GetSchema(ctx context.Context, tenantID uint64, schemaID string) (*types.OntologySchemaDetail, error)

	// AddType adds a type to a draft schema.
	AddType(ctx context.Context, tenantID uint64, t *types.OntologyType, actorID string) (*types.OntologyType, error)
	// UpdateType updates a type whose owning schema is still a draft.
	UpdateType(ctx context.Context, tenantID uint64, typeID string, input *types.OntologyTypeUpdate, actorID string) (*types.OntologyType, error)
	// DeleteType removes a type (and its properties) from a draft schema.
	DeleteType(ctx context.Context, tenantID uint64, typeID, actorID string) error

	// AddProperty adds a property to a type of a draft schema.
	AddProperty(ctx context.Context, tenantID uint64, p *types.OntologyProperty, actorID string) (*types.OntologyProperty, error)
	// UpdateProperty updates a property of a draft schema.
	UpdateProperty(ctx context.Context, tenantID uint64, propertyID string, input *types.OntologyPropertyUpdate, actorID string) (*types.OntologyProperty, error)
	// DeleteProperty removes a property from a draft schema.
	DeleteProperty(ctx context.Context, tenantID uint64, propertyID, actorID string) error

	// AddRelation adds a relation (both endpoints must exist) to a draft schema.
	AddRelation(ctx context.Context, tenantID uint64, rel *types.OntologyRelation, actorID string) (*types.OntologyRelation, error)
	// UpdateRelation updates a relation of a draft schema.
	UpdateRelation(ctx context.Context, tenantID uint64, relationID string, input *types.OntologyRelationUpdate, actorID string) (*types.OntologyRelation, error)
	// DeleteRelation removes a relation from a draft schema.
	DeleteRelation(ctx context.Context, tenantID uint64, relationID, actorID string) error
}
