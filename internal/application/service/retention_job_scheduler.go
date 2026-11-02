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
)

// retentionJobInterval is how often the retention/health jobs run. Both operate
// on day-scale data (30-day recycle window, daily health snapshots), so an
// hourly tick is generous and keeps the SQL cost invisible.
const retentionJobInterval = time.Hour

// retentionJobStartupDelay avoids competing with migrations and the startup
// request burst.
const retentionJobStartupDelay = 5 * time.Minute

// retentionJobEnabledEnvVar is the operational switch (default on).
const retentionJobEnabledEnvVar = "WEKNORA_RETENTION_JOBS_ENABLED"

// RetentionJobScheduler runs the maintenance jobs that had no caller.
//
// Two capabilities were fully implemented and tested but never scheduled:
//
//	RecycleBinService.PurgeDue        — §5.5 "到期进入归档或销毁审批"
//	KnowledgeHealthService.CronComputeAll — §14.6 health snapshots
//
// Without a caller the 30-day recycle window never expired (deleted content
// stayed restorable forever, quietly keeping storage and legal exposure) and
// health dashboards only ever showed whatever someone had computed by hand.
//
// The library services are injected as narrow function values rather than the
// full interfaces: this scheduler must not influence what those services do, and
// a caller that only needs "purge now" should not pull in an interface with
// thirteen unrelated methods.
type RetentionJobScheduler struct {
	purgeRecycleBin func(ctx context.Context, now time.Time) (int64, error)
	computeHealth   func(ctx context.Context, tenantID uint64) error
	listTenants     func(ctx context.Context) ([]uint64, error)

	startOnce sync.Once
	stopOnce  sync.Once
	started   atomic.Bool
	stopCh    chan struct{}
	doneCh    chan struct{}
	now       func() time.Time
}

// NewRetentionJobScheduler constructs the scheduler. Any nil dependency makes
// the corresponding job a no-op rather than a startup failure: Lite mode may
// not have every service wired.
func NewRetentionJobScheduler(
	purgeRecycleBin func(ctx context.Context, now time.Time) (int64, error),
	computeHealth func(ctx context.Context, tenantID uint64) error,
	listTenants func(ctx context.Context) ([]uint64, error),
) *RetentionJobScheduler {
	return &RetentionJobScheduler{
		purgeRecycleBin: purgeRecycleBin,
		computeHealth:   computeHealth,
		listTenants:     listTenants,
		stopCh:          make(chan struct{}),
		doneCh:          make(chan struct{}),
		now:             time.Now,
	}
}

// Start launches the job loop; repeated calls are a no-op.
func (s *RetentionJobScheduler) Start(ctx context.Context) {
	if s == nil {
		return
	}
	s.startOnce.Do(func() {
		s.started.Store(true)
		if !retentionJobsEnabled() {
			logger.Infof(ctx, "[retention-jobs] disabled via %s", retentionJobEnabledEnvVar)
			close(s.doneCh)
			return
		}
		logger.Infof(ctx, "[retention-jobs] starting: interval=%s startup_delay=%s",
			retentionJobInterval, retentionJobStartupDelay)
		go s.loop()
	})
}

// Stop asks the loop to exit and waits; idempotent.
func (s *RetentionJobScheduler) Stop() { s.StopWithin(0) }

// StopWithin is Stop with an upper bound; timeout <= 0 waits indefinitely.
func (s *RetentionJobScheduler) StopWithin(timeout time.Duration) {
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
		"[retention-jobs] still running after %s; continuing shutdown", timeout)
}

// loop runs one pass after the startup delay, then on each tick.
func (s *RetentionJobScheduler) loop() {
	defer close(s.doneCh)

	startup := time.NewTimer(retentionJobStartupDelay)
	defer startup.Stop()
	select {
	case <-s.stopCh:
		return
	case <-startup.C:
	}

	s.runOnce()
	ticker := time.NewTicker(retentionJobInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.runOnce()
		}
	}
}

// runOnce executes both jobs with a bounded context.
func (s *RetentionJobScheduler) runOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), retentionJobInterval)
	defer cancel()

	if s.purgeRecycleBin != nil {
		purged, err := s.purgeRecycleBin(ctx, s.now())
		if err != nil {
			logger.Warnf(ctx, "[retention-jobs] recycle purge failed: %v", err)
		} else if purged > 0 {
			logger.Infof(ctx, "[retention-jobs] purged %d expired recycle entr(ies)", purged)
		}
	}

	if s.computeHealth == nil || s.listTenants == nil {
		return
	}
	tenantIDs, err := s.listTenants(ctx)
	if err != nil {
		logger.Warnf(ctx, "[retention-jobs] tenant enumeration failed: %v", err)
		return
	}
	for _, tenantID := range tenantIDs {
		if err := s.computeHealth(ctx, tenantID); err != nil {
			// 单个租户失败不阻断其余：健康分是运营指标，不是业务前置条件。
			logger.Warnf(ctx, "[retention-jobs] health snapshot failed for tenant %d: %v", tenantID, err)
		}
	}
}

// retentionJobsEnabled reads the operational switch; unset or malformed means
// enabled.
func retentionJobsEnabled() bool {
	raw, ok := os.LookupEnv(retentionJobEnabledEnvVar)
	if !ok || strings.TrimSpace(raw) == "" {
		return true
	}
	enabled, err := strconv.ParseBool(raw)
	if err != nil {
		return true
	}
	return enabled
}
