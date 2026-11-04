package types

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// ============================================================================
// 推荐的可解释性（KnowAxis V3.0 §15.3 "猜你想学" 的说明能力）。
//
// 推荐位此前只有一个 Reason 枚举（related_relation / same_tag / hot / …）：
// 告诉读者"因为相关"，但**不说是哪条关系、哪个标签、多热**。运营也无法
// 回答"这条为什么排第一"——权重是配置在服务里的数字，回显出来的只有合成
// 后的 Score。
//
// 三个决定：
//  1. 每个命中信号各自成条（信号类型 + 原始证据 + 贡献分），而不是把合成
//     分拆成百分比。百分比会让人以为"热度占 43%"是精确的；而实际权重是
//     配置值，拆成百分比反而暗示了不存在的精度。
//  2. 理由按贡献降序排列。卡片的说明位只能放一两条，排在前面的必须是真正
//     决定名次的那一条。
//  3. 没有证据就不编理由。回归用户"同空间"兜底（same_space）本身是兜底
//     信号，它的贡献分如实偏低，卡片文案也应当说"来自同一空间"而不是
//     假装是兴趣匹配。
// ============================================================================

// RecommendationSignalType names one scoring input.
type RecommendationSignalType string

const (
	// RecommendationSignalRelation 知识关联边（依据/替代/补充/前置/冲突）。
	RecommendationSignalRelation RecommendationSignalType = "relation"
	// RecommendationSignalSharedTag 与当前知识共享的标签。
	RecommendationSignalSharedTag RecommendationSignalType = "shared_tag"
	// RecommendationSignalInterest 用户近期兴趣标签。
	RecommendationSignalInterest RecommendationSignalType = "interest_tag"
	// RecommendationSignalSameSpace 同一知识空间。
	RecommendationSignalSameSpace RecommendationSignalType = "same_space"
	// RecommendationSignalHot 近期阅读热度。
	RecommendationSignalHot RecommendationSignalType = "hot"
	// RecommendationSignalFreshness 内容新鲜度（衰减分）。
	RecommendationSignalFreshness RecommendationSignalType = "freshness"
	// RecommendationSignalLatest 最新动态榜的排序依据。
	RecommendationSignalLatest RecommendationSignalType = "latest"
)

// RecommendationSignal is one explainable contribution to the score.
type RecommendationSignal struct {
	// Type is the scoring input.
	Type RecommendationSignalType `json:"type"`
	// Contribution is the points this signal added.
	Contribution float64 `json:"contribution"`
	// Evidence is the human-readable basis: the relation type, the tag name,
	// "近 7 天 12 次阅读", "更新于 2026-11-01".
	Evidence string `json:"evidence,omitempty"`
	// Weight is the configured weight for this signal, echoed so an operator
	// can see why two similar items scored differently.
	Weight float64 `json:"weight,omitempty"`
	// Primary marks the signal that produced the list placement (the item's
	// Reason). It sorts ahead of larger-but-secondary contributions because
	// "why is this here" is answered by the primary reason, not by the
	// freshness bonus every item shares.
	Primary bool `json:"primary,omitempty"`
}

// RecommendationExplanation is the "why" panel for one item.
type RecommendationExplanation struct {
	// Summary is the one-line caption for the card.
	Summary string `json:"summary"`
	// Signals are the contributions, largest first.
	Signals []RecommendationSignal `json:"signals"`
	// Score is the total these signals (plus any base score) produced.
	Score float64 `json:"score"`
	// FreshnessHours is the item's age in hours at ranking time, so a reader
	// can tell "new" from "recently touched".
	FreshnessHours float64 `json:"freshness_hours,omitempty"`
}

// recommendationFreshnessMax is the score a just-updated document receives.
//
// 它必须与 service.baseScore 里的常量一致（那里写的是 5）；两处不一致会
// 让解释面板算出的贡献分与实际名次对不上，而这种偏差极难被发现。
const recommendationFreshnessMax = 5

// BuildRecommendationExplanation assembles the "why" for one item.
//
// item.Reason 决定**主信号**（与排序逻辑同源），其余信号按实际存在的证据
// 追加：一条既来自关联、又很热、又刚更新的推荐，读者应当看到三句话而不是
// 一句。
func BuildRecommendationExplanation(
	item RecommendationItem, weights RecommendationWeights, now time.Time,
	totalScore float64, views int64,
) RecommendationExplanation {
	explanation := RecommendationExplanation{Score: roundExplain(totalScore)}
	if !item.UpdatedAt.IsZero() {
		hours := now.Sub(item.UpdatedAt).Hours()
		if hours >= 0 {
			explanation.FreshnessHours = roundExplain(hours)
		}
	}

	signals := make([]RecommendationSignal, 0, 3)
	addSignal := func(
		kind RecommendationSignalType, contribution, weight float64,
		evidence string, primary bool,
	) {
		if contribution <= 0 {
			return
		}
		signals = append(signals, RecommendationSignal{
			Type: kind, Contribution: roundExplain(contribution),
			Weight: weight, Evidence: evidence, Primary: primary,
		})
	}

	switch item.Reason {
	case RecommendReasonRelated:
		addSignal(RecommendationSignalRelation, weights.RelationEdge,
			weights.RelationEdge, relationEvidence(item), true)
	case RecommendReasonSameTag:
		addSignal(RecommendationSignalSharedTag, weights.SharedTag,
			weights.SharedTag, tagEvidence(item.MatchedTag), true)
	case RecommendReasonInterest:
		addSignal(RecommendationSignalInterest, weights.InterestTag,
			weights.InterestTag, interestEvidence(item.MatchedTag), true)
	case RecommendReasonSameSpace:
		addSignal(RecommendationSignalSameSpace, weights.SameSpace,
			weights.SameSpace, sameSpaceEvidence(item.KnowledgeBaseName), true)
	case RecommendReasonHot:
		addSignal(RecommendationSignalHot, weights.HotPerView*float64(views),
			weights.HotPerView, hotEvidence(views), true)
	case RecommendReasonLatest:
		// 最新榜的排序依据就是更新时间本身，不额外加分。
		addSignal(RecommendationSignalLatest, 1,
			0, latestEvidence(item.UpdatedAt, now), true)
	}

	// 新鲜度是**独立于主信号**的附加项：任何榜单里的条目都会因更新而加分。
	if weights.FreshnessHalfLifeHours > 0 && !item.UpdatedAt.IsZero() {
		freshness := freshnessScore(item.UpdatedAt, now, weights)
		addSignal(RecommendationSignalFreshness, freshness,
			recommendationFreshnessMax, freshnessEvidence(item.UpdatedAt), false)
	}

	// 主信号排最前：它回答"这条为什么出现在这个榜单里"；新鲜度是所有条目
	// 共享的附加分，分数再高也不是名次的原因。其余信号按贡献降序。
	sort.SliceStable(signals, func(i, j int) bool {
		if signals[i].Primary != signals[j].Primary {
			return signals[i].Primary
		}
		if signals[i].Contribution != signals[j].Contribution {
			return signals[i].Contribution > signals[j].Contribution
		}
		return signals[i].Type < signals[j].Type
	})
	explanation.Signals = signals
	explanation.Summary = summarizeSignals(signals)
	return explanation
}

// freshnessScore is the same decay the ranking uses.
func freshnessScore(updatedAt, now time.Time, weights RecommendationWeights) float64 {
	hours := now.Sub(updatedAt).Hours()
	if hours <= 0 {
		return recommendationFreshnessMax
	}
	return recommendationFreshnessMax * math.Pow(0.5, hours/weights.FreshnessHalfLifeHours)
}

// summarizeSignals renders the card caption.
//
// 只用前两条：卡片说明位放不下更多，而按贡献降序的前两条就是决定名次的那
// 两条。没有信号时返回空串，调用方据此不渲染说明行（而不是显示一句空话）。
func summarizeSignals(signals []RecommendationSignal) string {
	if len(signals) == 0 {
		return ""
	}
	parts := make([]string, 0, 2)
	for i, signal := range signals {
		if i >= 2 {
			break
		}
		if signal.Evidence == "" {
			continue
		}
		parts = append(parts, signal.Evidence)
	}
	return strings.Join(parts, "；")
}

// relationEvidence describes a relation-based recommendation.
//
// 关系类型**不是**结果里的独立字段：RecommendationItem 只带 Reason，没有
// 具体的 relation_type。与其在这里编一个"有关联"的空话，不如让证据保持
// 诚实——如果调用方补上了关系类型，它会出现在这里。
func relationEvidence(item RecommendationItem) string {
	if item.MatchedTag != "" {
		return "关联关系：" + item.MatchedTag
	}
	return "与当前内容存在知识关联"
}

// tagEvidence names the shared tag.
func tagEvidence(tag string) string {
	if strings.TrimSpace(tag) == "" {
		return "与当前内容共享标签"
	}
	return "共享标签：" + tag
}

// interestEvidence names the interest tag.
func interestEvidence(tag string) string {
	if strings.TrimSpace(tag) == "" {
		return "与你最近阅读的主题相关"
	}
	return "你最近关注：" + tag
}

// sameSpaceEvidence names the space.
func sameSpaceEvidence(kbName string) string {
	if strings.TrimSpace(kbName) == "" {
		return "来自同一知识空间"
	}
	return "来自同一空间：" + kbName
}

// hotEvidence renders the view count.
func hotEvidence(views int64) string {
	if views <= 0 {
		return "近期阅读较多"
	}
	return fmt.Sprintf("近期 %d 次阅读", views)
}

// latestEvidence renders the update date.
func latestEvidence(updatedAt, now time.Time) string {
	if updatedAt.IsZero() {
		return "最近更新"
	}
	return "更新于 " + updatedAt.UTC().Format("2006-01-02")
}

// freshnessEvidence renders the freshness basis.
func freshnessEvidence(updatedAt time.Time) string {
	if updatedAt.IsZero() {
		return ""
	}
	return "更新于 " + updatedAt.UTC().Format("2006-01-02")
}

// roundExplain rounds to 3 decimals so the echoed numbers add up visibly.
func roundExplain(value float64) float64 {
	return math.Round(value*1000) / 1000
}

// ExplainableBoard attaches an explanation to every item of a board.
//
// 看板有四段（相关/猜你想学/热门/最新），每段的排序依据不同，但解释的构造
// 方式相同；这里只按段打标签，不改变任何排序结果——解释是**只读视图**，
// 它读的是已经算好的分数，绝不反过来影响名次。
func ExplainableBoard(
	board RecommendationBoard, weights RecommendationWeights, now time.Time,
	viewsOf func(knowledgeID string) int64,
) RecommendationBoard {
	explain := func(items []RecommendationItem) []RecommendationItem {
		if len(items) == 0 {
			return items
		}
		out := make([]RecommendationItem, len(items))
		for i, item := range items {
			copyOf := item
			views := int64(0)
			if viewsOf != nil {
				views = viewsOf(item.KnowledgeID)
			}
			explanation := BuildRecommendationExplanation(
				item, weights, now, item.Score, views)
			copyOf.Explanation = &explanation
			out[i] = copyOf
		}
		return out
	}
	board.Related = explain(board.Related)
	board.ForYou = explain(board.ForYou)
	board.Hot = explain(board.Hot)
	board.Latest = explain(board.Latest)
	return board
}
