package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
)

// 证据包业务哨兵错误。handler 层据此映射 HTTP 语义（404/400/401）。
var (
	// ErrEvidenceTenantMissing 请求上下文缺少租户（中间件层未注入），且
	// pack 上也没有预填租户 ID。
	ErrEvidenceTenantMissing = errors.New("evidence pack: tenant context is missing")
	// ErrEvidenceRequestIDRequired 缺少 request_id（证据包的锚点字段）。
	ErrEvidenceRequestIDRequired = errors.New("evidence pack: request id is required")
	// ErrEvidenceSessionIDRequired 缺少 session_id（会话时间线查询锚点）。
	ErrEvidenceSessionIDRequired = errors.New("evidence pack: session id is required")
	// ErrEvidenceInvalidRefusal refusal 不在白名单（''|refused|conflict|no_evidence）。
	ErrEvidenceInvalidRefusal = errors.New("evidence pack: unknown refusal code")
	// ErrEvidencePackNotFound 证据包不存在（或不在本租户）。
	ErrEvidencePackNotFound = errors.New("evidence pack: evidence pack not found")
)

// evidenceSessionListLimit 是会话时间线的默认条数（仓储侧还有硬上限）。
const evidenceSessionListLimit = 50

// evidencePackService implements interfaces.EvidencePackService
// （KnowAxis V3.0 §13.4 的 v1 子集）。
type evidencePackService struct {
	packs interfaces.EvidencePackRepository
	now   func() time.Time
}

// NewEvidencePackService constructs the evidence pack service.
func NewEvidencePackService(packs interfaces.EvidencePackRepository) interfaces.EvidencePackService {
	return &evidencePackService{
		packs: packs,
		now:   time.Now,
	}
}

// Save 冻结一次问答的证据包：
//  1. 定租户：pack.TenantID 优先（管道异步路径可能没有租户 ctx），
//     否则取 ctx（缺失报 ErrEvidenceTenantMissing）；
//  2. 校验 request_id 与 refusal 白名单；
//  3. 补 ID / CreatedAt / RetentionUntil（v1 保留期固定
//     created_at + EvidencePackRetentionDays 天，常量见 types 包）；
//  4. 交仓储按 request_id upsert 入哈希链（事务内锚链尾）。
//
// 返回落行后的 pack（含仓储回填的哈希链锚点）。失败不打断问答主链路 ——
// 调用方（管道挂钩）应按 best-effort 记日志降级。
func (s *evidencePackService) Save(ctx context.Context, pack *types.EvidencePack) (*types.EvidencePack, error) {
	if pack == nil {
		return nil, ErrEvidenceRequestIDRequired
	}
	if pack.TenantID == 0 {
		tenantID, ok := types.TenantIDFromContext(ctx)
		if !ok || tenantID == 0 {
			return nil, ErrEvidenceTenantMissing
		}
		pack.TenantID = tenantID
	}
	if pack.RequestID == "" {
		return nil, ErrEvidenceRequestIDRequired
	}
	if !types.EvidencePackRefusal(pack.Refusal).Valid() {
		return nil, ErrEvidenceInvalidRefusal
	}

	if pack.ID == "" {
		pack.ID = uuid.NewString()
	}
	if pack.CreatedAt.IsZero() {
		pack.CreatedAt = s.now()
	}
	if pack.RetentionUntil == nil {
		until := pack.CreatedAt.Add(types.EvidencePackRetentionDays * 24 * time.Hour)
		pack.RetentionUntil = &until
	}

	if err := s.packs.Save(ctx, pack); err != nil {
		return nil, fmt.Errorf("evidence pack: save: %w", err)
	}
	return pack, nil
}

// GetByRequestID 按 request_id 取单条（租户隔离；本人/Admin 可见性由
// 路由层控制，服务只按租户隔离）。
func (s *evidencePackService) GetByRequestID(ctx context.Context, requestID string) (*types.EvidencePack, error) {
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		return nil, ErrEvidenceTenantMissing
	}
	pack, err := s.packs.GetByRequestID(ctx, tenantID, requestID)
	if err != nil {
		if errors.Is(err, repository.ErrEvidencePackNotFound) {
			return nil, ErrEvidencePackNotFound
		}
		return nil, err
	}
	return pack, nil
}

// ListBySession 返回会话时间线上的证据包（最近 limit 条、时间正序）。
func (s *evidencePackService) ListBySession(
	ctx context.Context, sessionID string, limit int,
) ([]*types.EvidencePack, error) {
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		return nil, ErrEvidenceTenantMissing
	}
	if sessionID == "" {
		return nil, ErrEvidenceSessionIDRequired
	}
	if limit <= 0 {
		limit = evidenceSessionListLimit
	}
	packs, err := s.packs.ListBySession(ctx, tenantID, sessionID, limit)
	if err != nil {
		return nil, err
	}
	return packs, nil
}

// PurgeExpired 按保留期清理过期证据包（系统任务入口，无租户维度）。
func (s *evidencePackService) PurgeExpired(ctx context.Context, now time.Time) (int64, error) {
	return s.packs.PurgeExpired(ctx, now)
}
