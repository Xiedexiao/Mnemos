package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// KnowledgeMigrationService applies a bulk import manifest (§16.3 迁移工具).
type KnowledgeMigrationService interface {
	// Import validates the manifest and applies it item by item.
	//
	// 逐条独立成败：一条坏文件不会让其余条目全部失败，也不能因为整批里
	// 有一条失败就静默回滚——迁移完成后必须能看出"哪些没进来、为什么"。
	Import(
		ctx context.Context, tenantID uint64, actorID string, manifest types.MigrationManifest,
	) (*types.MigrationReport, error)
}
