package service

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/runtime"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

// FreshnessReviewScheduler implements KnowAxis V3.0 §7.7 复审调度 + §7.9
// 新鲜度评分：
//
//   - 后台 runner：24h Ticker（范式同 AuditLogRetentionRunner — 无
//     wall-clock 对齐需求，"approximately daily, eventually" 即可），
//     每轮做一次 RunSweep 批处理。
//   - 查询面：ScoreFor（单条评分）与 ReviewQueue（待复审清单，分页），
//     由 internal/handler/freshness.go 暴露。
//
// 扫描与转换语义：
//
//	Phase A（显式过期落库）: lifecycle_status='valid' AND expires_at < now
//	  → Transition(mark_expired)。读取时 ResolveLifecycle 早已把这类行降级
//	  为 expired（V3.0 §7.1 硬规则：到期降级是读取时行为，不做静默改库），
//	  本阶段是把该结果"显式落库"的治理批处理，走既有 mark_expired 动作。
//	Phase B（复审到期）: lifecycle_status='valid' AND review_cycle_days>0
//	  AND COALESCE(last_reviewed_at, created_at) < now - review_cycle_days
//	  → Transition("mark_pending_review_cycle")。
//	  valid → pending_review_cycle 在状态机（knowledge_lifecycle.go）里合法；
//	  Transition 目前没有对应的 Action 常量与 case，见下方 TODO。
//
// 查询直接走 *gorm.DB（先例：AutomationService / HousekeepingService），
// 因为跨租户系统扫描不属于任何现成 repository 接口的职责面；单条读取
// 复用 KnowledgeRepository.GetKnowledgeByIDOnly 以对齐 404 语义。
type FreshnessReviewScheduler struct {
	db        *gorm.DB
	kgRepo    interfaces.KnowledgeRepository
	lifecycle interfaces.KnowledgeLifecycleService
	audit     interfaces.AuditLogService
	now       func() time.Time

	startOnce sync.Once
	stopOnce  sync.Once
	stopCh    chan struct{}
	doneCh    chan struct{}
	// started 语义同 AuditLogRetentionRunner：在 startOnce.Do 内、goroutine
	// 启动前置位，让 Stop 能区分"从未 Start"与"正在跑"而不死锁。
	started atomic.Bool
}

// 接口满足性编译检查。
var (
	_ interfaces.FreshnessService = (*FreshnessReviewScheduler)(nil)
)

// 复审调度常量。
const (
	// freshnessSweepInterval 清扫间隔。24h 对复审周期（天粒度）足够：
	// 截断点每轮移动 24h，超期一天内被发现。缩短只会产生空扫。
	freshnessSweepInterval = 24 * time.Hour
	// freshnessSweepStartupDelay 首扫延迟：避开迁移与启动请求洪峰，
	// 范式同 auditLogPurgeStartupDelay。
	freshnessSweepStartupDelay = 10 * time.Minute
	// freshnessSweepBatchSize 每批扫描行数（keyset 分页）。
	freshnessSweepBatchSize = 200
	// freshnessSweepTimeout 单轮清扫的整体 DB 预算：批内是逐行两次查询
	// 的转换，大积压时一轮可达数分钟；超时放弃，下一轮继续。
	freshnessSweepTimeout = 5 * time.Minute
	// freshnessSchedulerActor 系统清扫的审计 actor（先例 kb_activity.go）。
	freshnessSchedulerActor = "system"
	// freshnessSweepEnabledEnvVar 运维开关（默认开启），范式同
	// WEKNORA_HOUSEKEEPING_ENABLED。
	freshnessSweepEnabledEnvVar = "WEKNORA_FRESHNESS_SWEEP_ENABLED"
)

// 复审调度审计动作（§7.7：每次状态变更都留审计）。定义在本文件而非
// types/audit_log.go：这是调度器的私有事件面，后续接入 §7.9 其它调度
// 事件时在此扩展。
const (
	// auditActionSchedulerReviewDue 复审到期 → pending_review_cycle。
	auditActionSchedulerReviewDue = types.AuditAction("scheduler.review_due")
	// auditActionSchedulerExpireLanded 显式过期落库 → expired。
	auditActionSchedulerExpireLanded = types.AuditAction("scheduler.expire_landed")
)

// NewFreshnessReviewScheduler 构造调度器（只校验依赖，不动数据库；直到
// Start 被调用前不会产生任何扫描）。db 供跨租户扫描/队列查询；kgRepo 供
// 单条读取（404 语义对齐）；lifecycle 承担全部状态迁移；audit 记录调度
// 事件（nil 时跳过审计，不阻塞清扫）。
func NewFreshnessReviewScheduler(
	db *gorm.DB,
	kgRepo interfaces.KnowledgeRepository,
	lifecycle interfaces.KnowledgeLifecycleService,
	audit interfaces.AuditLogService,
) *FreshnessReviewScheduler {
	return &FreshnessReviewScheduler{
		db:        db,
		kgRepo:    kgRepo,
		lifecycle: lifecycle,
		audit:     audit,
		now:       time.Now,
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
	}
}

// freshnessSweepEnabled 读取运维开关；未设置/非法值默认开启。
func freshnessSweepEnabled() bool {
	raw, ok := os.LookupEnv(freshnessSweepEnabledEnvVar)
	if !ok || strings.TrimSpace(raw) == "" {
		return true
	}
	enabled, err := strconv.ParseBool(raw)
	if err != nil {
		return true
	}
	return enabled
}

// Start 启动后台清扫 goroutine。重复调用是 no-op（sync.Once）。开关关闭
// 时 runner 保持休眠，Stop 仍可安全调用。
func (s *FreshnessReviewScheduler) Start(ctx context.Context) {
	if s == nil || s.db == nil || s.lifecycle == nil {
		return
	}
	s.startOnce.Do(func() {
		s.started.Store(true)
		if !freshnessSweepEnabled() {
			logger.Infof(ctx,
				"[freshness-scheduler] disabled via %s", freshnessSweepEnabledEnvVar)
			close(s.doneCh)
			return
		}
		logger.Infof(ctx,
			"[freshness-scheduler] starting sweep: interval=%s batch=%d startup_delay=%s",
			freshnessSweepInterval, freshnessSweepBatchSize, freshnessSweepStartupDelay)
		go s.loop()
	})
}

// Stop 通知循环退出并阻塞等待；幂等。从未 Start 时立即返回（见 started）。
func (s *FreshnessReviewScheduler) Stop() {
	s.StopWithin(0)
}

// StopWithin 是带上限的 Stop。timeout <= 0 表示等循环自然返回。
func (s *FreshnessReviewScheduler) StopWithin(timeout time.Duration) {
	if s == nil {
		return
	}
	if !s.started.Load() {
		return
	}
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
	if runtime.WaitFor(s.doneCh, timeout) {
		return
	}
	logger.Warnf(context.Background(),
		"[freshness-scheduler] sweep still running after %s; continuing shutdown", timeout)
}

// loop 清扫节奏：启动延迟后立即扫一次，再进入 Ticker。每次迭代用独立的
// context.Background（Start 的请求级 ctx 在容器 init 返回即被取消）。
func (s *FreshnessReviewScheduler) loop() {
	defer close(s.doneCh)

	startupTimer := time.NewTimer(freshnessSweepStartupDelay)
	defer startupTimer.Stop()
	select {
	case <-startupTimer.C:
	case <-s.stopCh:
		return
	}

	s.runOnce()

	ticker := time.NewTicker(freshnessSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.runOnce()
		case <-s.stopCh:
			return
		}
	}
}

// runOnce 执行单轮清扫（带整体超时预算），错误记 WARN 不中断进程。
func (s *FreshnessReviewScheduler) runOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), freshnessSweepTimeout)
	defer cancel()

	result, err := s.RunSweep(ctx)
	if err != nil {
		logger.Warnf(ctx, "[freshness-scheduler] sweep failed: err=%v", err)
		return
	}
	if result.ReviewFlagged > 0 || result.ExpiredLanded > 0 || result.Failures > 0 {
		logger.Infof(ctx,
			"[freshness-scheduler] sweep complete: review_flagged=%d expired_landed=%d failures=%d duration=%s",
			result.ReviewFlagged, result.ExpiredLanded, result.Failures, result.Duration)
	} else {
		logger.Debugf(ctx,
			"[freshness-scheduler] sweep complete: review_flagged=0 expired_landed=0 failures=0 duration=%s",
			result.Duration)
	}
}

// reviewDueComparison 返回"距上次复审（缺省 created_at）超出
// review_cycle_days"的 SQL 片段。now 作为参数绑定。逐行日期算术没有跨
// 方言的可移植写法，按 Dialector.Name() 分支（先例：storagebackend.go /
// task_queue.go）；仅本仓库实际支持的 postgres / sqlite，未知方言回退
// postgres 片段。两边都以 UTC 归一化后比较（sqlite 按文本比较 DATETIME，
// 带 时区的 now 会把未来任务误判为到期 —— 先例 AutomationService.tick）。
func reviewDueComparison(db *gorm.DB, now time.Time) (string, []interface{}) {
	switch db.Dialector.Name() {
	case "sqlite":
		// datetime(?, '-N days')：N 来自行内 review_cycle_days，靠字符串
		// 拼接进 modifier。存储文本与 datetime() 输出在亚秒/时区后缀上
		// 存在格式差异，字典序比较仅在秒级边界有噪声 —— 对天粒度的日扫
		// 无影响。
		return "COALESCE(last_reviewed_at, created_at) < datetime(?, '-' || CAST(review_cycle_days AS TEXT) || ' days')",
			[]interface{}{now}
	default:
		// postgres（及兼容方言）：timestamptz - (int * interval)。
		return "COALESCE(last_reviewed_at, created_at) < ? - review_cycle_days * INTERVAL '1 day'",
			[]interface{}{now}
	}
}

// RunSweep 执行一轮清扫：先 Phase A（显式过期落库，避免到期行再被
// Phase B 捞走），后 Phase B（复审到期 → pending_review_cycle）。批间
// 检查 ctx 取消；单条失败计数后继续。返回部分成功的结果与首个致命错误。
func (s *FreshnessReviewScheduler) RunSweep(ctx context.Context) (*types.FreshnessSweepResult, error) {
	result := &types.FreshnessSweepResult{StartedAt: s.now()}
	start := time.Now()

	now := s.now().UTC()

	expiredLanded, err := s.sweepExpired(ctx, now)
	result.ExpiredLanded = expiredLanded.affected
	result.Failures += expiredLanded.failures
	if err != nil {
		result.Duration = time.Since(start)
		return result, err
	}

	reviewFlagged, err := s.sweepReviewDue(ctx, now)
	result.ReviewFlagged = reviewFlagged.affected
	result.Failures += reviewFlagged.failures
	result.Duration = time.Since(start)
	return result, err
}

// sweepStats 是一个清扫阶段的产出。
type sweepStats struct {
	affected int // 成功转换的行数
	failures int // 转换失败（容错继续）的行数
}

// sweepExpired Phase A：显式有效期已过但仍为 valid 的行 → mark_expired。
// keyset 分页（id 升序 + 游标），转换成功后行离开匹配集，游标推进即可。
func (s *FreshnessReviewScheduler) sweepExpired(ctx context.Context, now time.Time) (sweepStats, error) {
	stats := sweepStats{}
	cursor := ""
	for {
		if ctx.Err() != nil {
			return stats, ctx.Err()
		}
		var rows []*types.Knowledge
		query := s.db.WithContext(ctx).Model(&types.Knowledge{}).
			Where("lifecycle_status = ? AND expires_at IS NOT NULL AND expires_at < ? AND id > ?",
				string(types.KnowledgeLifecycleValid), now, cursor).
			Order("id").Limit(freshnessSweepBatchSize)
		if err := query.Find(&rows).Error; err != nil {
			return stats, err
		}
		if len(rows) == 0 {
			return stats, nil
		}
		for _, row := range rows {
			cursor = row.ID
			if s.transition(ctx, row, types.KnowledgeLifecycleActionMarkExpired,
				auditActionSchedulerExpireLanded, "scheduler: explicit expiry landed") {
				stats.affected++
			} else {
				stats.failures++
			}
		}
		if len(rows) < freshnessSweepBatchSize {
			return stats, nil
		}
	}
}

// sweepReviewDue Phase B：复审到期仍为 valid 的行 → pending_review_cycle。
// 注意显式排除"expires_at 已过"的行（那是 Phase A 的业务，避免同一行在
// 一轮里被先后打成 pending_review_cycle 再由下一轮补成 expired）。
func (s *FreshnessReviewScheduler) sweepReviewDue(ctx context.Context, now time.Time) (sweepStats, error) {
	stats := sweepStats{}
	dueComparison, dueArgs := reviewDueComparison(s.db, now)
	cursor := ""
	for {
		if ctx.Err() != nil {
			return stats, ctx.Err()
		}
		var rows []*types.Knowledge
		query := s.db.WithContext(ctx).Model(&types.Knowledge{}).
			Where("lifecycle_status = ? AND review_cycle_days > 0 AND id > ? "+
				"AND (expires_at IS NULL OR expires_at >= ?) AND "+dueComparison,
				append([]interface{}{
					string(types.KnowledgeLifecycleValid), cursor, now,
				}, dueArgs...)...).
			Order("id").Limit(freshnessSweepBatchSize)
		if err := query.Find(&rows).Error; err != nil {
			return stats, err
		}
		if len(rows) == 0 {
			return stats, nil
		}
		for _, row := range rows {
			cursor = row.ID
			// Failures —— 行不被改动，次日重试，无副作用。
			if s.transition(ctx, row, types.KnowledgeLifecycleActionMarkPendingReviewCycle,
				auditActionSchedulerReviewDue, "scheduler: review cycle due") {
				stats.affected++
			} else {
				stats.failures++
			}
		}
		if len(rows) < freshnessSweepBatchSize {
			return stats, nil
		}
	}
}

// transition 走状态机应用一个动作并写审计。返回是否成功；失败记 WARN 由
// 上层计数 —— 调度清扫对单行失败必须容错继续。
func (s *FreshnessReviewScheduler) transition(
	ctx context.Context,
	row *types.Knowledge,
	action types.KnowledgeLifecycleAction,
	auditAction types.AuditAction,
	changeNote string,
) bool {
	_, err := s.lifecycle.Transition(ctx, &types.KnowledgeLifecycleTransitionInput{
		KnowledgeID: row.ID,
		Action:      action,
		ActorID:     freshnessSchedulerActor,
		ChangeNote:  changeNote,
	})
	if err != nil {
		logger.Warnf(ctx,
			"[freshness-scheduler] transition failed: knowledge=%s action=%s err=%v",
			row.ID, action, err)
		s.logAudit(ctx, row, auditAction, types.AuditOutcomeFailed, map[string]interface{}{
			"action": string(action),
			"error":  err.Error(),
		})
		return false
	}
	s.logAudit(ctx, row, auditAction, types.AuditOutcomeSuccess, map[string]interface{}{
		"action":     string(action),
		"from_state": row.LifecycleStatus,
		"trigger":    "scheduler",
	})
	return true
}

// logAudit 写一条调度审计事件。逐行写而非按批聚合：审计链按租户维护
// （audit_logs 的 hash chain），跨租户批次无法落到单一 TenantID。审计
// 失败只记日志，绝不阻塞清扫（与 lifecycle service 同策略）。
func (s *FreshnessReviewScheduler) logAudit(
	ctx context.Context,
	row *types.Knowledge,
	action types.AuditAction,
	outcome types.AuditOutcome,
	details map[string]interface{},
) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Log(ctx, &types.AuditLog{
		TenantID:    row.TenantID,
		ActorUserID: freshnessSchedulerActor,
		Action:      action,
		ScopeType:   "knowledge",
		ScopeID:     row.KnowledgeBaseID,
		TargetType:  "knowledge",
		TargetID:    row.ID,
		Outcome:     outcome,
		Details:     lifecycleJSON(details),
	})
}

// ScoreFor 返回单条知识的当前新鲜度评分。读取复用
// GetKnowledgeByIDOnly（不按租户过滤），访问控制由路由层
// KBAccessReadFromKnowledgeIDParam 先行完成。
func (s *FreshnessReviewScheduler) ScoreFor(
	ctx context.Context, knowledgeID string,
) (*types.FreshnessScore, error) {
	if s.kgRepo == nil {
		return nil, apperrors.NewInternalServerError("freshness service is not ready")
	}
	knowledge, err := s.kgRepo.GetKnowledgeByIDOnly(ctx, knowledgeID)
	if err != nil {
		return nil, err
	}
	score := types.FromKnowledge(knowledge, s.now())
	return &score, nil
}

// ReviewQueue 返回本租户待复审清单（§7.7）：已处于 pending_review_cycle
// 的行 + 尚为 valid 但显式过期已到或复审到期的行。SQL 级分页（count +
// offset/limit），排序按复审基线时间升序（最久未复审的排最前）。
func (s *FreshnessReviewScheduler) ReviewQueue(
	ctx context.Context, page *types.Pagination,
) (*types.PageResult, error) {
	caller := types.CallerFromContext(ctx)
	if caller.TenantID == 0 {
		return nil, apperrors.NewUnauthorizedError("tenant context is missing")
	}
	if page == nil {
		page = &types.Pagination{}
	}

	now := s.now().UTC()
	dueComparison, dueArgs := reviewDueComparison(s.db, now)

	scope := func(q *gorm.DB) *gorm.DB {
		return q.Model(&types.Knowledge{}).
			Where("tenant_id = ?", caller.TenantID).
			Where(
				"(lifecycle_status = ? "+
					"OR (lifecycle_status = ? AND expires_at IS NOT NULL AND expires_at < ?) "+
					"OR (lifecycle_status = ? AND review_cycle_days > 0 AND "+dueComparison+"))",
				append([]interface{}{
					string(types.KnowledgeLifecyclePendingReviewCycle),
					string(types.KnowledgeLifecycleValid), now,
					string(types.KnowledgeLifecycleValid),
				}, dueArgs...)...,
			)
	}

	var total int64
	if err := scope(s.db.WithContext(ctx)).Count(&total).Error; err != nil {
		return nil, err
	}

	var rows []*types.Knowledge
	if err := scope(s.db.WithContext(ctx)).
		Order("COALESCE(last_reviewed_at, created_at) ASC").Order("id ASC").
		Offset(page.Offset()).Limit(page.Limit()).
		Find(&rows).Error; err != nil {
		return nil, err
	}

	items := make([]*types.FreshnessReviewQueueItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, &types.FreshnessReviewQueueItem{
			Knowledge: row,
			Score:     types.FromKnowledge(row, s.now()),
			Reasons:   freshnessQueueReasons(row, now),
		})
	}
	return types.NewPageResult(total, page, items), nil
}

// freshnessQueueReasons 计算一条知识进入待复审队列的原因（机器可读）。
// 纯函数，与 types.FromKnowledge 同风格。
func freshnessQueueReasons(k *types.Knowledge, now time.Time) []string {
	reasons := make([]string, 0, 3)
	status := types.ParseKnowledgeLifecycleStatus(k.LifecycleStatus)
	if status == types.KnowledgeLifecyclePendingReviewCycle {
		reasons = append(reasons, "already_pending_review_cycle")
	}
	if k.ExpiresAt != nil && now.After(*k.ExpiresAt) {
		reasons = append(reasons, "expires_at_reached")
	}
	if k.ReviewCycleDays > 0 {
		baseline := k.CreatedAt
		if k.LastReviewedAt != nil {
			baseline = *k.LastReviewedAt
		}
		if now.Sub(baseline) > time.Duration(k.ReviewCycleDays)*24*time.Hour {
			reasons = append(reasons, "review_cycle_due")
			if k.LastReviewedAt == nil {
				reasons = append(reasons, "review_never_performed")
			}
		}
	}
	return reasons
}
