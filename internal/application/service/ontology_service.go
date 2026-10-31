package service

import (
	"context"
	"errors"
	"strings"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ontologyService implements interfaces.OntologyService: the schema
// governance rules (KnowAxis V3.0 §8) on top of the ontology tables.
// Hard rules:
//   - only draft schemas are editable or deletable; published rows are
//     immutable history;
//   - publish requires ≥1 type and every relation endpoint must resolve to a
//     type of the same schema (KAG 谓词模型);
//   - a draft can only be deleted when it holds no types;
//   - every mutation writes an ontology.* audit event.
type ontologyService struct {
	repo  interfaces.OntologyRepository
	audit interfaces.AuditLogService
}

// NewOntologyService constructs the ontology service.
func NewOntologyService(
	repo interfaces.OntologyRepository,
	audit interfaces.AuditLogService,
) interfaces.OntologyService {
	return &ontologyService{repo: repo, audit: audit}
}

// CreateSchema creates a draft schema (version 1) with a tenant-unique name.
func (s *ontologyService) CreateSchema(
	ctx context.Context, schema *types.OntologySchema, actorID string,
) (*types.OntologySchema, error) {
	if schema == nil || strings.TrimSpace(schema.Name) == "" {
		return nil, apperrors.NewBadRequestError("schema name is required")
	}
	schema.Name = strings.TrimSpace(schema.Name)
	if schema.Status == "" {
		schema.Status = types.OntologySchemaStatusDraft
	}
	if schema.Status != types.OntologySchemaStatusDraft {
		return nil, apperrors.NewBadRequestError("a new schema must start as draft")
	}
	if schema.Version <= 0 {
		schema.Version = 1
	}
	if schema.ID == "" {
		schema.ID = uuid.NewString()
	}
	if existing, err := s.repo.GetSchemaByName(ctx, schema.TenantID, schema.Name); err == nil && existing != nil {
		return nil, apperrors.NewBadRequestError("schema name already exists in this workspace")
	}
	if err := s.repo.CreateSchema(ctx, schema); err != nil {
		return nil, err
	}
	s.logAudit(ctx, schema.TenantID, actorID, types.OntologyActionSchemaCreated,
		"ontology_schema", schema.ID, map[string]interface{}{"name": schema.Name})
	return schema, nil
}

// UpdateSchema renames/describes a draft schema; published history is immutable.
func (s *ontologyService) UpdateSchema(
	ctx context.Context, tenantID uint64, schemaID string, input *types.OntologySchemaUpdate, actorID string,
) (*types.OntologySchema, error) {
	if input == nil {
		return nil, apperrors.NewBadRequestError("update body is required")
	}
	schema, err := s.loadSchema(ctx, tenantID, schemaID)
	if err != nil {
		return nil, err
	}
	if !schema.Status.Editable() {
		return nil, apperrors.NewBadRequestError("only draft schemas can be updated")
	}
	if input.Name != nil {
		newName := strings.TrimSpace(*input.Name)
		if newName == "" {
			return nil, apperrors.NewBadRequestError("schema name cannot be empty")
		}
		if newName != schema.Name {
			if existing, err := s.repo.GetSchemaByName(ctx, tenantID, newName); err == nil && existing != nil && existing.ID != schema.ID {
				return nil, apperrors.NewBadRequestError("schema name already exists in this workspace")
			}
			schema.Name = newName
		}
	}
	if input.Description != nil {
		schema.Description = *input.Description
	}
	if err := s.repo.UpdateSchema(ctx, schema); err != nil {
		return nil, err
	}
	s.logAudit(ctx, tenantID, actorID, types.OntologyActionSchemaUpdated,
		"ontology_schema", schema.ID, map[string]interface{}{"name": schema.Name})
	return schema, nil
}

// PublishSchema validates the draft structure then performs the atomic
// draft→published transition with a version bump.
func (s *ontologyService) PublishSchema(
	ctx context.Context, tenantID uint64, schemaID, actorID string,
) (*types.OntologySchema, error) {
	schema, err := s.loadSchema(ctx, tenantID, schemaID)
	if err != nil {
		return nil, err
	}
	if schema.Status != types.OntologySchemaStatusDraft {
		return nil, apperrors.NewBadRequestError("only draft schemas can be published")
	}
	// 发布门禁 1：至少 1 个类型（空 schema 发布出去毫无约束力）。
	typeList, err := s.repo.ListTypesBySchema(ctx, schemaID)
	if err != nil {
		return nil, err
	}
	if len(typeList) == 0 {
		return nil, apperrors.NewBadRequestError("schema must define at least one type before publishing")
	}
	// 发布门禁 2：每条 relation 的两端类型必须存在于本 schema。
	existingTypes := make(map[string]struct{}, len(typeList))
	for _, t := range typeList {
		existingTypes[t.ID] = struct{}{}
	}
	relations, err := s.repo.ListRelationsBySchema(ctx, schemaID)
	if err != nil {
		return nil, err
	}
	for _, rel := range relations {
		if _, ok := existingTypes[rel.SourceTypeID]; !ok {
			return nil, apperrors.NewBadRequestError(
				"relation " + rel.Name + " references an unknown source type")
		}
		if _, ok := existingTypes[rel.TargetTypeID]; !ok {
			return nil, apperrors.NewBadRequestError(
				"relation " + rel.Name + " references an unknown target type")
		}
	}
	published, err := s.repo.PublishSchema(ctx, tenantID, schemaID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 并发发布/删除竞态：重读以给出准确错误。
			return nil, apperrors.NewConflictError("schema is no longer a draft")
		}
		return nil, err
	}
	s.logAudit(ctx, tenantID, actorID, types.OntologyActionSchemaPublished,
		"ontology_schema", schemaID, map[string]interface{}{
			"name":      schema.Name,
			"version":   published.Version,
			"types":     len(typeList),
			"relations": len(relations),
		})
	return published, nil
}

// DeleteSchema deletes a draft schema that holds no types.
func (s *ontologyService) DeleteSchema(ctx context.Context, tenantID uint64, schemaID, actorID string) error {
	schema, err := s.loadSchema(ctx, tenantID, schemaID)
	if err != nil {
		return err
	}
	if !schema.Status.Editable() {
		return apperrors.NewBadRequestError("only draft schemas can be deleted")
	}
	count, err := s.repo.CountTypesBySchema(ctx, schemaID)
	if err != nil {
		return err
	}
	if count > 0 {
		return apperrors.NewBadRequestError("schema still has types; remove them first")
	}
	if err := s.repo.DeleteSchemaCascade(ctx, tenantID, schemaID); err != nil {
		return err
	}
	s.logAudit(ctx, tenantID, actorID, types.OntologyActionSchemaDeleted,
		"ontology_schema", schemaID, map[string]interface{}{"name": schema.Name})
	return nil
}

// ListSchemas returns the tenant's schemas (no aggregate).
func (s *ontologyService) ListSchemas(ctx context.Context, tenantID uint64) ([]*types.OntologySchema, error) {
	return s.repo.ListSchemas(ctx, tenantID)
}

// GetSchema returns the schema aggregated with types, per-type properties
// and relations.
func (s *ontologyService) GetSchema(ctx context.Context, tenantID uint64, schemaID string) (*types.OntologySchemaDetail, error) {
	schema, err := s.loadSchema(ctx, tenantID, schemaID)
	if err != nil {
		return nil, err
	}
	typeList, err := s.repo.ListTypesBySchema(ctx, schemaID)
	if err != nil {
		return nil, err
	}
	detail := &types.OntologySchemaDetail{
		Schema: schema,
		Types:  make([]*types.OntologyTypeDetail, 0, len(typeList)),
	}
	for _, t := range typeList {
		props, err := s.repo.ListPropertiesByType(ctx, t.ID)
		if err != nil {
			return nil, err
		}
		detail.Types = append(detail.Types, &types.OntologyTypeDetail{
			OntologyType: *t,
			Properties:   props,
		})
	}
	detail.Relations, err = s.repo.ListRelationsBySchema(ctx, schemaID)
	if err != nil {
		return nil, err
	}
	return detail, nil
}

// AddType adds a type to a draft schema. DisplayName defaults to the machine
// name; aliases/config JSON defaults are normalized.
func (s *ontologyService) AddType(
	ctx context.Context, tenantID uint64, t *types.OntologyType, actorID string,
) (*types.OntologyType, error) {
	if t == nil || strings.TrimSpace(t.Name) == "" {
		return nil, apperrors.NewBadRequestError("type name is required")
	}
	schema, err := s.loadSchema(ctx, tenantID, t.SchemaID)
	if err != nil {
		return nil, err
	}
	if !schema.Status.Editable() {
		return nil, apperrors.NewBadRequestError("types can only be added to draft schemas")
	}
	t.Name = strings.TrimSpace(t.Name)
	t.SchemaID = schema.ID
	if t.ID == "" {
		t.ID = uuid.NewString()
	}
	if strings.TrimSpace(t.DisplayName) == "" {
		t.DisplayName = t.Name
	}
	t.Aliases = normalizeOntologyJSON(t.Aliases, "[]")
	t.Config = normalizeOntologyJSON(t.Config, "{}")
	if err := s.repo.CreateType(ctx, t); err != nil {
		return nil, err
	}
	s.logAudit(ctx, tenantID, actorID, types.OntologyActionTypeCreated,
		"ontology_type", t.ID, map[string]interface{}{
			"schema_id": schema.ID, "name": t.Name,
		})
	return t, nil
}

// UpdateType updates a type whose owning schema is still a draft.
func (s *ontologyService) UpdateType(
	ctx context.Context, tenantID uint64, typeID string, input *types.OntologyTypeUpdate, actorID string,
) (*types.OntologyType, error) {
	if input == nil {
		return nil, apperrors.NewBadRequestError("update body is required")
	}
	t, err := s.loadTypeForEdit(ctx, tenantID, typeID)
	if err != nil {
		return nil, err
	}
	t.DisplayName = strings.TrimSpace(input.DisplayName)
	t.Description = input.Description
	t.Aliases = normalizeOntologyJSON(input.Aliases, "[]")
	t.Config = normalizeOntologyJSON(input.Config, "{}")
	if err := s.repo.UpdateType(ctx, t); err != nil {
		return nil, err
	}
	s.logAudit(ctx, tenantID, actorID, types.OntologyActionTypeUpdated,
		"ontology_type", t.ID, map[string]interface{}{"name": t.Name})
	return t, nil
}

// DeleteType removes a type and its properties from a draft schema.
func (s *ontologyService) DeleteType(ctx context.Context, tenantID uint64, typeID, actorID string) error {
	t, err := s.loadTypeForEdit(ctx, tenantID, typeID)
	if err != nil {
		return err
	}
	if err := s.repo.DeleteType(ctx, t.ID); err != nil {
		return err
	}
	s.logAudit(ctx, tenantID, actorID, types.OntologyActionTypeDeleted,
		"ontology_type", t.ID, map[string]interface{}{"name": t.Name})
	return nil
}

// AddProperty adds a property to a type of a draft schema.
func (s *ontologyService) AddProperty(
	ctx context.Context, tenantID uint64, p *types.OntologyProperty, actorID string,
) (*types.OntologyProperty, error) {
	if p == nil || strings.TrimSpace(p.Name) == "" {
		return nil, apperrors.NewBadRequestError("property name is required")
	}
	dataType := p.DataType
	if !dataType.Valid() {
		return nil, apperrors.NewBadRequestError("unknown property data type")
	}
	t, err := s.loadTypeForEdit(ctx, tenantID, p.TypeID)
	if err != nil {
		return nil, err
	}
	p.Name = strings.TrimSpace(p.Name)
	p.TypeID = t.ID
	p.DataType = dataType
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	p.Config = normalizeOntologyJSON(p.Config, "{}")
	if err := s.repo.CreateProperty(ctx, p); err != nil {
		return nil, err
	}
	s.logAudit(ctx, tenantID, actorID, types.OntologyActionPropertyCreated,
		"ontology_property", p.ID, map[string]interface{}{
			"type_id": t.ID, "name": p.Name, "data_type": string(p.DataType),
		})
	return p, nil
}

// UpdateProperty updates a property of a draft schema.
func (s *ontologyService) UpdateProperty(
	ctx context.Context, tenantID uint64, propertyID string, input *types.OntologyPropertyUpdate, actorID string,
) (*types.OntologyProperty, error) {
	if input == nil {
		return nil, apperrors.NewBadRequestError("update body is required")
	}
	p, err := s.repo.GetProperty(ctx, propertyID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperrors.NewNotFoundError("ontology property not found")
		}
		return nil, err
	}
	// 属主校验：property → type → schema（租户内）且 schema 仍为 draft。
	if _, err := s.loadTypeForEdit(ctx, tenantID, p.TypeID); err != nil {
		return nil, err
	}
	dataType := types.OntologyPropertyDataType(input.DataType)
	if !dataType.Valid() {
		return nil, apperrors.NewBadRequestError("unknown property data type")
	}
	p.DataType = dataType
	p.Required = input.Required
	p.Config = normalizeOntologyJSON(input.Config, "{}")
	if err := s.repo.UpdateProperty(ctx, p); err != nil {
		return nil, err
	}
	s.logAudit(ctx, tenantID, actorID, types.OntologyActionPropertyUpdated,
		"ontology_property", p.ID, map[string]interface{}{"name": p.Name})
	return p, nil
}

// DeleteProperty removes a property from a draft schema.
func (s *ontologyService) DeleteProperty(ctx context.Context, tenantID uint64, propertyID, actorID string) error {
	p, err := s.repo.GetProperty(ctx, propertyID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperrors.NewNotFoundError("ontology property not found")
		}
		return err
	}
	if _, err := s.loadTypeForEdit(ctx, tenantID, p.TypeID); err != nil {
		return err
	}
	if err := s.repo.DeleteProperty(ctx, p.ID); err != nil {
		return err
	}
	s.logAudit(ctx, tenantID, actorID, types.OntologyActionPropertyDeleted,
		"ontology_property", p.ID, map[string]interface{}{"name": p.Name})
	return nil
}

// AddRelation adds a relation to a draft schema; both endpoints must be
// types of the same schema.
func (s *ontologyService) AddRelation(
	ctx context.Context, tenantID uint64, rel *types.OntologyRelation, actorID string,
) (*types.OntologyRelation, error) {
	if rel == nil || strings.TrimSpace(rel.Name) == "" {
		return nil, apperrors.NewBadRequestError("relation name is required")
	}
	schema, err := s.loadSchema(ctx, tenantID, rel.SchemaID)
	if err != nil {
		return nil, err
	}
	if !schema.Status.Editable() {
		return nil, apperrors.NewBadRequestError("relations can only be added to draft schemas")
	}
	if err := s.validateRelationEndpoints(ctx, schema.ID, rel.SourceTypeID, rel.TargetTypeID); err != nil {
		return nil, err
	}
	cardinality := rel.Cardinality.Normalize()
	if !cardinality.Valid() {
		return nil, apperrors.NewBadRequestError("unknown relation cardinality")
	}
	rel.Name = strings.TrimSpace(rel.Name)
	rel.SchemaID = schema.ID
	rel.Cardinality = cardinality
	if rel.ID == "" {
		rel.ID = uuid.NewString()
	}
	if err := s.repo.CreateRelation(ctx, rel); err != nil {
		return nil, err
	}
	s.logAudit(ctx, tenantID, actorID, types.OntologyActionRelationCreated,
		"ontology_relation", rel.ID, map[string]interface{}{
			"schema_id": schema.ID, "name": rel.Name,
		})
	return rel, nil
}

// UpdateRelation updates a relation of a draft schema; changed endpoints are
// re-validated.
func (s *ontologyService) UpdateRelation(
	ctx context.Context, tenantID uint64, relationID string, input *types.OntologyRelationUpdate, actorID string,
) (*types.OntologyRelation, error) {
	if input == nil {
		return nil, apperrors.NewBadRequestError("update body is required")
	}
	rel, err := s.repo.GetRelation(ctx, relationID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperrors.NewNotFoundError("ontology relation not found")
		}
		return nil, err
	}
	schema, err := s.loadSchema(ctx, tenantID, rel.SchemaID)
	if err != nil {
		return nil, err
	}
	if !schema.Status.Editable() {
		return nil, apperrors.NewBadRequestError("only draft schemas can be modified")
	}
	if err := s.validateRelationEndpoints(ctx, schema.ID, input.SourceTypeID, input.TargetTypeID); err != nil {
		return nil, err
	}
	cardinality := types.OntologyRelationCardinality(input.Cardinality).Normalize()
	if !cardinality.Valid() {
		return nil, apperrors.NewBadRequestError("unknown relation cardinality")
	}
	rel.SourceTypeID = input.SourceTypeID
	rel.TargetTypeID = input.TargetTypeID
	rel.Cardinality = cardinality
	rel.Description = input.Description
	if err := s.repo.UpdateRelation(ctx, rel); err != nil {
		return nil, err
	}
	s.logAudit(ctx, tenantID, actorID, types.OntologyActionRelationUpdated,
		"ontology_relation", rel.ID, map[string]interface{}{"name": rel.Name})
	return rel, nil
}

// DeleteRelation removes a relation from a draft schema.
func (s *ontologyService) DeleteRelation(ctx context.Context, tenantID uint64, relationID, actorID string) error {
	rel, err := s.repo.GetRelation(ctx, relationID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperrors.NewNotFoundError("ontology relation not found")
		}
		return err
	}
	if _, err := s.schemaEditable(ctx, tenantID, rel.SchemaID); err != nil {
		return err
	}
	if err := s.repo.DeleteRelation(ctx, rel.ID); err != nil {
		return err
	}
	s.logAudit(ctx, tenantID, actorID, types.OntologyActionRelationDeleted,
		"ontology_relation", rel.ID, map[string]interface{}{"name": rel.Name})
	return nil
}

// loadSchema resolves a tenant-scoped schema, mapping not-found to a 404.
func (s *ontologyService) loadSchema(ctx context.Context, tenantID uint64, schemaID string) (*types.OntologySchema, error) {
	schema, err := s.repo.GetSchema(ctx, tenantID, schemaID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperrors.NewNotFoundError("ontology schema not found")
		}
		return nil, err
	}
	return schema, nil
}

// schemaEditable loads the tenant-scoped schema and asserts it is a draft.
func (s *ontologyService) schemaEditable(ctx context.Context, tenantID uint64, schemaID string) (*types.OntologySchema, error) {
	schema, err := s.loadSchema(ctx, tenantID, schemaID)
	if err != nil {
		return nil, err
	}
	if !schema.Status.Editable() {
		return nil, apperrors.NewBadRequestError("only draft schemas can be modified")
	}
	return schema, nil
}

// loadTypeForEdit resolves a type, verifies its owning schema belongs to the
// tenant and is still a draft. This is the tenant-scoping gate for every
// type/property mutation.
func (s *ontologyService) loadTypeForEdit(ctx context.Context, tenantID uint64, typeID string) (*types.OntologyType, error) {
	t, err := s.repo.GetType(ctx, typeID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperrors.NewNotFoundError("ontology type not found")
		}
		return nil, err
	}
	if _, err := s.schemaEditable(ctx, tenantID, t.SchemaID); err != nil {
		return nil, err
	}
	return t, nil
}

// validateRelationEndpoints asserts both endpoints are non-empty types of the
// given schema.
func (s *ontologyService) validateRelationEndpoints(ctx context.Context, schemaID, sourceTypeID, targetTypeID string) error {
	if sourceTypeID == "" || targetTypeID == "" {
		return apperrors.NewBadRequestError("relation source_type_id and target_type_id are required")
	}
	for _, endpoint := range []string{sourceTypeID, targetTypeID} {
		t, err := s.repo.GetType(ctx, endpoint)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperrors.NewBadRequestError("relation endpoint type does not exist in this schema")
			}
			return err
		}
		if t.SchemaID != schemaID {
			return apperrors.NewBadRequestError("relation endpoint type must belong to the same schema")
		}
	}
	return nil
}

// logAudit writes one ontology.* audit event; audit failures never fail the
// mutation (same policy as the ACL service).
func (s *ontologyService) logAudit(
	ctx context.Context, tenantID uint64, actorID string,
	action types.AuditAction, targetType, targetID string, details map[string]interface{},
) {
	if s.audit == nil {
		return
	}
	if err := s.audit.Log(ctx, &types.AuditLog{
		TenantID:    tenantID,
		ActorUserID: actorID,
		Action:      action,
		ScopeType:   "ontology",
		ScopeID:     targetID,
		TargetType:  targetType,
		TargetID:    targetID,
		Outcome:     "success",
		Details:     lifecycleJSON(details),
	}); err != nil {
		logger.Warnf(ctx, "ontology audit log failed (action=%s): %v", action, err)
	}
}

// normalizeOntologyJSON maps absent/null JSON payloads to the column default
// ('[]' for alias lists, '{}' for config objects).
func normalizeOntologyJSON(v types.JSON, fallback string) types.JSON {
	raw := strings.TrimSpace(string(v))
	if raw == "" || raw == "null" {
		return types.JSON(fallback)
	}
	return v
}
