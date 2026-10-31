package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// FreshnessService 承载 KnowAxis V3.0 §7.9 新鲜度评分与 §7.7 复审调度
// 的查询/清扫入口。实现方 internal/application/service 里的
// FreshnessReviewScheduler 同时也是一个 Start/Stop 后台 runner（范式同
// AuditLogRetentionRunner）：定时把复审到期/显式过期的条目落库。
//
// 装配（container）：
//
//	must(container.Provide(service.NewFreshnessReviewScheduler))
//	must(container.Provide(func(s *service.FreshnessReviewScheduler) interfaces.FreshnessService { return s }))
type FreshnessService interface {
	// ScoreFor 返回单条知识当前时刻的新鲜度评分（§7.9 纯函数包装）。
	// 知识不存在时返回仓储层哨兵错误（repository.ErrKnowledgeNotFound）。
	ScoreFor(ctx context.Context, knowledgeID string) (*types.FreshnessScore, error)

	// ReviewQueue 返回本租户待复审清单（§7.7）：lifecycle 已处于
	// pending_review_cycle 的条目 + 尚为 valid 但复审到期或显式有效期
	// 已过的条目。分页；行内附当前评分与入队原因。
	// 租户上下文取自 ctx（types.CallerFromContext），路由层 g.Admin() 保证可信。
	ReviewQueue(ctx context.Context, page *types.Pagination) (*types.PageResult, error)

	// RunSweep 执行一次复审调度清扫并返回汇总：
	//   1) valid 且 expires_at 已过 → 显式落库 expired（读取时
	//      ResolveLifecycle 早已降级，此处是持久化批处理）；
	//   2) valid 且复审到期（last_reviewed_at/created_at 距今超出
	//      review_cycle_days）→ 落库 pending_review_cycle。
	// 两条路径都经 KnowledgeLifecycleService.Transition（状态机校验 +
	// 审计）。单条失败容错继续。Start() 起的 ticker 每 24h 调一次。
	RunSweep(ctx context.Context) (*types.FreshnessSweepResult, error)
}
