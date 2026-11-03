package service

import (
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// 事件版本乱序保护（KnowAxis V3.0 §3.3 增量同步 / §7.10 同步一致性）。
//
// §3.3 要求"事件带业务版本号，拒绝旧事件覆盖新版本；乱序安全"。平台此前
// 只把源的更新时间记进 metadata.source_updated_at 供展示，**从不比较**：
// 一个延迟重投的旧事件会把刚同步好的新内容覆盖回旧版本，而且不会报错。
//
// 这里把比较放在 ingest 的替换路径上：只有当来件的源更新时间不早于已存版本
// 时才替换。时间相等的视为同一版本，仍然替换（内容可能因源系统内部修订而
// 变化，而源没有推进 mtime）。

// sourceUpdateStale reports whether an incoming item is older than what is
// already stored for the same external id.
//
// Unknown timestamps never block: connectors that do not expose a modified time
// cannot participate in ordering, and silently dropping their events would be a
// worse failure than an occasional out-of-order write. The check only rejects
// when both sides are known and the incoming one is strictly older.
func sourceUpdateStale(incoming *types.FetchedItem, stored map[string]string) bool {
	if incoming == nil || incoming.UpdatedAt.IsZero() || stored == nil {
		return false
	}
	raw := stored["source_updated_at"]
	if raw == "" {
		return false
	}
	storedAt, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return false
	}
	return incoming.UpdatedAt.Before(storedAt)
}

// sourceVersionOf extracts the comparable source version from a stored row.
func sourceVersionOf(knowledge *types.Knowledge) map[string]string {
	if knowledge == nil {
		return nil
	}
	return knowledge.GetMetadata()
}
