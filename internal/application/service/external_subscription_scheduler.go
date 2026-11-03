package service

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/runtime"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// 外部订阅采集调度（KnowAxis V3.0 §7.3）。
//
// 每个订阅源自带检查周期（默认 24h），调度器每小时扫一次"哪些源到期了"。
// 小时级 ticker 而不是为每个源起定时器：源数量是企业级配置的个位数，起
// 几百个定时器没有收益，反而让运行状态难观测。
type ExternalSubscriptionScheduler struct {
	svc interfaces.ExternalSubscriptionService

	startOnce sync.Once
	stopOnce  sync.Once
	stopCh    chan struct{}
	doneCh    chan struct{}
	started   atomic.Bool
}

// 调度参数。
const (
	// externalSubscriptionSweepInterval 是"检查哪些源到期"的节奏。源本身
	// 的周期最短 1h，所以小时级扫描不会漏。
	externalSubscriptionSweepInterval = time.Hour
	// externalSubscriptionSweepStartupDelay 避开启动请求洪峰。
	externalSubscriptionSweepStartupDelay = 2 * time.Minute
	// externalSubscriptionSweepTimeout 单轮整体预算；单源另有 2min 上限。
	externalSubscriptionSweepTimeout = 15 * time.Minute
	// externalSubscriptionSchedulerEnabledEnvVar 运维开关（默认开启）。
	externalSubscriptionSchedulerEnabledEnvVar = "EXTERNAL_SUBSCRIPTION_SCHEDULER_ENABLED"
)

// NewExternalSubscriptionScheduler constructs the scheduler.
func NewExternalSubscriptionScheduler(
	svc interfaces.ExternalSubscriptionService,
) *ExternalSubscriptionScheduler {
	return &ExternalSubscriptionScheduler{
		svc:    svc,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
}

// Start launches the background sweep. Repeated calls are no-ops.
func (s *ExternalSubscriptionScheduler) Start(ctx context.Context) {
	if s == nil || s.svc == nil {
		return
	}
	s.startOnce.Do(func() {
		s.started.Store(true)
		if !externalSubscriptionSchedulerEnabled() {
			logger.Infof(ctx, "[external-subscription] scheduler disabled via %s",
				externalSubscriptionSchedulerEnabledEnvVar)
			close(s.doneCh)
			return
		}
		logger.Infof(ctx,
			"[external-subscription] starting sweep: interval=%s startup_delay=%s",
			externalSubscriptionSweepInterval, externalSubscriptionSweepStartupDelay)
		go s.loop()
	})
}

// Stop signals the loop and waits for it. Safe to call without Start.
func (s *ExternalSubscriptionScheduler) Stop() {
	s.StopWithin(0)
}

// StopWithin is Stop with a wait bound; timeout <= 0 waits indefinitely.
func (s *ExternalSubscriptionScheduler) StopWithin(timeout time.Duration) {
	if s == nil || !s.started.Load() {
		return
	}
	s.stopOnce.Do(func() { close(s.stopCh) })
	if runtime.WaitFor(s.doneCh, timeout) {
		return
	}
	logger.Warnf(context.Background(),
		"[external-subscription] sweep still running after %s; continuing shutdown", timeout)
}

// loop runs one sweep after the startup delay, then hourly.
func (s *ExternalSubscriptionScheduler) loop() {
	defer close(s.doneCh)

	startupTimer := time.NewTimer(externalSubscriptionSweepStartupDelay)
	defer startupTimer.Stop()
	select {
	case <-startupTimer.C:
	case <-s.stopCh:
		return
	}

	s.runOnce()

	ticker := time.NewTicker(externalSubscriptionSweepInterval)
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

// runOnce performs one sweep with an overall budget; failures are logged.
func (s *ExternalSubscriptionScheduler) runOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), externalSubscriptionSweepTimeout)
	defer cancel()

	result, err := s.RunOnce(ctx)
	if err != nil {
		logger.Warnf(ctx, "[external-subscription] sweep failed: %v", err)
		return
	}
	if result.FeedsChecked == 0 {
		logger.Debugf(ctx, "[external-subscription] sweep complete: no feed due")
		return
	}
	logger.Infof(ctx,
		"[external-subscription] sweep complete: checked=%d failed=%d new_entries=%d signals=%d",
		result.FeedsChecked, result.FeedsFailed, result.NewEntries, result.Signals)
	for _, sweepErr := range result.Errors {
		logger.Warnf(ctx, "[external-subscription] feed error: %s", sweepErr)
	}
}

// RunOnce exposes one sweep for tests and for an operator-triggered run.
func (s *ExternalSubscriptionScheduler) RunOnce(
	ctx context.Context,
) (*types.ExternalSubscriptionSweepResult, error) {
	if s == nil || s.svc == nil {
		return &types.ExternalSubscriptionSweepResult{CheckedAt: time.Now().UTC()}, nil
	}
	return s.svc.SyncDue(ctx)
}

// externalSubscriptionSchedulerEnabled reads the ops switch, defaulting to on.
func externalSubscriptionSchedulerEnabled() bool {
	raw, ok := os.LookupEnv(externalSubscriptionSchedulerEnabledEnvVar)
	if !ok || strings.TrimSpace(raw) == "" {
		return true
	}
	enabled, err := strconv.ParseBool(raw)
	if err != nil {
		return true
	}
	return enabled
}
