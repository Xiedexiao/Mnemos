package types

import (
	"fmt"
	"strings"
	"time"
)

// ============================================================================
// 引用依据标注（KnowAxis V3.0 §12.2 排序的"权威"说明 + §13.4 证据追溯）。
//
// 证据包已经记录了一次回答的版本与密级快照，但**回答正文里的引用**只带
// 标题与片段：读者看到"[1] 报警处理规程"无法判断"这是哪一版、什么时候
// 生效、谁最后确认过"。这三件事恰恰决定了一条引用能不能被当作依据——
// 一条三年前的 v1 和昨天复核过的 v3 是完全不同的证据。
//
// 三个决定：
//  1. 标注是**纯函数**生成的字符串，不新增表、不新增接口：它由已经在结果
//     上的元数据（版本、生效日期、最后验证时间）拼出来。同一份元数据永远
//     生成同一段文字，可以直接用在提示词、导出与界面上。
//  2. 缺什么就省略什么，不写"未知"。一串"版本未知 / 生效日期未知"既没有
//     信息量，也会让真正的缺失（老数据没有版本号）显得像系统故障。
//  3. 时间统一按 UTC 日期输出（不带时分秒）。引用依据回答的是"哪一天的
//     版本"，精确到秒会给读者一种并不存在的确定性。
// ============================================================================

// CitationProvenance is the §12.2/§13.4 provenance carried on one hit.
//
// 字段刻意都是"可空"的：老数据与手工上传的文档可能没有版本或复核记录，
// 空值表示"这项信息不存在"，与"存在但为空"是两件事。
type CitationProvenance struct {
	// Version is the published version number (0 when unknown).
	Version int `json:"version,omitempty"`
	// EffectiveAt is when this version became effective (nil when unset).
	EffectiveAt *time.Time `json:"effective_at,omitempty"`
	// LastReviewedAt is when a human last confirmed it (nil when never).
	LastReviewedAt *time.Time `json:"last_reviewed_at,omitempty"`
	// LastReviewedBy is who confirmed it ("" when never).
	LastReviewedBy string `json:"last_reviewed_by,omitempty"`
	// Label is the rendered one-line citation suffix ("" when nothing is known).
	Label string `json:"label,omitempty"`
}

// BuildCitationProvenance assembles the provenance for one hit.
//
// reviewerName 由调用方解析（用户名需要额外查询，不该让这个纯函数依赖
// 用户服务）；传空串时标注里省略复核人，只保留日期。
func BuildCitationProvenance(
	version int, effectiveAt, lastReviewedAt *time.Time,
	lastReviewedBy, reviewerName string,
	now time.Time,
) CitationProvenance {
	provenance := CitationProvenance{
		Version:        version,
		EffectiveAt:    effectiveAt,
		LastReviewedAt: lastReviewedAt,
		LastReviewedBy: lastReviewedBy,
	}
	provenance.Label = RenderCitationProvenance(provenance, reviewerName, now)
	return provenance
}

// RenderCitationProvenance renders the human-readable suffix.
//
// 形如："v3 · 生效 2026-03-01 · 复核 2026-10-12 张三"。每一段只在数据存在
// 时出现；三段都没有时返回空串——调用方据此决定是否渲染分隔符，而不是留
// 一个悬空的"·"。
func RenderCitationProvenance(
	provenance CitationProvenance, reviewerName string, now time.Time,
) string {
	parts := make([]string, 0, 3)
	if provenance.Version > 0 {
		parts = append(parts, fmt.Sprintf("v%d", provenance.Version))
	}
	if provenance.EffectiveAt != nil && !provenance.EffectiveAt.IsZero() {
		// 未来生效的版本要标出来：读者会以为它已经生效了。
		if provenance.EffectiveAt.After(now) {
			parts = append(parts,
				"将于 "+provenance.EffectiveAt.UTC().Format("2006-01-02")+" 生效")
		} else {
			parts = append(parts,
				"生效 "+provenance.EffectiveAt.UTC().Format("2006-01-02"))
		}
	}
	if provenance.LastReviewedAt != nil && !provenance.LastReviewedAt.IsZero() {
		reviewed := "复核 " + provenance.LastReviewedAt.UTC().Format("2006-01-02")
		if name := strings.TrimSpace(reviewerName); name != "" {
			reviewed += " " + name
		}
		parts = append(parts, reviewed)
	}
	return strings.Join(parts, " · ")
}

// CitationProvenanceSuffix renders the inline citation suffix for a hit.
//
// 与 RenderCitationProvenance 的区别只在包装：有内容时用括号包起来直接接在
// 标题后面（"[1] 报警处理规程（v3 · 生效 2026-03-01）"），没有内容时返回空串，
// 调用方原样拼接即可，不需要自己判断。
func CitationProvenanceSuffix(
	provenance CitationProvenance, reviewerName string, now time.Time,
) string {
	label := RenderCitationProvenance(provenance, reviewerName, now)
	if label == "" {
		return ""
	}
	return "（" + label + "）"
}

// ProvenanceKnown reports whether anything is known at all.
//
// 用于决定是否在界面上显示"依据"一行：版本号 1 也算已知（v1 是真实信息，
// 说明它从发布后没有再更新过）。
func (p CitationProvenance) ProvenanceKnown() bool {
	return p.Version > 0 || p.EffectiveAt != nil || p.LastReviewedAt != nil
}
