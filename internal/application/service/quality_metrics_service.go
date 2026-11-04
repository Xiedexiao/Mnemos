package service

import (
	"context"
	"time"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// qualityMetricsService implements interfaces.QualityMetricsService
// (KnowAxis V3.0 §14.7 质量运营指标).
type qualityMetricsService struct {
	repo interfaces.QualityMetricsRepository
	now  func() time.Time
}

// NewQualityMetricsService constructs the service.
func NewQualityMetricsService(
	repo interfaces.QualityMetricsRepository,
) interfaces.QualityMetricsService {
	return &qualityMetricsService{repo: repo, now: time.Now}
}

// Get computes the three metric groups.
//
// 每一组都独立取数：某一组的查询失败不该让另外两组也变成空白——运营人员
// 看到"准确性有数、价值取不到"远好过整页空白。取不到的组返回带 Note 的空
// 比率，如实说明是"取数失败"而不是"没有样本"。
func (s *qualityMetricsService) Get(
	ctx context.Context, tenantID uint64, days int,
) (*types.QualityMetrics, error) {
	if tenantID == 0 {
		return nil, apperrors.NewUnauthorizedError("tenant context is required")
	}
	window := types.BuildQualityWindow(s.now(), days)
	metrics := &types.QualityMetrics{Window: window, GeneratedAt: window.Until}

	s.fillTimeliness(ctx, tenantID, window, metrics)
	s.fillAccuracy(ctx, tenantID, window, metrics)
	s.fillValue(ctx, tenantID, window, metrics)
	return metrics, nil
}

// fillTimeliness computes the §14.7 及时性 group.
func (s *qualityMetricsService) fillTimeliness(
	ctx context.Context, tenantID uint64, window types.QualityMetricWindow,
	metrics *types.QualityMetrics,
) {
	pairs, err := s.repo.SignalToUpdatePairs(ctx, tenantID, window.Since, 2000)
	if err != nil {
		logger.Warnf(ctx, "[quality-metrics] signal latency failed: %v", err)
		metrics.Timeliness.SourceToUpdateHours = types.QualityRatio{
			Unit: "hours", Note: "取数失败",
		}
	} else {
		metrics.Timeliness.SourceToUpdateHours = types.MeanHours(pairs)
	}

	onTime, completed, err := s.repo.ReviewOnTime(ctx, tenantID, window.Since)
	if err != nil {
		logger.Warnf(ctx, "[quality-metrics] review timeliness failed: %v", err)
		metrics.Timeliness.ReviewOnTimeRate = types.QualityRatio{Unit: "%", Note: "取数失败"}
	} else {
		metrics.Timeliness.ReviewOnTimeRate = types.NewQualityRatio(onTime, completed, "%")
		if completed == 0 {
			metrics.Timeliness.ReviewOnTimeRate.Note =
				"窗口内没有设定了时限并完成的复审，不计算按时率"
		}
	}

	samples, err := s.repo.CorrectionCloseDurations(ctx, tenantID, window.Since, 2000)
	if err != nil {
		logger.Warnf(ctx, "[quality-metrics] correction close time failed: %v", err)
		metrics.Timeliness.CorrectionCloseHours = types.QualityRatio{
			Unit: "hours", Note: "取数失败",
		}
		return
	}
	metrics.Timeliness.CorrectionCloseHours = types.MeanHours(samples)
}

// fillAccuracy computes the §14.7 准确性 group.
func (s *qualityMetricsService) fillAccuracy(
	ctx context.Context, tenantID uint64, window types.QualityMetricWindow,
	metrics *types.QualityMetrics,
) {
	published, err := s.repo.CountPublishedKnowledge(ctx, tenantID)
	if err != nil {
		logger.Warnf(ctx, "[quality-metrics] published count failed: %v", err)
	}
	flagged, flagErr := s.repo.CountErrorPublished(ctx, tenantID, window.Since)
	if flagErr != nil {
		logger.Warnf(ctx, "[quality-metrics] error-publish count failed: %v", flagErr)
		metrics.Accuracy.ErrorPublishRate = types.QualityRatio{Unit: "%", Note: "取数失败"}
	} else {
		metrics.Accuracy.ErrorPublishRate = types.NewQualityRatio(flagged, published, "%")
		if published == 0 {
			metrics.Accuracy.ErrorPublishRate.Note = "窗口内没有已发布知识，不计算错误发布率"
		}
	}

	passed, failed, samplingErr := s.repo.SamplingStats(ctx, tenantID, window.Since)
	if samplingErr != nil {
		logger.Warnf(ctx, "[quality-metrics] sampling stats failed: %v", samplingErr)
		metrics.Accuracy.SamplingFailRate = types.QualityRatio{Unit: "%", Note: "取数失败"}
	} else {
		// 不合格率 = failed / (passed + failed)：pending 与 waived 不计入，
		// 它们还没形成结论。
		metrics.Accuracy.SamplingFailRate = types.NewQualityRatio(failed, passed+failed, "%")
		if passed+failed == 0 {
			metrics.Accuracy.SamplingFailRate.Note = "窗口内没有已结案的抽检"
		}
	}

	withCitations, answers, citationErr := s.repo.AnswerCounts(ctx, tenantID, window.Since)
	if citationErr != nil {
		logger.Warnf(ctx, "[quality-metrics] citation coverage failed: %v", citationErr)
		metrics.Accuracy.CitationCoverageRate = types.QualityRatio{Unit: "%", Note: "取数失败"}
	} else {
		metrics.Accuracy.CitationCoverageRate = types.NewQualityRatio(withCitations, answers, "%")
		if answers == 0 {
			metrics.Accuracy.CitationCoverageRate.Note = "窗口内没有回答，不计算引用覆盖率"
		}
	}
}

// fillValue computes the §14.7 价值 group.
func (s *qualityMetricsService) fillValue(
	ctx context.Context, tenantID uint64, window types.QualityMetricWindow,
	metrics *types.QualityMetrics,
) {
	published, err := s.repo.CountPublishedKnowledge(ctx, tenantID)
	if err != nil {
		logger.Warnf(ctx, "[quality-metrics] published count failed: %v", err)
	}
	used, usedErr := s.repo.CountUsedKnowledge(ctx, tenantID, window.Since)
	if usedErr != nil {
		logger.Warnf(ctx, "[quality-metrics] used count failed: %v", usedErr)
		metrics.Value.ReuseRate = types.QualityRatio{Unit: "%", Note: "取数失败"}
	} else {
		metrics.Value.ReuseRate = types.NewQualityRatio(used, published, "%")
		if published == 0 {
			metrics.Value.ReuseRate.Note = "没有可复用的已发布知识"
		}
	}

	useful, rated, feedbackErr := s.repo.FeedbackResolution(ctx, tenantID, window.Since)
	if feedbackErr != nil {
		logger.Warnf(ctx, "[quality-metrics] feedback resolution failed: %v", feedbackErr)
		metrics.Value.ResolutionRate = types.QualityRatio{Unit: "%", Note: "取数失败"}
	} else {
		metrics.Value.ResolutionRate = types.NewQualityRatio(useful, rated, "%")
		if rated == 0 {
			metrics.Value.ResolutionRate.Note = "窗口内没有评价，不计算解决率"
		}
	}

	// 重复问题下降：与上一等长窗口比较。
	previousSince := window.Since.AddDate(0, 0, -window.Days)
	currentRepeats, currentErr := s.repo.RepeatedZeroResultQueries(
		ctx, tenantID, window.Since, window.Until)
	previousRepeats, previousErr := s.repo.RepeatedZeroResultQueries(
		ctx, tenantID, previousSince, window.Since)
	if currentErr != nil || previousErr != nil {
		logger.Warnf(ctx, "[quality-metrics] repeated zero-result failed: %v/%v", currentErr, previousErr)
		metrics.Value.RepeatedQuestionDrop = types.QualityRatio{Unit: "%", Note: "取数失败"}
	} else {
		metrics.Value.RepeatedQuestionDrop = types.RepeatedQuestionDrop(currentRepeats, previousRepeats)
	}

	created, closed, gapErr := s.repo.GapCounts(ctx, tenantID, window.Since, window.Until)
	if gapErr != nil {
		logger.Warnf(ctx, "[quality-metrics] gap counts failed: %v", gapErr)
		metrics.Value.GapCloseRate = types.QualityRatio{Unit: "%", Note: "取数失败"}
	} else if created == 0 && closed > 0 {
		// 当期没有新增但关掉了存量缺口：这是好事，但"关闭率"无定义。
		metrics.Value.GapCloseRate = types.QualityRatio{
			Numerator: closed, Denominator: 0, Unit: "%",
			Note: "窗口内没有新增缺口，仅关闭了存量缺口",
		}
	} else {
		metrics.Value.GapCloseRate = types.NewQualityRatio(closed, created, "%")
		if created == 0 {
			metrics.Value.GapCloseRate.Note = "窗口内没有新增缺口"
		}
	}
}

// compile-time guard.
var _ interfaces.QualityMetricsService = (*qualityMetricsService)(nil)
