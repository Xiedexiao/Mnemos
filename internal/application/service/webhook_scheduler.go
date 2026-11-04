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

// 出站 Webhook 投递调度（KnowAxis V3.0 §16.4 签名与重试）。
//
// 每 30 秒扫一次到期的 pending 行。节拍比重试退避的最短档（30s）一致：更快
// 只会产生空扫，更慢会让首投延迟变大。
type WebhookDeliveryScheduler struct {
	svc interfaces.WebhookService

	startOnce sync.Once
	stopOnce  sync.Once
	stopCh    chan struct{}
	doneCh    chan struct{}
	started   atomic.Bool
}

// 投递参数。
const (
	// webhookDeliveryInterval 是扫描节拍。
	webhookDeliveryInterval = 30 * time.Second
	// webhookDeliveryStartupDelay 避开启动洪峰。
	webhookDeliveryStartupDelay = 30 * time.Second
	// webhookDeliveryBatch 是单轮上限。
	webhookDeliveryBatch = 100
	// webhookDeliveryTimeout 是单轮整体预算。
	webhookDeliveryTimeout = 5 * time.Minute
	// webhookSchedulerEnabledEnvVar 运维开关（默认开启）。
	webhookSchedulerEnabledEnvVar = "WEBHOOK_DELIVERY_ENABLED"
)

// NewWebhookDeliveryScheduler constructs the scheduler.
func NewWebhookDeliveryScheduler(svc interfaces.WebhookService) *WebhookDeliveryScheduler {
	return &WebhookDeliveryScheduler{
		svc:    svc,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
}

// Start launches the delivery loop.
func (s *WebhookDeliveryScheduler) Start(ctx context.Context) {
	if s == nil || s.svc == nil {
		return
	}
	s.startOnce.Do(func() {
		s.started.Store(true)
		if !webhookSchedulerEnabled() {
			logger.Infof(ctx, "[webhook] delivery scheduler disabled via %s",
				webhookSchedulerEnabledEnvVar)
			close(s.doneCh)
			return
		}
		logger.Infof(ctx, "[webhook] starting delivery loop: interval=%s", webhookDeliveryInterval)
		go s.loop()
	})
}

// Stop signals the loop and waits for it.
func (s *WebhookDeliveryScheduler) Stop() { s.StopWithin(0) }

// StopWithin is Stop with a wait bound.
func (s *WebhookDeliveryScheduler) StopWithin(timeout time.Duration) {
	if s == nil || !s.started.Load() {
		return
	}
	s.stopOnce.Do(func() { close(s.stopCh) })
	if runtime.WaitFor(s.doneCh, timeout) {
		return
	}
	logger.Warnf(context.Background(),
		"[webhook] delivery loop still running after %s; continuing shutdown", timeout)
}

// loop runs one pass after the startup delay, then on the interval.
func (s *WebhookDeliveryScheduler) loop() {
	defer close(s.doneCh)

	startupTimer := time.NewTimer(webhookDeliveryStartupDelay)
	defer startupTimer.Stop()
	select {
	case <-startupTimer.C:
	case <-s.stopCh:
		return
	}
	s.runOnce()

	ticker := time.NewTicker(webhookDeliveryInterval)
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

// runOnce performs one dispatch pass.
func (s *WebhookDeliveryScheduler) runOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), webhookDeliveryTimeout)
	defer cancel()
	result, err := s.RunOnce(ctx)
	if err != nil {
		logger.Warnf(ctx, "[webhook] delivery pass failed: %v", err)
		return
	}
	if result.Attempted == 0 {
		return
	}
	logger.Infof(ctx,
		"[webhook] delivery pass complete: attempted=%d delivered=%d retrying=%d dead=%d",
		result.Attempted, result.Delivered, result.Retrying, result.Dead)
}

// RunOnce exposes one pass for tests and operator-triggered runs.
func (s *WebhookDeliveryScheduler) RunOnce(ctx context.Context) (
	*types.WebhookSweepResult, error,
) {
	if s == nil || s.svc == nil {
		return &types.WebhookSweepResult{}, nil
	}
	return s.svc.DeliverDue(ctx, webhookDeliveryBatch)
}

// webhookSchedulerEnabled reads the ops switch, defaulting to on.
func webhookSchedulerEnabled() bool {
	raw, ok := os.LookupEnv(webhookSchedulerEnabledEnvVar)
	if !ok || strings.TrimSpace(raw) == "" {
		return true
	}
	enabled, err := strconv.ParseBool(raw)
	if err != nil {
		return true
	}
	return enabled
}
