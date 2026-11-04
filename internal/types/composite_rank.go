package types

import (
	"math"
	"sort"
	"strings"
	"time"
)

// ============================================================================
// 综合排序（KnowAxis V3.0 §12.2 "综合：相关度 + 热度 + 权威性 + 新鲜度"）。
//
// 列表此前只有创建/更新/文件名三种排序——都是**单一维度**。缺的是"综合"：
// 一条刚被别人引用过、属于 L3 权威、还在复审期内的文档，应该排在一条三年
// 没动过、没人看过的文档前面，而按任一单列都做不到。
//
// 四个决定：
//  1. 四个维度各自归一到 [0,1] 再按权重合成，而不是把原始计数相加。热度是
//     "多少次浏览"、新鲜度是"多少天"，量纲不同，直接相加等于让浏览数最多的
//     维度垄断排序。
//  2. 每个维度都会随结果回显（含它自己的原始值）。"为什么这条排第一"必须
//     能回答——一个不可解释的综合分在评审会上是无法辩护的，运营也无法据此
//     调权重。
//  3. 相关度在没有查询词时为**中性值**，不是 0：不带关键词浏览列表时，
//     "相关度"这一维无法判断，把它记 0 会系统性压低所有条目、让排序退化成
//     纯热度。中性值让其余三维按相对大小起决定作用。
//  4. 权重是显式常量而不是"魔法数字"。四项之和为 1，读取方能一眼看出相对
//     重要程度；调权只改这一处。
// ============================================================================

// CompositeRankWeights is the §12.2 composite scoring weight set.
//
// 相关度最高（0.40）：综合排序的第一职责仍然是把"和这次浏览最相关的东西"
// 放到前面。权威性（0.25）高于热度（0.20）：一条 L3 权威内容即使暂时没人
// 看，也比一条被反复点开但只是闲聊记录的文档更该被看到。新鲜度（0.15）
// 垫底但不为零：过期内容不该靠"曾经很权威"长期占据榜首。
type CompositeRankWeights struct {
	Relevance float64 `json:"relevance"`
	Hotness   float64 `json:"hotness"`
	Authority float64 `json:"authority"`
	Freshness float64 `json:"freshness"`
}

// DefaultCompositeRankWeights returns the v1 weights.
func DefaultCompositeRankWeights() CompositeRankWeights {
	return CompositeRankWeights{
		Relevance: 0.40, Hotness: 0.20, Authority: 0.25, Freshness: 0.15,
	}
}

// CompositeRankInput is one row entering the composite scorer.
type CompositeRankInput struct {
	KnowledgeID string
	// Title / FileName feed the relevance dimension.
	Title    string
	FileName string
	// CurrentVersion / LifecycleStatus / Classification / ExpiresAt /
	// LastReviewedAt / ReviewCycleDays / CreatedAt feed authority and
	// freshness (same inputs as FreshnessScore).
	CurrentVersion  int
	LifecycleStatus string
	Classification  string
	ExpiresAt       *time.Time
	LastReviewedAt  *time.Time
	ReviewCycleDays int
	CreatedAt       time.Time
	UpdatedAt       time.Time
	// Views / Citations / Favorites are the raw hotness counters.
	Views     int64
	Citations int64
	Favorites int64
	// Level is the §10.4 knowledge level (L0–L3) from custom metadata,
	// "" when the row never came from an experience card.
	Level string
}

// CompositeRankBreakdown is the per-dimension detail echoed to the caller.
//
// 每个维度都带 normalized（进入加权的那一项）与 raw（可复核的依据）。只回显
// 归一化值不够：运营看到 0.62 无法判断"是浏览 30 次还是 3000 次"。
type CompositeRankBreakdown struct {
	// Relevance: how well the title matches the browse keyword.
	Relevance CompositeRankDimension `json:"relevance"`
	// Hotness: reads + citations + favorites, normalized inside this page.
	Hotness CompositeRankDimension `json:"hotness"`
	// Authority: knowledge level, classification and lifecycle.
	Authority CompositeRankDimension `json:"authority"`
	// Freshness: reuse of the §14.1 freshness score.
	Freshness CompositeRankDimension `json:"freshness"`
	// Score is the weighted sum in [0,1], rounded to 4 decimals.
	Score float64 `json:"score"`
}

// CompositeRankDimension is one dimension's contribution.
type CompositeRankDimension struct {
	// Value is the normalized score in [0,1].
	Value float64 `json:"value"`
	// Weight is the weight applied to Value.
	Weight float64 `json:"weight"`
	// Contribution is Value * Weight.
	Contribution float64 `json:"contribution"`
	// Raw is the human-readable basis ("views=12; citations=3", "L3", "good").
	Raw string `json:"raw,omitempty"`
}

// CompositeRankedRow pairs a row with its breakdown.
type CompositeRankedRow struct {
	KnowledgeID string                 `json:"knowledge_id"`
	Breakdown   CompositeRankBreakdown `json:"breakdown"`
}

// CompositeRankResult is the response for a composite-ordered page.
type CompositeRankResult struct {
	// Rows in composite order, highest score first.
	Rows []CompositeRankedRow `json:"rows"`
	// Weights echoes the weight set used.
	Weights CompositeRankWeights `json:"weights"`
	// Keyword is the term relevance was measured against ("" = neutral).
	Keyword string `json:"keyword"`
	// GeneratedAt is the scoring time.
	GeneratedAt time.Time `json:"generated_at"`
	// ScoredCount is how many candidates entered the scorer. It is reported
	// because composite ordering happens inside a bounded candidate window:
	// a caller comparing pages should know that the window, not the whole
	// knowledge base, produced the order.
	ScoredCount int `json:"scored_count"`
}

const (
	// CompositeRankCandidateFactor is how many candidates per requested row.
	//
	// 综合分需要候选之间相互比较（热度组内归一），所以必须先取一批再排序。
	// 5 倍是一个折中：足够让"近期活跃但不在前 20 条"的条目进入候选，又不会
	// 让一次列表请求扫过整个空间。
	CompositeRankCandidateFactor = 5
	// MaxCompositeRankCandidates caps the candidate window.
	MaxCompositeRankCandidates = 500
)

// NeutralRelevance is the relevance value when there is no keyword to match.
//
// 0.5 而不是 0：不带关键词浏览时相关度不可判定，记 0 会让所有条目在这一维
// 上同时归零——数学上等价于"这一维不参与"，但回显出来是"相关度都是 0"，
// 读起来像"这些结果都不相关"。中性值直接表达"未测量"。
const NeutralRelevance = 0.5

// RelevanceForKeyword scores one row's title against the browse keyword.
//
// 这里刻意是**低成本**的：列表页可能一次算 20 行，不能为每行跑一次分词+
// 倒排。规则只有三条，按命中强度递减：
//   - 标题以下去空白后等于关键词 → 1.0；
//   - 标题包含关键词 → 0.75；
//   - 关键词的每个字符都出现在标题里（子序列）→ 0.5；
//   - 否则 0。
//
// 子序列那一档用来兜住"E45报警"vs"E45 报警处理"这种空格/插字差异。真正
// 的相关度排序仍由检索管线负责；这一维只是让综合排序在有查询词时不至于
// 把明显不相关的条目排上来。
func RelevanceForKeyword(title, fileName, keyword string) float64 {
	needle := normalizeRankText(keyword)
	if needle == "" {
		return NeutralRelevance
	}
	for _, haystack := range []string{title, fileName} {
		normalized := normalizeRankText(haystack)
		if normalized == "" {
			continue
		}
		if normalized == needle {
			return 1.0
		}
		if strings.Contains(normalized, needle) {
			return 0.75
		}
	}
	for _, haystack := range []string{title, fileName} {
		if isSubsequence(normalizeRankText(haystack), needle) {
			return 0.5
		}
	}
	return 0
}

// normalizeRankText lowercases and drops all whitespace/punctuation.
func normalizeRankText(text string) string {
	out := make([]rune, 0, len(text))
	for _, ch := range text {
		switch {
		case ch >= 'A' && ch <= 'Z':
			out = append(out, ch+'a'-'A')
		case ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9':
			out = append(out, ch)
		case ch > 0x2E80:
			// CJK 与全角字符：保留（标点也保留，中文标题里标点常是语义一部分）
			out = append(out, ch)
		}
	}
	return string(out)
}

// isSubsequence reports whether every rune of needle appears in haystack in order.
func isSubsequence(haystack, needle string) bool {
	if needle == "" {
		return false
	}
	needleRunes := []rune(needle)
	position := 0
	for _, ch := range haystack {
		if position < len(needleRunes) && ch == needleRunes[position] {
			position++
		}
	}
	return position == len(needleRunes)
}

// AuthorityForKnowledge scores how authoritative one row is.
//
// 三个信号，取**最大值**而不是加权和：它们表达的是同一件事的不同来源
// （"这条内容的可信等级"), 一条 L3 文档不该因为它是 internal 密级而被降权。
//   - 知识层级 L3 = 1.0、L2 = 0.7、L1 = 0.4、L0/未标注 = 0.3；
//   - 密级 public = 0.6（可对外发布，本身是一种权威声明）；
//   - 生命周期 valid = 0.5 保底，pending_review_cycle/stale 不给保底。
//
// 0.3 的基线是刻意的：没有层级标记的普通文档也不是零权威，否则一个新租户
// 的排序会完全由热度决定。
func AuthorityForKnowledge(level, classification, lifecycle string) (float64, string) {
	score := 0.3
	reason := "未标注层级"
	switch KnowledgeCandidateLevel(level) {
	case KnowledgeCandidateLevelL3:
		score, reason = 1.0, "L3 权威"
	case KnowledgeCandidateLevelL2:
		score, reason = 0.7, "L2 已确认"
	case KnowledgeCandidateLevelL1:
		score, reason = 0.4, "L1 候选"
	}
	if ParseKnowledgeClassification(classification) == KnowledgeClassificationPublic && score < 0.6 {
		score, reason = 0.6, "public 可对外"
	}
	status := ParseKnowledgeLifecycleStatus(lifecycle)
	if status == KnowledgeLifecycleValid && score < 0.5 {
		score, reason = 0.5, "有效且未降级"
	}
	return score, reason
}

// HotnessForKnowledges normalizes the raw counters inside one result set.
//
// 用**组内最大值**归一而不是固定阈值：不同租户的体量差几个数量级，把
// "浏览 100 次"写死成满分会让小租户的所有条目都接近 0、大租户的所有条目
// 都接近 1——排序失去区分度。组内归一保证这一次排序里"最热的那个"得 1 分。
//
// 三个计数的权重不同：浏览 1 分、收藏 2 分、被回答引用 3 分。引用最难伪造
// （要模型认为这条内容支撑了答案），浏览最容易（点开就算）。
func HotnessForKnowledges(inputs []CompositeRankInput) map[string]float64 {
	out := make(map[string]float64, len(inputs))
	if len(inputs) == 0 {
		return out
	}
	raw := make(map[string]float64, len(inputs))
	var maxRaw float64
	for _, input := range inputs {
		weighted := float64(input.Views) + 2*float64(input.Favorites) + 3*float64(input.Citations)
		raw[input.KnowledgeID] = weighted
		if weighted > maxRaw {
			maxRaw = weighted
		}
	}
	for id, value := range raw {
		if maxRaw <= 0 {
			// 整组都没有热度数据：全给 0，让其余维度决定顺序。给 1 会伪造
			// 出"所有条目一样热"的信号。
			out[id] = 0
			continue
		}
		out[id] = float64(int(value/maxRaw*1000+0.5)) / 1000
	}
	return out
}

// CompositeRankHotnessRaw renders the counters for the breakdown.
func CompositeRankHotnessRaw(input CompositeRankInput) string {
	if input.Views == 0 && input.Favorites == 0 && input.Citations == 0 {
		return "无浏览/收藏/引用"
	}
	return "views=" + itoaRank(input.Views) +
		"; favorites=" + itoaRank(input.Favorites) +
		"; citations=" + itoaRank(input.Citations)
}

// itoaRank renders a non-negative int64.
func itoaRank(value int64) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var digits [20]byte
	pos := len(digits)
	for value > 0 {
		pos--
		digits[pos] = byte('0' + value%10)
		value /= 10
	}
	if negative {
		return "-" + string(digits[pos:])
	}
	return string(digits[pos:])
}

// ScoreComposite ranks rows by the §12.2 composite formula.
//
// 纯函数：同样的输入与权重恒得同样的输出与同样的顺序（同分时按知识 ID
// 升序），因此可以被离线评估集复现。
func ScoreComposite(
	inputs []CompositeRankInput, keyword string, weights CompositeRankWeights, now time.Time,
) *CompositeRankResult {
	result := &CompositeRankResult{
		Rows:        make([]CompositeRankedRow, 0, len(inputs)),
		Weights:     weights,
		Keyword:     keyword,
		GeneratedAt: now.UTC(),
	}
	if len(inputs) == 0 {
		return result
	}
	hotness := HotnessForKnowledges(inputs)

	for _, input := range inputs {
		relevance := RelevanceForKeyword(input.Title, input.FileName, keyword)
		authority, authorityReason := AuthorityForKnowledge(
			input.Level, input.Classification, input.LifecycleStatus)

		// 新鲜度复用 §14.1 的评分（0–100 → 0–1）：两个功能用同一套口径，
		// 否则"新鲜度 30 分"和"综合排序里新鲜度 0.8"会互相矛盾。
		freshnessScore := FromKnowledge(&Knowledge{
			CreatedAt:       input.CreatedAt,
			UpdatedAt:       input.UpdatedAt,
			CurrentVersion:  input.CurrentVersion,
			LifecycleStatus: input.LifecycleStatus,
			ExpiresAt:       input.ExpiresAt,
			LastReviewedAt:  input.LastReviewedAt,
			ReviewCycleDays: input.ReviewCycleDays,
		}, now)
		freshness := float64(freshnessScore.Total) / 100.0
		if freshness < 0 {
			freshness = 0
		}
		if freshness > 1 {
			freshness = 1
		}

		relevanceDim := CompositeRankDimension{
			Value: roundRank(relevance), Weight: weights.Relevance,
			Raw: rankRelevanceRaw(keyword),
		}
		hotnessDim := CompositeRankDimension{
			Value: roundRank(hotness[input.KnowledgeID]), Weight: weights.Hotness,
			Raw: CompositeRankHotnessRaw(input),
		}
		authorityDim := CompositeRankDimension{
			Value: roundRank(authority), Weight: weights.Authority,
			Raw: authorityReason,
		}
		freshnessDim := CompositeRankDimension{
			Value: roundRank(freshness), Weight: weights.Freshness,
			Raw: string(freshnessScore.Grade),
		}
		for _, dim := range []*CompositeRankDimension{
			&relevanceDim, &hotnessDim, &authorityDim, &freshnessDim,
		} {
			dim.Contribution = math.Round(dim.Value*dim.Weight*1e4) / 1e4
		}
		score := relevanceDim.Contribution + hotnessDim.Contribution +
			authorityDim.Contribution + freshnessDim.Contribution

		result.Rows = append(result.Rows, CompositeRankedRow{
			KnowledgeID: input.KnowledgeID,
			Breakdown: CompositeRankBreakdown{
				Relevance: relevanceDim,
				Hotness:   hotnessDim,
				Authority: authorityDim,
				Freshness: freshnessDim,
				Score:     math.Round(score*1e4) / 1e4,
			},
		})
	}

	sort.SliceStable(result.Rows, func(i, j int) bool {
		if result.Rows[i].Breakdown.Score != result.Rows[j].Breakdown.Score {
			return result.Rows[i].Breakdown.Score > result.Rows[j].Breakdown.Score
		}
		return result.Rows[i].KnowledgeID < result.Rows[j].KnowledgeID
	})
	return result
}

// rankRelevanceRaw explains the relevance dimension.
func rankRelevanceRaw(keyword string) string {
	if normalizeRankText(keyword) == "" {
		return "未提供关键词（中性值）"
	}
	return keyword
}

// roundRank rounds to 4 decimals so the echoed value matches the sum.
func roundRank(value float64) float64 {
	return math.Round(value*1e4) / 1e4
}

// ValidCompositeRankWeights reports whether a weight set is usable.
func ValidCompositeRankWeights(weights CompositeRankWeights) bool {
	total := weights.Relevance + weights.Hotness + weights.Authority + weights.Freshness
	if math.Abs(total-1.0) > 1e-6 {
		return false
	}
	for _, weight := range []float64{
		weights.Relevance, weights.Hotness, weights.Authority, weights.Freshness,
	} {
		if weight < 0 || weight > 1 {
			return false
		}
	}
	return true
}
