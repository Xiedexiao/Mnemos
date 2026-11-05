package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// PromptRolloutRepository persists the active-version pointer per template.
//
// 指针独立于版本表：版本表是 append-only 的历史，指针是"现在用哪一版"。
// 把"当前生效"写进版本行会让历史被改写，而历史不可变正是可审计的前提。
type PromptRolloutRepository interface {
	// GetPointer returns the pointer for one template key; (nil, nil) when none
	// is set, which means "serve the latest version" (the previous behaviour).
	GetPointer(
		ctx context.Context, tenantID uint64, templateKey string,
	) (*types.PromptVersionPointer, error)
	// SavePointer upserts the pointer.
	SavePointer(ctx context.Context, pointer *types.PromptVersionPointer) error
	// GetVersion returns one exact version row, so a rollback target can be
	// proven to exist before the pointer moves to it.
	GetVersion(
		ctx context.Context, tenantID uint64, templateKey string, version int,
	) (*types.PromptVersion, error)
}

// PromptRolloutService is the §16.5 canary / rollback surface.
type PromptRolloutService interface {
	// Configure sets the stable version and an optional canary split.
	Configure(
		ctx context.Context, tenantID uint64, actorID string,
		pointer types.PromptVersionPointer,
	) (*types.PromptVersionPointer, error)
	// Rollback points the stable version back at an existing version.
	Rollback(
		ctx context.Context, tenantID uint64, actorID string,
		request types.PromptRollbackRequest,
	) (*types.PromptVersionPointer, error)
	// Select resolves which version one subject should see.
	Select(
		ctx context.Context, tenantID uint64, templateKey, subjectKey string,
	) (*types.PromptVersionSelection, error)
	// Get returns the pointer (a zero value when unset).
	Get(ctx context.Context, tenantID uint64, templateKey string) (*types.PromptVersionPointer, error)
}
