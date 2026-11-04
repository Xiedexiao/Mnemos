package interfaces

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// WatermarkRepository persists the download trace ledger (§16.3 水印留痕).
type WatermarkRepository interface {
	// Create appends one record.
	Create(ctx context.Context, record *types.WatermarkRecord) error
	// List returns the tenant's records, newest first, optionally filtered.
	List(
		ctx context.Context, tenantID uint64, knowledgeID, userID string, limit int,
	) ([]*types.WatermarkRecord, error)
}

// WatermarkService plans the mark for a download and records the trace.
type WatermarkService interface {
	// Plan resolves the mark for one download without recording it.
	//
	// 与 Record 分开：调用方需要先拿到水印文本才能写进文件，写文件之后才
	// 知道该记哪一行。合成一个方法会让"写文件失败但台账已记"成为可能。
	Plan(
		ctx context.Context, tenantID uint64, userID string,
		knowledge *types.Knowledge, fileName string, source string, requestID string,
	) types.WatermarkApplied
	// Record writes the ledger row for a delivered download.
	Record(
		ctx context.Context, tenantID uint64, userID string,
		knowledge *types.Knowledge, applied types.WatermarkApplied, source, requestID string,
	) error
	// List returns the tenant's watermark ledger.
	List(
		ctx context.Context, tenantID uint64, knowledgeID, userID string, limit int,
	) ([]*types.WatermarkRecord, error)
	// Now exposes the clock so callers can render the same timestamp.
	Now() time.Time
}
