package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// KnowledgeRelationRepository persists knowledge-to-knowledge edges
// (KnowAxis V3.0 §4.5).
type KnowledgeRelationRepository interface {
	// CreatePair inserts an edge and its reverse mirror atomically.
	CreatePair(ctx context.Context, forward, reverse *types.KnowledgeRelation) error
	// ListForKnowledge returns every edge touching one document.
	ListForKnowledge(
		ctx context.Context, tenantID uint64, knowledgeID string,
	) ([]*types.KnowledgeRelation, error)
	// ListOutgoing returns edges the document authored.
	ListOutgoing(
		ctx context.Context, tenantID uint64, knowledgeID string, relationType string,
	) ([]*types.KnowledgeRelation, error)
	// ListIncoming returns edges pointing at the document.
	ListIncoming(
		ctx context.Context, tenantID uint64, knowledgeID string, relationType string,
	) ([]*types.KnowledgeRelation, error)
	// DeletePair removes both directions of one edge.
	DeletePair(ctx context.Context, tenantID uint64, fromID, toID, relationType string) (int64, error)
	// DeleteAllForKnowledge removes every edge touching one document.
	DeleteAllForKnowledge(ctx context.Context, tenantID uint64, knowledgeID string) (int64, error)
	// CountForKnowledge counts edges touching one document.
	CountForKnowledge(ctx context.Context, tenantID uint64, knowledgeID string) (int64, error)
}

// KnowledgeRelationService is the §4.5 surface: link two documents with a typed
// relation, list what a document links to and what links back, and report the
// impact of deleting a document that others depend on.
type KnowledgeRelationService interface {
	// Link creates a typed edge between two documents the caller may see.
	Link(
		ctx context.Context, tenantID uint64, fromID, toID string,
		relationType types.KnowledgeRelationType, note, actorID string,
		origin types.KnowledgeRelationOrigin,
	) (*types.KnowledgeRelation, error)
	// Unlink removes a typed edge in both directions.
	Unlink(
		ctx context.Context, tenantID uint64, fromID, toID string,
		relationType types.KnowledgeRelationType,
	) (int64, error)
	// List returns the reader-facing views of a document's edges.
	List(
		ctx context.Context, tenantID uint64, knowledgeID string,
	) ([]types.KnowledgeRelationView, error)
	// DeleteImpact reports how many edges would be orphaned by deleting a
	// document, so the delete flow can warn instead of silently breaking links.
	DeleteImpact(ctx context.Context, tenantID uint64, knowledgeID string) (int64, error)
	// OnKnowledgeDeleted removes every edge touching a deleted document.
	OnKnowledgeDeleted(ctx context.Context, tenantID uint64, knowledgeID string) (int64, error)
}
