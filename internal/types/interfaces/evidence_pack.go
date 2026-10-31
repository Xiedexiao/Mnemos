package interfaces

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// EvidencePackRepository 证据包的存储原语。链完整性由 Save 在单事务内
// "读链尾 → 算行哈希 → 落行"的原子性保证（同租户 advisory lock 串行化）。
type EvidencePackRepository interface {
	// Save 按 request_id upsert 一行证据包（一次问答 = 一行）：
	//   - 首次写入：读当前租户链尾 row_hash 作为 hash_chain_prev（链首为
	//     GENESIS），计算 hash_chain_row 后插入；
	//   - 重复投递（同 request_id 同载荷）：幂等跳过，返回 nil；
	//   - 重存（同 request_id 不同载荷）：更新内容列并按原 hash_chain_prev
	//     重算行哈希 —— 若该行已不是链尾，后续链校验会显式断链（可检测
	//     的改写，审计语义如此要求）。
	// pack.CreatedAt 必须由调用方填好（哈希载荷的一部分）。
	Save(ctx context.Context, pack *types.EvidencePack) error
	// GetByRequestID 按租户 + request_id 取单条；不存在返回
	// repository.ErrEvidencePackNotFound。
	GetByRequestID(ctx context.Context, tenantID uint64, requestID string) (*types.EvidencePack, error)
	// ListBySession 返回该会话最近 limit 条证据包，按创建时间正序返回
	// （回放一条会话时间线）。limit <= 0 时用默认值，硬上限见仓储常量。
	ListBySession(ctx context.Context, tenantID uint64, sessionID string, limit int) ([]*types.EvidencePack, error)
	// PurgeExpired 删除 retention_until 已过期（非空且 < now）的行，返回
	// 删除行数。retention_until IS NULL 的行视同永久保留。供后续定时清理
	// 任务（audit retention sweep 模式）调用。
	PurgeExpired(ctx context.Context, now time.Time) (int64, error)
}

// EvidencePackService 证据包服务（KnowAxis V3.0 §13.4 v1 子集）。租户一律
// 取自 ctx（types.TenantIDFromContext），缺失返回 service.ErrEvidenceTenantMissing；
// 管道侧异步写入若 ctx 无租户，可在 pack.TenantID 预填租户 ID。本人可见性
// /Admin 放行由路由层控制，服务只按租户隔离。
type EvidencePackService interface {
	// Save 冻结一次问答的证据包：补 ID/CreatedAt/RetentionUntil（v1 固定
	// created_at + EvidencePackRetentionDays 天），校验 refusal 白名单后
	// 交仓储按 request_id upsert 入链。pipeline 内写入由聊天管道挂钩调用
	// （见 handler/session 侧装配说明）。
	Save(ctx context.Context, pack *types.EvidencePack) (*types.EvidencePack, error)
	// GetByRequestID 按 request_id 取单条（租户隔离）。
	GetByRequestID(ctx context.Context, requestID string) (*types.EvidencePack, error)
	// ListBySession 返回会话时间线上的证据包（最近 limit 条，时间正序）。
	ListBySession(ctx context.Context, sessionID string, limit int) ([]*types.EvidencePack, error)
	// PurgeExpired 按保留期清理过期证据包（系统任务入口，无租户维度）。
	PurgeExpired(ctx context.Context, now time.Time) (int64, error)
}
