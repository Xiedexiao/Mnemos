package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// KnowledgeMapRepository supplies the raw rows behind one space's knowledge map.
type KnowledgeMapRepository interface {
	// MapNodes returns the documents of one space with the fields the map
	// needs (folder, lifecycle, classification, tags). Visibility filtering
	// happens in the service, because it needs subject expansion.
	MapNodes(
		ctx context.Context, tenantID uint64, knowledgeBaseID string, limit int,
	) ([]types.KnowledgeMapNode, error)
	// MapEdges returns the relation edges whose BOTH ends are in the given
	// document set. Edges to documents outside the set are dropped here rather
	// than filtered later: an edge to an invisible document would leak that it
	// exists.
	MapEdges(
		ctx context.Context, tenantID uint64, knowledgeIDs []string,
	) ([]types.KnowledgeMapEdge, error)
	// CountSpaceKnowledge counts the space's documents so the map can report
	// truncation and hidden counts without loading everything.
	CountSpaceKnowledge(ctx context.Context, tenantID uint64, knowledgeBaseID string) (int64, error)
	// RoleMapNodes returns documents tagged for one job role (§15.5 岗位知识地图).
	// Role mapping uses tags: the platform has no separate job-role model, and
	// a tag is the one explicit declaration of scope it does have.
	RoleMapNodes(
		ctx context.Context, tenantID uint64, tagNames []string, limit int,
	) ([]types.KnowledgeMapNode, error)
}

// KnowledgeMapService is the §4.6 / §15.5 knowledge-map surface.
type KnowledgeMapService interface {
	// SpaceMap builds the map of one space under the requested view.
	SpaceMap(
		ctx context.Context, tenantID uint64, knowledgeBaseID string,
		view types.KnowledgeMapView, limit int,
	) (*types.KnowledgeMap, error)
	// RoleMap builds the job-role map: documents tagged for the role, grouped
	// by folder, with the learning-task coverage of the caller.
	RoleMap(
		ctx context.Context, tenantID uint64, userID string, roleTags []string, limit int,
	) (*types.RoleKnowledgeMap, error)
}
