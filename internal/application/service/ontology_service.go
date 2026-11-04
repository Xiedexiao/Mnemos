package service

import (
	"context"
	"errors"
	"strings"
	"time"

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
	// goldenSets backs the §8.2 L4 requirement: a change to a critical
	// ontology must pass a golden-set regression before it is published.
	// Optional: nil means the deployment has no quality gate, which is the
	// correct reading for installs that never created a golden set.
	goldenSets interfaces.GoldenSetService
	// governance records approval receipts for the L2+ dual-approval rule.
	// Optional: nil skips that gate (an install without an approval engine
	// cannot require two approvers).
	governance interfaces.GovernanceApprovalService
	now        func() time.Time
}

// NewOntologyService constructs the ontology service.
func NewOntologyService(
	repo interfaces.OntologyRepository,
	audit interfaces.AuditLogService,
	goldenSets interfaces.GoldenSetService,
	governance interfaces.GovernanceApprovalService,
) interfaces.OntologyService {
	return &ontologyService{
		repo: repo, audit: audit, goldenSets: goldenSets,
		governance: governance, now: time.Now,
	}
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
	// 治理分级（§8.2）：请求值被提升到领域下限（价格/合同 ≥ L2，图纸/HR ≥ L3），
	// 但绝不被降低——下限是"只能抬高"的约束。
	requested := types.ParseOntologyGovernanceLevel(schema.GovernanceLevel)
	schema.GovernanceLevel = string(types.EffectiveOntologyGovernanceLevel(requested, schema.Domain))
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
	if input.Domain != nil {
		schema.Domain = strings.TrimSpace(*input.Domain)
	}
	if input.GovernanceLevel != nil {
		requested := types.ParseOntologyGovernanceLevel(*input.GovernanceLevel)
		effective := types.EffectiveOntologyGovernanceLevel(requested, schema.Domain)
		// 降级需要显式拒绝：一次"手滑改回 L1"会让强治理域的产出直接进入
		// 问答，而这正是 §8.2 要阻止的。
		current := types.ParseOntologyGovernanceLevel(schema.GovernanceLevel)
		if effective.Rank() < current.Rank() {
			return nil, apperrors.NewBadRequestError(
				"ontology governance level cannot be lowered (current " + string(current) + ")")
		}
		schema.GovernanceLevel = string(effective)
	} else {
		// 领域变化可能抬高下限，随手改领域也要重算。
		current := types.ParseOntologyGovernanceLevel(schema.GovernanceLevel)
		schema.GovernanceLevel = string(types.EffectiveOntologyGovernanceLevel(current, schema.Domain))
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
	// §8.2 治理门禁 0：级别要求更强的审核。L4 需要黄金集回归通过；这套检查
	// 放在结构校验之前，因为"根本没有资格发布"比"结构写错了"更早成立。
	if err := s.requireGovernanceGate(ctx, tenantID, schema); err != nil {
		return nil, err
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

// requireGovernanceGate enforces the §8.2 level-dependent publish rules.
//
// 三条规则按级别递进：
//   - L4：本空间黄金集最近一次评测必须通过。本体是抽取规则的来源，改它
//     等于改全空间的抽取结果，没有回归门禁就把结构放出去等于赌运气。
//   - L2+：结构变更需要治理审批。这里检查是否已经存在针对本 schema 的
//     待决/已通过审批；没有审批引擎的部署（governance == nil）跳过该条，
//     而不是让整个本体功能不可用。
//   - L3 的"迁移预演"是操作步骤而非服务端判定：它由发布前的 dry-run
//     接口承担（类型变更影响面），这里不重复拦截。
//
// 门禁查询失败一律**放行并记日志**：这是治理增强，不是安全门禁，用一次
// 数据库抖动锁死全部本体发布不划算。
func (s *ontologyService) requireGovernanceGate(
	ctx context.Context, tenantID uint64, schema *types.OntologySchema,
) error {
	level := types.ParseOntologyGovernanceLevel(schema.GovernanceLevel)
	if level.RequiresGoldenRegression() && s.goldenSets != nil {
		pass, run, err := s.checkGoldenRegression(ctx, tenantID, schema)
		if err != nil {
			logger.Warnf(ctx, "[ontology] golden gate check failed for schema %s: %v", schema.ID, err)
		} else if !pass {
			runID := ""
			if run != nil {
				runID = run.ID
			}
			s.logAudit(ctx, tenantID, "", types.OntologyActionSchemaUpdated,
				"ontology_schema", schema.ID, map[string]interface{}{
					"blocked_by": "golden_gate",
					"run_id":     runID,
					"level":      string(level),
				})
			return apperrors.NewForbiddenError(
				"该本体为 L4（关键级），发布前必须通过黄金集回归评测")
		}
	}
	if level.RequiresDualApproval() && s.governance != nil {
		approved, err := s.hasOntologyApproval(ctx, tenantID, schema)
		if err != nil {
			logger.Warnf(ctx, "[ontology] approval lookup failed for schema %s: %v", schema.ID, err)
		} else if !approved {
			return apperrors.NewForbiddenError(
				"该本体为 " + string(level) + " 级，发布前需要经过治理审批")
		}
	}
	return nil
}

// checkGoldenRegression asks the golden gate of the schema's domain.
//
// 本体没有"自己的空间"，所以沿 domain 找一个覆盖该领域的黄金集：评测的是
// 抽取结果，而抽取结果按空间存放。找不到任何集合时返回"未通过"——L4 级
// 结构声称自己是关键路径，没有可验证的基准就不该发布。
func (s *ontologyService) checkGoldenRegression(
	ctx context.Context, tenantID uint64, schema *types.OntologySchema,
) (bool, *types.GoldenSetRun, error) {
	sets, err := s.goldenSets.ListSets(ctx, tenantID, schema.Domain)
	if err != nil {
		return false, nil, err
	}
	if len(sets) == 0 {
		return false, nil, nil
	}
	for _, set := range sets {
		if set == nil {
			continue
		}
		pass, run, gateErr := s.goldenSets.CheckGate(ctx, tenantID, set.ID)
		if gateErr != nil {
			return false, nil, gateErr
		}
		if !pass {
			return false, run, nil
		}
	}
	return true, nil, nil
}

// hasOntologyApproval reports whether the schema has an approved governance
// task for this version.
//
// 审批主体用 ontology_schema；多级链的会签/或签由审批引擎自己判定，这里只问
// "最新一条是否已通过并带回了回执"。
func (s *ontologyService) hasOntologyApproval(
	ctx context.Context, tenantID uint64, schema *types.OntologySchema,
) (bool, error) {
	decision, err := s.governance.RequireApproval(
		ctx, tenantID, types.ApprovalSubjectOntology, schema.ID)
	if err != nil {
		if errors.Is(err, ErrGovernanceNotApproved) {
			return false, nil
		}
		return false, err
	}
	return decision != nil && decision.Approved, nil
}
