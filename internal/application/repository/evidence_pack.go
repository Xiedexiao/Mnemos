package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

// ErrEvidencePackNotFound 证据包不存在的哨兵错误，service 层据此映射 404。
var ErrEvidencePackNotFound = errors.New("evidence pack not found")

// 证据包列表参数：会话时间线是低频人读接口，默认 50 条、硬上限 200，
// 防止无界扫描（与审批待办/回执列表同一口径）。
const (
	evidenceListDefaultLimit = 50
	evidenceListMaxLimit     = 200
)

// evidencePackRepository implements the evidence pack repository interface.
type evidencePackRepository struct {
	db *gorm.DB
}

// NewEvidencePackRepository creates a new evidence pack repository.
func NewEvidencePackRepository(db *gorm.DB) interfaces.EvidencePackRepository {
	return &evidencePackRepository{db: db}
}

// evidencePackAdvisoryLock 串行化同一租户的链尾读取与 request_id upsert：
// 两个并发 Save 同时读到同一链尾会让哈希链分叉（同一 prev 挂两个子行）。
// Postgres 用事务级 advisory lock 兜住；其他方言（单测 sqlite）退化为
// 普通事务，靠单写者假设。
const evidencePackAdvisoryLock = "SELECT pg_advisory_xact_lock(hashtext('evidence_packs:' || ?))"

// Save 按 request_id upsert 一行证据包，事务内完成"读现状 → 定锚 → 算行
// 哈希 → 落行"。语义详见 interfaces.EvidencePackRepository.Save。
func (r *evidencePackRepository) Save(ctx context.Context, pack *types.EvidencePack) error {
	if pack == nil {
		return errors.New("evidence pack: nil pack")
	}
	if pack.TenantID == 0 {
		return errors.New("evidence pack: tenant_id is required")
	}
	if pack.RequestID == "" {
		return errors.New("evidence pack: request_id is required")
	}
	if pack.CreatedAt.IsZero() {
		return errors.New("evidence pack: created_at is required for hash chaining")
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "postgres" {
			if err := tx.Exec(evidencePackAdvisoryLock, fmt.Sprintf("%d", pack.TenantID)).Error; err != nil {
				return err
			}
		}

		var existing types.EvidencePack
		err := tx.Where("tenant_id = ? AND request_id = ?", pack.TenantID, pack.RequestID).
			First(&existing).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			return r.insertChained(tx, pack)
		case err != nil:
			return err
		default:
			return r.upsertExisting(tx, &existing, pack)
		}
	})
}

// insertChained 首次写入：锚在当前租户链尾，计算行哈希后插入。
func (r *evidencePackRepository) insertChained(tx *gorm.DB, pack *types.EvidencePack) error {
	var tail types.EvidencePack
	err := tx.Where("tenant_id = ?", pack.TenantID).
		Order("created_at DESC").
		First(&tail).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		// 本租户第一行证据包：锚在链首。
		pack.HashChainPrev = types.EvidencePackGenesisHash
	case err != nil:
		return err
	default:
		pack.HashChainPrev = tail.HashChainRow
	}
	pack.HashChainRow = types.ComputeEvidenceRowHash(pack.HashChainPrev, pack)
	return tx.Create(pack).Error
}

// upsertExisting 重复 request_id 的落行路径：
//   - 同载荷重复投递（管道重试、事件重放）：幂等跳过；
//   - 载荷有变（答案流式落定后补写）：更新内容列并按原 hash_chain_prev
//     重算行哈希。prev 取 existing 而非当前链尾 —— 若 existing 已不是链尾，
//     重算后的行哈希会让后续链校验显式断链，把"定稿被改写"暴露出来。
func (r *evidencePackRepository) upsertExisting(
	tx *gorm.DB, existing *types.EvidencePack, pack *types.EvidencePack,
) error {
	// 锚点与身份沿用既有行：request_id 不变，行哈希随载荷变化。
	pack.ID = existing.ID
	pack.CreatedAt = existing.CreatedAt
	pack.HashChainPrev = existing.HashChainPrev

	if recomputed := types.ComputeEvidenceRowHash(existing.HashChainPrev, pack); recomputed == existing.HashChainRow {
		// 同载荷重复投递：幂等跳过（调用方拿到与库里一致的 pack）。
		pack.HashChainRow = existing.HashChainRow
		return nil
	}
	pack.HashChainRow = types.ComputeEvidenceRowHash(existing.HashChainPrev, pack)
	return tx.Save(pack).Error
}

// GetByRequestID gets one evidence pack by tenant and request id.
func (r *evidencePackRepository) GetByRequestID(
	ctx context.Context, tenantID uint64, requestID string,
) (*types.EvidencePack, error) {
	var pack types.EvidencePack
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND request_id = ?", tenantID, requestID).
		First(&pack).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrEvidencePackNotFound
		}
		return nil, err
	}
	return &pack, nil
}

// ListBySession returns the most recent `limit` packs of a session, returned
// in chronological order so the caller replays the timeline as it happened.
func (r *evidencePackRepository) ListBySession(
	ctx context.Context, tenantID uint64, sessionID string, limit int,
) ([]*types.EvidencePack, error) {
	if limit <= 0 {
		limit = evidenceListDefaultLimit
	}
	if limit > evidenceListMaxLimit {
		limit = evidenceListMaxLimit
	}
	// 先取最新 N 条，再反转为时间正序：limit 截掉的是最旧的行，
	// 时间线头部（最新问答）永远可见。
	var packs []*types.EvidencePack
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND session_id = ?", tenantID, sessionID).
		Order("created_at DESC").
		Limit(limit).
		Find(&packs).Error; err != nil {
		return nil, err
	}
	for i, j := 0, len(packs)-1; i < j; i, j = i+1, j-1 {
		packs[i], packs[j] = packs[j], packs[i]
	}
	return packs, nil
}

// PurgeExpired deletes rows whose retention_until has passed (non-null and
// strictly before now). Rows with NULL retention_until are kept forever.
// Called by the retention sweep task（后续按 audit retention sweep 模式接入）.
func (r *evidencePackRepository) PurgeExpired(ctx context.Context, now time.Time) (int64, error) {
	result := r.db.WithContext(ctx).
		Where("retention_until IS NOT NULL AND retention_until < ?", now).
		Delete(&types.EvidencePack{})
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}
