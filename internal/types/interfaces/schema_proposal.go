package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// SchemaProposalRepository persists the schema_proposals table created by
// migration 000155 (KnowAxis V3.0 §8.2/§8.4). Every read/write is tenant
// scoped.
type SchemaProposalRepository interface {
	// Create inserts one proposal row (service层已填好 ID/状态/审计字段).
	Create(ctx context.Context, proposal *types.SchemaProposal) error
	// GetByID returns one tenant-scoped proposal; missing rows surface as
	// repository.ErrSchemaProposalNotFound.
	GetByID(ctx context.Context, tenantID uint64, proposalID string) (*types.SchemaProposal, error)
	// List returns the tenant's proposals newest first, with the total count.
	// filter.Status empty means no status restriction.
	List(
		ctx context.Context, tenantID uint64, filter types.SchemaProposalListFilter,
	) ([]*types.SchemaProposal, int64, error)
	// Update persists the mutable governance fields of one proposal: status,
	// reviewed_by, review_comment, applied_schema_id, compatibility_report and
	// updated_at. The proposal payload itself is immutable after creation.
	Update(ctx context.Context, proposal *types.SchemaProposal) error
	// UpdateCompatibilityReport writes only compatibility_report + updated_at,
	// so a check never races with a concurrent approval on other columns.
	UpdateCompatibilityReport(ctx context.Context, proposal *types.SchemaProposal) error
}

// SchemaProposalService implements the §8.2/§8.4 governance loop on top of
// SchemaProposalRepository, OntologyService and (optionally) a chat model:
//
//	documents --ProposeFromDocuments--> pending proposal
//	proposal  --CheckCompatibility---> compatibility_report (blocker/warning/info)
//	proposal  --Approve|Reject-------> approved | rejected   (Admin, 四眼)
//	proposal  --Apply----------------> draft ontology schema (applied_schema_id)
//
// Hard rules:
//   - a proposal never mutates ontology_schemas by itself; only Apply does,
//     and only when status == approved and the compatibility report has no
//     blocker;
//   - approval requires Admin and a reviewer different from the creator
//     (four-eyes), enforced in the service;
//   - every state change writes an ontology.schema_proposal_* audit event.
type SchemaProposalService interface {
	// ProposeFromDocuments builds a pending proposal from up to 8 knowledge
	// documents (≤4000 runes each): their chunk text is fed to the injected
	// chat model, whose JSON answer is parsed into a ProposalPayload. A model
	// that does not return the expected JSON yields a 400. Without an injected
	// model this returns ErrSchemaProposalModelMissing; callers fall back to
	// ProposeManual.
	ProposeFromDocuments(
		ctx context.Context, tenantID uint64, name, domain string, knowledgeIDs []string, actorID string,
	) (*types.SchemaProposal, error)
	// ProposeManual creates a pending proposal from a caller-supplied payload
	// (the escape hatch when no model is configured, or when a human wants to
	// hand-edit what the model produced).
	ProposeManual(ctx context.Context, input types.SchemaProposalManualInput, actorID string) (*types.SchemaProposal, error)
	// CheckCompatibility diffs the proposal against the tenant's published
	// schemas plus the same-named schema (any status) and stores the report:
	// new types are auto-pass (info), same-named type property changes are
	// warnings, relation endpoints resolving nowhere are blockers.
	CheckCompatibility(ctx context.Context, tenantID uint64, proposalID string) (*types.CompatibilityReport, error)
	// Approve marks a pending proposal approved (Admin + reviewer != creator).
	Approve(ctx context.Context, tenantID uint64, proposalID, reviewerID, comment string) (*types.SchemaProposal, error)
	// Reject marks a pending proposal rejected (terminal; Admin + four-eyes).
	Reject(ctx context.Context, tenantID uint64, proposalID, reviewerID, comment string) (*types.SchemaProposal, error)
	// Apply materializes an approved proposal into ontology_schemas through
	// OntologyService.CreateSchema/AddType/AddRelation (appending to the
	// same-named draft schema when it already exists), then records
	// applied_schema_id and flips the status to applied.
	Apply(ctx context.Context, tenantID uint64, proposalID, actorID string) (*types.SchemaProposalApplyResult, error)
	// List returns the tenant's proposals (optionally filtered by status).
	List(ctx context.Context, tenantID uint64, filter types.SchemaProposalListFilter) ([]*types.SchemaProposal, int64, error)
	// Get returns one proposal by ID (tenant scoped).
	Get(ctx context.Context, tenantID uint64, proposalID string) (*types.SchemaProposal, error)
}
