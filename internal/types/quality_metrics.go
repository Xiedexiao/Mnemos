package types

import (
	"sort"
	"time"
)

// ============================================================================
// 质量运营指标（KnowAxis V3.0 §14.7 "质量运营指标"）。
//
// §14.7 把质量运营指标分成三组：
//   及时性：源变更→知识更新时延、复审按时率、纠错关闭时长
//   准确性：错误发布率、抽检不合格率、回答引用覆盖率
//   价值：复用率、解决率、重复问题下降、知识缺口关闭率
//
// 平台此前没有任何一组指标接口：健康分只看存量结构（§14.6），而"运营得
// 怎么样"必须看**流转**——多快、多准、有没有用。两者的区别是：
// 健康分回答"资产现在什么状态"，运营指标回答"治理动作是否在起作用"。
//
// 四个设计约束：
//  1. 每个指标都带分子/分母，不返回裸比率。0.8 可能是 4/5 也可能是 800/1000，
//     前者不能作为运营结论。分母为 0 时返回 nil 而不是 0——"没有样本"和
//     "全部失败"是完全不同的两件事。
//  2. 时间窗口显式传入并回显，避免"这个数是什么时间段的"无从判断。
//  3. 指标口径写进代码注释（每个字段说明它到底数什么），因为质量指标最
//     容易在多次改动后悄悄改变含义。
//  4. 缺口关闭率是**存量**口径（当期关闭 / 当期新增），不是累计：累计关闭率
//     会随时间单调趋近 1，失去指导意义。
// ============================================================================

// QualityMetricWindow is the look-back window the metrics cover.
type QualityMetricWindow struct {
	// Days is the window length.
	Days int `json:"days"`
	// Since / Until are the resolved bounds (UTC).
	Since time.Time `json:"since"`
	Until time.Time `json:"until"`
}

// QualityRatio is one metric value with its evidence.
//
// Nil means "no sample in the window". Reporting 0 would claim the metric was
// measured and came out at zero, which is a different statement.
type QualityRatio struct {
	// Value is numerator / denominator, nil when the denominator is 0.
	Value *float64 `json:"value"`
	// Numerator / Denominator are the raw counts behind the value.
	Numerator   int64 `json:"numerator"`
	Denominator int64 `json:"denominator"`
	// Unit renders the metric for display ("hours", "%", "×").
	Unit string `json:"unit"`
	// Note explains a zero-denominator or a known caveat.
	Note string `json:"note,omitempty"`
}

// NewQualityRatio builds a ratio, leaving Value nil when there is no sample.
func NewQualityRatio(numerator, denominator int64, unit string) QualityRatio {
	ratio := QualityRatio{Numerator: numerator, Denominator: denominator, Unit: unit}
	if denominator == 0 {
		ratio.Note = "窗口内没有样本，不计算比率"
		return ratio
	}
	value := float64(numerator) / float64(denominator)
	ratio.Value = &value
	return ratio
}

// NewQualityDuration builds a duration metric in hours.
func NewQualityDuration(totalHours float64, samples int64) QualityRatio {
	ratio := QualityRatio{Numerator: samples, Denominator: samples, Unit: "hours"}
	if samples == 0 {
		ratio.Note = "窗口内没有样本，不计算时长"
		return ratio
	}
	value := totalHours / float64(samples)
	ratio.Value = &value
	return ratio
}

// TimelinessMetrics is the §14.7 及时性 group.
type TimelinessMetrics struct {
	// SourceToUpdateHours 源变更→知识更新时延：以变更信号的采集时间到对应
	// 变更单发布时间的平均小时数计。没有变更单的信号不计入（它还没闭环）。
	SourceToUpdateHours QualityRatio `json:"source_to_update_hours"`
	// ReviewOnTimeRate 复审按时率：窗口内完成的复审中，在 due 之前完成的
	// 占比。逾期完成后才做的不算按时。
	ReviewOnTimeRate QualityRatio `json:"review_on_time_rate"`
	// CorrectionCloseHours 纠错关闭时长：窗口内关闭的纠错类质量任务，从创建
	// 到关闭的平均小时数。
	CorrectionCloseHours QualityRatio `json:"correction_close_hours"`
}

// AccuracyMetrics is the §14.7 准确性 group.
type AccuracyMetrics struct {
	// ErrorPublishRate 错误发布率：已发布内容里被事后判定为错误的比例。
	// 口径 = 抽检不通过 + 纠错任务确认有误 / 已发布总量。
	ErrorPublishRate QualityRatio `json:"error_publish_rate"`
	// SamplingFailRate 抽检不合格率：窗口内已结案抽检中 failed 的占比。
	SamplingFailRate QualityRatio `json:"sampling_fail_rate"`
	// CitationCoverageRate 回答引用覆盖率：窗口内有引用的回答占全部回答的
	// 比例。拒答不计入分母——拒答本来就没有引用。
	CitationCoverageRate QualityRatio `json:"citation_coverage_rate"`
}

// ValueMetrics is the §14.7 价值 group.
type ValueMetrics struct {
	// ReuseRate 复用率：窗口内被至少引用/阅读过一次的已发布知识占比。
	ReuseRate QualityRatio `json:"reuse_rate"`
	// ResolutionRate 解决率：窗口内有"有用"反馈的回答占比（分母为有反馈的
	// 回答，不是全部回答——没人评价不等于没解决）。
	ResolutionRate QualityRatio `json:"resolution_rate"`
	// RepeatedQuestionDrop 重复问题下降：窗口内零结果查询的重复次数相对
	// 上一等长窗口的下降比例。正数表示下降。
	RepeatedQuestionDrop QualityRatio `json:"repeated_question_drop"`
	// GapCloseRate 缺口关闭率：窗口内关闭的缺口 / 窗口内新增的缺口。
	GapCloseRate QualityRatio `json:"gap_close_rate"`
}

// QualityMetrics is the composed §14.7 payload.
type QualityMetrics struct {
	// Window the numbers cover.
	Window QualityMetricWindow `json:"window"`
	// Timeliness / Accuracy / Value are the three groups.
	Timeliness TimelinessMetrics `json:"timeliness"`
	Accuracy   AccuracyMetrics   `json:"accuracy"`
	Value      ValueMetrics      `json:"value"`
	// GeneratedAt is the computation time.
	GeneratedAt time.Time `json:"generated_at"`
}

// KnowledgeUsageSignal is one knowledge item's usage counters, used for the
// reuse-rate numerator.
type KnowledgeUsageSignal struct {
	KnowledgeID string
	// Used counts reads plus citations in the window.
	Used int64
}

// ClampQualityWindow normalizes the requested window.
func ClampQualityWindow(days int) int {
	if days <= 0 {
		return 30
	}
	if days > 365 {
		return 365
	}
	return days
}

// BuildQualityWindow resolves the window bounds.
func BuildQualityWindow(now time.Time, days int) QualityMetricWindow {
	days = ClampQualityWindow(days)
	until := now.UTC()
	return QualityMetricWindow{
		Days: days, Since: until.AddDate(0, 0, -days), Until: until,
	}
}

// ComputeReuseRate turns usage counters into the reuse metric.
//
// 分母是"窗口内已发布的知识总数"，分子是"其中至少被用过一次的"。只统计
// 已发布内容：草稿与归档没有人能用，把它们算进分母会让复用率永远偏低。
func ComputeReuseRate(signals []KnowledgeUsageSignal, publishedTotal int64) QualityRatio {
	var used int64
	for _, signal := range signals {
		if signal.Used > 0 {
			used++
		}
	}
	return NewQualityRatio(used, publishedTotal, "%")
}

// SortUsageSignals orders counters descending, for contributor views.
func SortUsageSignals(signals []KnowledgeUsageSignal) {
	sort.SliceStable(signals, func(i, j int) bool {
		if signals[i].Used != signals[j].Used {
			return signals[i].Used > signals[j].Used
		}
		return signals[i].KnowledgeID < signals[j].KnowledgeID
	})
}

// RepeatedQuestionDrop computes the drop between two windows.
//
// 上一窗口 0 次重复而本窗口有重复时返回 0（没有基线可比，不能声称"下降
// -100%"）；两窗口都为 0 时也没有基线，同样返回 nil。
func RepeatedQuestionDrop(current, previous int64) QualityRatio {
	ratio := QualityRatio{Numerator: previous, Denominator: current, Unit: "%"}
	if previous == 0 {
		ratio.Note = "上一窗口没有重复问题，无法计算下降"
		return ratio
	}
	value := float64(previous-current) / float64(previous)
	ratio.Value = &value
	return ratio
}

// QualityLatencySample is one (from, to) pair used for duration metrics.
//
// 返回原始成对数据而不是让数据库算平均：SQLite 没有 EXTRACT(EPOCH ...)，
// 在 Go 侧求均值是唯一跨方言一致的做法，也让"发布早于信号"这种不成立的
// 数据能在求均值前被剔除。
type QualityLatencySample struct {
	From time.Time
	To   time.Time
}

// MeanHours averages the durations of the samples.
func MeanHours(samples []QualityLatencySample) QualityRatio {
	var totalHours float64
	valid := 0
	for _, sample := range samples {
		if sample.To.Before(sample.From) {
			continue
		}
		totalHours += sample.To.Sub(sample.From).Hours()
		valid++
	}
	ratio := QualityRatio{Unit: "hours"}
	if valid == 0 {
		ratio.Note = "窗口内没有样本，不计算时长"
		return ratio
	}
	value := totalHours / float64(valid)
	ratio.Value = &value
	ratio.Numerator = int64(valid)
	ratio.Denominator = int64(len(samples))
	return ratio
}
