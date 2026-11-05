package service

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// indexCheckService implements interfaces.IndexCheckService
// (KnowAxis V3.0 §17.1 性能自检 / §17.4 可运维性).
//
// 它回答一个压测回答不了的问题：**哪条关键查询会随数据量线性变差**。
// 压测告诉你"现在慢"，索引检查告诉你"明天会更慢，以及为什么"。
type indexCheckService struct {
	repo interfaces.IndexCheckRepository
	now  func() time.Time
}

// NewIndexCheckService constructs the service.
func NewIndexCheckService(repo interfaces.IndexCheckRepository) interfaces.IndexCheckService {
	return &indexCheckService{repo: repo, now: time.Now}
}

// Run executes every configured check.
//
// 单条检查失败不中断整轮：一条查询报错只把那条标成 unknown，其余照常给出
// 结论。"因为一条查询写错了所以整份自检没结果"会让这个功能被弃用。
func (s *indexCheckService) Run(ctx context.Context) (*types.IndexCheckReport, error) {
	dialect := s.repo.Dialect()
	checks := types.IndexCheckQueries()
	report := &types.IndexCheckReport{
		Dialect:   dialect,
		Results:   make([]types.IndexCheckResult, 0, len(checks)),
		CheckedAt: s.now().UTC().Format(time.RFC3339),
	}
	for _, check := range checks {
		plan, err := s.repo.Explain(ctx, check)
		if err != nil {
			// 表不存在或 SQL 写错：标 unknown 并带上错误，不当作通过。
			logger.Warnf(ctx, "[index-check] %s failed: %v", check.Name, err)
			report.Results = append(report.Results, types.IndexCheckResult{
				Name: check.Name, Verdict: types.IndexCheckUnknown,
				Why:    check.Why,
				Advice: err.Error(),
			})
			report.Unknown++
			continue
		}
		result := types.ParseIndexPlan(dialect, plan)
		result.Name = check.Name
		result.Why = check.Why
		switch result.Verdict {
		case types.IndexCheckFullScan:
			report.FullScans++
		case types.IndexCheckUnknown:
			report.Unknown++
		}
		report.Results = append(report.Results, result)
	}
	return report, nil
}

// compile-time guard.
var _ interfaces.IndexCheckService = (*indexCheckService)(nil)
