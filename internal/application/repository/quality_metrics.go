package repository

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// qualityMetricsRepository answers the aggregate queries behind §14.7.
//
// 这些查询刻意直接走 *gorm.DB 而不是各垂直领域的仓储：指标要跨表聚合
// （变更信号 × 变更单 × 抽检 × 反馈 × 搜索日志），任何单个垂直的仓储都不
// 拥有这个视角。先例：OperationsDashboardService。
type qualityMetricsRepository struct {
	db *gorm.DB
}

// NewQualityMetricsRepository constructs the repository.
func NewQualityMetricsRepository(db *gorm.DB) interfaces.QualityMetricsRepository {
	return &qualityMetricsRepository{db: db}
}

// CountPublishedKnowledge counts usable published entries.
func (r *qualityMetricsRepository) CountPublishedKnowledge(
	ctx context.Context, tenantID uint64,
) (int64, error) {
	var total int64
	err := r.db.WithContext(ctx).Table("knowledges").
		Where("tenant_id = ? AND deleted_at IS NULL", tenantID).
		Where("COALESCE(NULLIF(lifecycle_status, ''), 'valid') = ?",
			string(types.KnowledgeLifecycleValid)).
		Count(&total).Error
	return total, err
}

// CountUsedKnowledge counts published entries touched in the window.
//
// "用过"的判定：窗口内被阅读（knowledge_view_events）或被引用进回答
// （evidence_packs.citations 非空）。两者都算复用——被引用比被阅读更能
// 说明内容支撑了答案。
func (r *qualityMetricsRepository) CountUsedKnowledge(
	ctx context.Context, tenantID uint64, since time.Time,
) (int64, error) {
	var total int64
	err := r.db.WithContext(ctx).Raw(`
		SELECT COUNT(DISTINCT k.id) FROM knowledges k
		WHERE k.tenant_id = ? AND k.deleted_at IS NULL
		  AND COALESCE(NULLIF(k.lifecycle_status, ''), 'valid') = ?
		  AND (
		    EXISTS (SELECT 1 FROM knowledge_view_events v
		            WHERE v.knowledge_id = k.id AND v.tenant_id = ?
		              AND v.created_at >= ?)
		    OR EXISTS (SELECT 1 FROM evidence_packs e
		               WHERE e.tenant_id = ?
		                 AND CAST(e.citations AS TEXT) LIKE '%' || k.id || '%'
		                 AND e.created_at >= ?)
		  )`,
		tenantID, string(types.KnowledgeLifecycleValid),
		tenantID, since.UTC(),
		tenantID, since.UTC(),
	).Scan(&total).Error
	return total, err
}

// SignalToUpdatePairs returns the raw (signal created, change published) pairs
// so the service can compute the mean portably when the dialect lacks EXTRACT.
func (r *qualityMetricsRepository) SignalToUpdatePairs(
	ctx context.Context, tenantID uint64, since time.Time, limit int,
) ([]types.QualityLatencySample, error) {
	if limit <= 0 || limit > 5000 {
		limit = 2000
	}
	var rows []struct {
		SignalAt  time.Time
		UpdatedAt time.Time
	}
	err := r.db.WithContext(ctx).Raw(`
		SELECT s.created_at AS signal_at, c.effective_at AS updated_at
		FROM change_signals s
		JOIN change_requests c ON c.id = s.change_request_id
		WHERE s.tenant_id = ?
		  AND s.status = ?
		  AND s.created_at >= ?
		  AND c.effective_at IS NOT NULL
		ORDER BY s.created_at DESC
		LIMIT ?`,
		tenantID, string(types.ChangeSignalConverted), since.UTC(), limit,
	).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]types.QualityLatencySample, 0, len(rows))
	for _, row := range rows {
		if row.UpdatedAt.Before(row.SignalAt) {
			// 发布早于信号：数据不成立（迁移或手工改单），不计入。
			continue
		}
		out = append(out, types.QualityLatencySample{
			From: row.SignalAt, To: row.UpdatedAt,
		})
	}
	return out, nil
}

// ReviewOnTime counts completed reviews and how many finished before due.
func (r *qualityMetricsRepository) ReviewOnTime(
	ctx context.Context, tenantID uint64, since time.Time,
) (onTime, completed int64, err error) {
	type row struct {
		OnTime int64
		Total  int64
	}
	var result row
	// sla_due 为空表示该复审没有设定时限，不计入按时率分母——"没有截止时间"
	// 无法判断是否按时。
	err = r.db.WithContext(ctx).Table("approval_tasks").
		Select(`SUM(CASE WHEN decision_at <= sla_due THEN 1 ELSE 0 END) AS on_time, COUNT(*) AS total`).
		Where("tenant_id = ? AND status = ? AND decision_at IS NOT NULL AND decision_at >= ? AND sla_due IS NOT NULL",
			tenantID, string(types.ApprovalTaskStatusApproved), since.UTC()).
		Scan(&result).Error
	return result.OnTime, result.Total, err
}

// CorrectionCloseDurations returns (created, closed) pairs for correction tasks.
func (r *qualityMetricsRepository) CorrectionCloseDurations(
	ctx context.Context, tenantID uint64, since time.Time, limit int,
) ([]types.QualityLatencySample, error) {
	if limit <= 0 || limit > 5000 {
		limit = 2000
	}
	var rows []struct {
		CreatedAt  time.Time
		ResolvedAt time.Time
	}
	err := r.db.WithContext(ctx).Table("quality_tasks").
		Select("created_at, resolved_at").
		Where("tenant_id = ? AND task_type = ? AND resolved_at IS NOT NULL AND resolved_at >= ?",
			tenantID, string(types.QualityTaskTypeCorrection), since.UTC()).
		Limit(limit).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]types.QualityLatencySample, 0, len(rows))
	for _, row := range rows {
		out = append(out, types.QualityLatencySample{From: row.CreatedAt, To: row.ResolvedAt})
	}
	return out, nil
}

// SamplingStats reads the inspection aggregate.
func (r *qualityMetricsRepository) SamplingStats(
	ctx context.Context, tenantID uint64, since time.Time,
) (passed, failed int64, err error) {
	type row struct {
		Passed int64
		Failed int64
	}
	var result row
	err = r.db.WithContext(ctx).Table("sampling_inspections").
		Select(`SUM(CASE WHEN status = 'passed' THEN 1 ELSE 0 END) AS passed,
		        SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END) AS failed`).
		Where("tenant_id = ? AND created_at >= ?", tenantID, since.UTC()).
		Scan(&result).Error
	return result.Passed, result.Failed, err
}

// CountErrorPublished counts published entries confirmed wrong after release.
//
// 口径：窗口内被判定抽检不通过或开了纠错任务的知识（去重）。它衡量"发布
// 环节漏掉了多少"，而不是"现在有多少错"。
func (r *qualityMetricsRepository) CountErrorPublished(
	ctx context.Context, tenantID uint64, since time.Time,
) (int64, error) {
	var total int64
	err := r.db.WithContext(ctx).Raw(`
		SELECT COUNT(DISTINCT knowledge_id) FROM (
			SELECT knowledge_id FROM sampling_inspections
			WHERE tenant_id = ? AND status = 'failed' AND created_at >= ?
			UNION
			SELECT knowledge_id FROM quality_tasks
			WHERE tenant_id = ? AND task_type IN (?, ?) AND created_at >= ?
		) AS flagged`,
		tenantID, since.UTC(),
		tenantID, string(types.QualityTaskTypeCorrection),
		string(types.QualityTaskTypeSamplingFailed), since.UTC(),
	).Scan(&total).Error
	return total, err
}

// AnswerCounts counts answered turns and how many carried citations.
//
// 拒答不计入分母：拒答本来就没有引用，把它算进"没有引用"会人为压低覆盖率。
func (r *qualityMetricsRepository) AnswerCounts(
	ctx context.Context, tenantID uint64, since time.Time,
) (withCitations, total int64, err error) {
	type row struct {
		WithCitations int64
		Total         int64
	}
	var result row
	// 不用 `<> '[]'::jsonb`：那是 Postgres 专有写法，SQLite 会直接语法报错。
	// 改为"去掉空白后不等于 '[]'"，两种方言都成立，也不依赖 JSON 函数。
	err = r.db.WithContext(ctx).Table("evidence_packs").
		Select(`SUM(CASE WHEN refusal = '' AND TRIM(CAST(citations AS TEXT)) NOT IN ('[]', 'null') THEN 1 ELSE 0 END) AS with_citations,
		        SUM(CASE WHEN refusal = '' THEN 1 ELSE 0 END) AS total`).
		Where("tenant_id = ? AND created_at >= ?", tenantID, since.UTC()).
		Scan(&result).Error
	return result.WithCitations, result.Total, err
}

// FeedbackResolution counts rated answers and the useful ones.
func (r *qualityMetricsRepository) FeedbackResolution(
	ctx context.Context, tenantID uint64, since time.Time,
) (useful, rated int64, err error) {
	type row struct {
		Useful int64
		Total  int64
	}
	var result row
	err = r.db.WithContext(ctx).Table("message_feedback").
		Select(`SUM(CASE WHEN rating = ? THEN 1 ELSE 0 END) AS useful, COUNT(*) AS total`,
			string(types.FeedbackRatingUseful)).
		Where("tenant_id = ? AND created_at >= ?", tenantID, since.UTC()).
		Scan(&result).Error
	return result.Useful, result.Total, err
}

// RepeatedZeroResultQueries counts zero-result queries that recurred.
//
// "重复"的定义是窗口中同一归一化问题出现 ≥2 次仍无结果：只出现一次的
// 零结果更可能是问法问题，重复出现的才说明知识真的缺。
func (r *qualityMetricsRepository) RepeatedZeroResultQueries(
	ctx context.Context, tenantID uint64, since, until time.Time,
) (int64, error) {
	type row struct {
		Total int64
	}
	var result row
	err := r.db.WithContext(ctx).Raw(`
		SELECT COALESCE(SUM(occurrences), 0) AS total FROM (
			SELECT COUNT(*) AS occurrences FROM search_logs
			WHERE tenant_id = ? AND result_count = 0 AND created_at >= ? AND created_at < ?
			GROUP BY query_normalized
			HAVING COUNT(*) >= 2
		) AS repeated`,
		tenantID, since.UTC(), until.UTC(),
	).Scan(&result).Error
	return result.Total, err
}

// GapCounts returns new and closed gaps in the window.
func (r *qualityMetricsRepository) GapCounts(
	ctx context.Context, tenantID uint64, since, until time.Time,
) (created, closed int64, err error) {
	type row struct {
		Created int64
		Closed  int64
	}
	var result row
	err = r.db.WithContext(ctx).Table("knowledge_gaps").
		Select(`SUM(CASE WHEN created_at >= ? AND created_at < ? THEN 1 ELSE 0 END) AS created,
		        SUM(CASE WHEN status = 'closed' AND updated_at >= ? AND updated_at < ? THEN 1 ELSE 0 END) AS closed`,
			since.UTC(), until.UTC(), since.UTC(), until.UTC()).
		Where("tenant_id = ?", tenantID).
		Scan(&result).Error
	return result.Created, result.Closed, err
}
