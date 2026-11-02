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
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// approvalSLASweepInterval 是 SLA 清扫间隔。审批时限以小时计，5 分钟一轮
// 意味着最坏情况下催办/升级延迟 5 分钟，与 §6.3 的治理期望相符，且扫描
// 只命中 sla_due 已进入窗口的少量行。
const approvalSLASweepInterval = 5 * time.Minute

// approvalSLASweepStartupDelay 首扫延迟：避开迁移与启动请求洪峰。
const approvalSLASweepStartupDelay = 2 * time.Minute

// approvalSLASweepEnabledEnvVar 运维开关（默认开启）。
const approvalSLASweepEnabledEnvVar = "WEKNORA_APPROVAL_SLA_SWEEP_ENABLED"

// ApprovalSLAScheduler runs the §6.3 SLA sweep on a ticker.
//
// The review flow has always computed an sla_due timestamp; without a consumer
// it was decoration. This scheduler is that consumer, kept deliberately small:
// the sweep logic and its idempotency live in ApprovalSLAService, and this type
// only owns the cadence and lifecycle (Start/Stop), mirroring the freshness
// scheduler's shape so operations has one pattern to reason about.
type ApprovalSLAScheduler struct {
	sla       interfaces.ApprovalSLAService
	startOnce sync.Once
	stopOnce  sync.Once
	started   atomic.Bool
	stopCh    chan struct{}
	doneCh    chan struct{}
}

// NewApprovalSLAScheduler constructs the scheduler. Nothing runs until Start.
func NewApprovalSLAScheduler(sla interfaces.ApprovalSLAService) *ApprovalSLAScheduler {
	return &ApprovalSLAScheduler{
		sla:    sla,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
}

// Start launches the sweep goroutine; repeated calls are a no-op.
func (s *ApprovalSLAScheduler) Start(ctx context.Context) {
	if s == nil || s.sla == nil {
		return
	}
	s.startOnce.Do(func() {
		s.started.Store(true)
		if !approvalSLASweepEnabled() {
			logger.Infof(ctx, "[approval-sla-scheduler] disabled via %s", approvalSLASweepEnabledEnvVar)
			close(s.doneCh)
			return
		}
		logger.Infof(ctx,
			"[approval-sla-scheduler] starting sweep: interval=%s startup_delay=%s",
			approvalSLASweepInterval, approvalSLASweepStartupDelay)
		go s.loop()
	})
}

// Stop asks the loop to exit and waits; idempotent.
func (s *ApprovalSLAScheduler) Stop() {
	s.StopWithin(0)
}

// StopWithin is Stop with an upper bound; timeout <= 0 waits indefinitely.
func (s *ApprovalSLAScheduler) StopWithin(timeout time.Duration) {
	if s == nil || !s.started.Load() {
		return
	}
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
	if runtime.WaitFor(s.doneCh, timeout) {
		return
	}
	logger.Warnf(context.Background(),
		"[approval-sla-scheduler] sweep still running after %s; continuing shutdown", timeout)
}

// loop runs one sweep after the startup delay, then on each tick.
func (s *ApprovalSLAScheduler) loop() {
	defer close(s.doneCh)

	startup := time.NewTimer(approvalSLASweepStartupDelay)
	defer startup.Stop()
	select {
	case <-s.stopCh:
		return
	case <-startup.C:
	}

	s.sweep()
	ticker := time.NewTicker(approvalSLASweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.sweep()
		}
	}
}

// sweep runs one pass with its own context: the Start context is cancelled as
// soon as container init returns.
func (s *ApprovalSLAScheduler) sweep() {
	ctx, cancel := context.WithTimeout(context.Background(), approvalSLASweepInterval)
	defer cancel()
	reminded, escalated, err := s.sla.SweepOnce(ctx)
	if err != nil {
		logger.Warnf(ctx, "[approval-sla-scheduler] sweep failed: %v", err)
		return
	}
	if reminded > 0 || escalated > 0 {
		logger.Infof(ctx, "[approval-sla-scheduler] reminded=%d escalated=%d", reminded, escalated)
	}
}

// approvalSLASweepEnabled reads the operational switch; unset or malformed
// defaults to enabled.
func approvalSLASweepEnabled() bool {
	raw, ok := os.LookupEnv(approvalSLASweepEnabledEnvVar)
	if !ok || strings.TrimSpace(raw) == "" {
		return true
	}
	enabled, err := strconv.ParseBool(raw)
	if err != nil {
		return true
	}
	return enabled
}
