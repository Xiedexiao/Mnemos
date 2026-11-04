package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// ReadingRepository persists reading progress and serves the outline source.
type ReadingRepository interface {
	// GetProgress returns one user's position in one document, or nil.
	GetProgress(
		ctx context.Context, tenantID uint64, userID, knowledgeID string,
	) (*types.ReadingProgress, error)
	// SaveProgress upserts the position.
	SaveProgress(ctx context.Context, progress *types.ReadingProgress) error
	// ListProgress returns the user's positions, newest first.
	ListProgress(ctx context.Context, tenantID uint64, userID string, limit int) ([]*types.ReadingProgress, error)
	// ChunkContents returns the document's chunk texts in order, for the TOC.
	ChunkContents(ctx context.Context, tenantID uint64, knowledgeID string) ([]string, error)
}

// ReadingService is the §15.2 reading-page surface.
type ReadingService interface {
	// TOC returns the document outline (permission-checked).
	TOC(ctx context.Context, tenantID uint64, knowledgeID string) (*types.ReadingTOC, error)
	// SaveProgress records the caller's position.
	SaveProgress(
		ctx context.Context, tenantID uint64, userID string, input types.ReadingProgressInput,
	) (*types.ReadingProgress, error)
	// GetProgress returns the caller's position in one document.
	GetProgress(
		ctx context.Context, tenantID uint64, userID, knowledgeID string,
	) (*types.ReadingProgress, error)
	// ContinueReading returns the caller's recent positions.
	ContinueReading(
		ctx context.Context, tenantID uint64, userID string, limit int,
	) (*types.ReadingProgressList, error)
}
