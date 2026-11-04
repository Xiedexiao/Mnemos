package types

import (
	"sort"
	"strings"
	"unicode"
)

// ============================================================================
// 错别字纠正与查询改写（KnowAxis V3.0 §12.1 "容错"）。
//
// 纠错的难点不是算编辑距离，而是**什么时候不该纠**：
//   - "E45" 与 "E46" 距离为 1，但它们是两台设备，改了就是答错问题；
//   - "报井" 距离 1，全库只有"报警"时显然该纠；
//   - 短词（1–2 个字符）的编辑距离几乎没有信息量，距离 1 就等于换了整个词。
//
// 因此这里的纠错是"候选驱动"的：只看**语料里真实存在的词**，并且要求
// 1) 等长或长度差 ≤ 1；2) 编辑距离 ≤ 阈值；3) 候选词在语料里出现过；
// 4) 含数字/编号的词不纠（编号是精确标识符，§12.1 "编号精确"）。
//
// 同音/近音是另一条通道：中文错别字十有八九是同音字（"报井"/"报警"），
// 编辑距离 1 恰好也能覆盖，但"精"与"警"这类形近音近的组合用拼音相等判断
// 更稳。两条通道合并去重后统一排序。
// ============================================================================

// FuzzyCorrection bounds how far a suggestion may drift.
type FuzzyCorrection struct {
	// Original is the token as typed.
	Original string `json:"original"`
	// Suggestion is the corpus term it most likely meant.
	Suggestion string `json:"suggestion"`
	// Distance is the Levenshtein distance (0 for a pure homophone swap).
	Distance int `json:"distance"`
	// Reason explains which channel produced it (typo / homophone).
	Reason string `json:"reason"`
}

// Fuzzy correction reasons.
const (
	// FuzzyReasonTypo 字形相近：编辑距离在阈值内。
	FuzzyReasonTypo = "typo"
	// FuzzyReasonHomophone 同音/近音：拼音相同或只差一个音节。
	FuzzyReasonHomophone = "homophone"
)

const (
	// DefaultMaxEditDistance is the typo threshold for tokens of 3+ runes.
	//
	// 2 是"报井处理"→"报警处理"这类两处笔误的上限；对 3 字词就是 2/3 的
	// 字符被替换，再放宽就失去意义了。
	DefaultMaxEditDistance = 2
	// shortTokenMaxDistance is the threshold for 1–2 rune tokens: 1.
	shortTokenMaxDistance = 1
	// shortTokenRunes is the boundary between the two thresholds.
	shortTokenRunes = 3
)

// EditDistance computes the Levenshtein distance between two rune slices.
//
// 用滚动一维数组：查询纠错要对每个候选词算一次，二维表在这个调用频次下
// 分配压力明显。rune 语义（不是 byte）是必须的——中文一个字符三字节，
// 按 byte 算会把"报警"与"报井"算成距离 1（正确）但把"报警"与"报京"
// 算成距离 1 的同时也把无关的半个字符算成替换。
func EditDistance(a, b string) int {
	ar, br := []rune(a), []rune(b)
	if len(ar) == 0 {
		return len(br)
	}
	if len(br) == 0 {
		return len(ar)
	}
	// 让 b 是较短的一侧，滚动数组长度为 len(b)+1。
	if len(ar) < len(br) {
		ar, br = br, ar
	}
	previous := make([]int, len(br)+1)
	current := make([]int, len(br)+1)
	for j := 0; j <= len(br); j++ {
		previous[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		current[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			deletion := previous[j] + 1
			insertion := current[j-1] + 1
			substitution := previous[j-1] + cost
			best := deletion
			if insertion < best {
				best = insertion
			}
			if substitution < best {
				best = substitution
			}
			current[j] = best
		}
		previous, current = current, previous
	}
	return previous[len(br)]
}

// maxEditDistanceFor returns the threshold that applies to a token.
func maxEditDistanceFor(token string) int {
	if len([]rune(token)) < shortTokenRunes {
		return shortTokenMaxDistance
	}
	return DefaultMaxEditDistance
}

// containsDigit reports whether the token looks like an identifier.
//
// 编号/错误码/物料号必须精确匹配（§12.1 "编号精确通道"）：把 "E45" 纠成
// "E46" 不是容错，是给出错误的设备。含数字的片段一律不参与纠错。
func containsDigit(token string) bool {
	for _, ch := range token {
		if unicode.IsDigit(ch) {
			return true
		}
	}
	return false
}

// FuzzyTokenize splits a query into the units correction operates on.
//
// 中文按**双字**切（"报警处理" → 报警/警处/处理）：中文没有空格，双字
// 片段是"可能的词"的最小近似；单字太短（编辑距离 1 就等于换字）。
// 拉丁/数字按非字母数字切分，长度 ≥ 2 才保留。
func FuzzyTokenize(query string) []string {
	tokens := make([]string, 0, 8)
	var latin []rune
	var han []rune
	flushLatin := func() {
		if len(latin) >= 2 {
			tokens = append(tokens, string(latin))
		}
		latin = latin[:0]
	}
	flushHan := func() {
		switch len(han) {
		case 0:
		case 1:
			// 单个汉字：只在整条查询就这一个词时才有意义，交给上层判断。
			tokens = append(tokens, string(han))
		default:
			for i := 0; i+1 < len(han); i++ {
				tokens = append(tokens, string(han[i:i+2]))
			}
		}
		han = han[:0]
	}
	for _, ch := range query {
		if unicode.Is(unicode.Han, ch) {
			flushLatin()
			han = append(han, ch)
			continue
		}
		if unicode.IsLetter(ch) || unicode.IsDigit(ch) {
			flushHan()
			latin = append(latin, unicode.ToLower(ch))
			continue
		}
		flushLatin()
		flushHan()
	}
	flushLatin()
	flushHan()
	return dedupeStrings(tokens)
}

// SuggestCorrections proposes corpus tokens close to the query's tokens.
//
// vocabulary 是"语料里出现过的词"（标题、术语标准名），由调用方提供。
// **词表与查询按同一粒度切分**：查询切成双字片段（报警处理 → 报警/警处/处理），
// 词表也切成双字片段。不这么做就无法把"报井"纠成"报警"——整词"报警处理"
// 与片段"报井"的长度差远超阈值，永远匹配不上。
//
// 这个函数不做任何 IO：给定同一份词表，输出完全确定，因此能被单元测试与
// 离线评估集直接复用。
//
// 排序规则：距离小的在前；同距离同音优先（中文错别字十有八九是同音字）；
// 再按词长降序、字典序。
func SuggestCorrections(
	query string, vocabulary []string, limit int,
) []FuzzyCorrection {
	if limit <= 0 {
		limit = 5
	}
	tokens := FuzzyTokenize(query)
	if len(tokens) == 0 || len(vocabulary) == 0 {
		return nil
	}

	// 词表切成同样的片段，并按长度分桶：只比较长度差 ≤ 阈值的候选。
	type entry struct {
		token string
		runes int
	}
	entries := make([]entry, 0, len(vocabulary)*2)
	byLength := make(map[int][]int, 16)
	seenToken := make(map[string]struct{}, len(vocabulary)*2)
	for _, term := range vocabulary {
		for _, fragment := range FuzzyTokenize(term) {
			if _, duplicate := seenToken[fragment]; duplicate {
				continue
			}
			seenToken[fragment] = struct{}{}
			index := len(entries)
			entries = append(entries, entry{token: fragment, runes: len([]rune(fragment))})
			byLength[entries[index].runes] = append(byLength[entries[index].runes], index)
		}
	}
	if len(entries) == 0 {
		return nil
	}

	// 拼音按需计算并缓存：同一个候选片段可能被多个查询片段比较到。
	pinyinCache := make(map[string]string, len(entries))

	best := make(map[string]FuzzyCorrection, len(tokens)*2)
	consider := func(correction FuzzyCorrection) {
		key := correction.Original + "\x00" + correction.Suggestion
		existing, ok := best[key]
		if !ok || betterCorrection(correction, existing) {
			best[key] = correction
		}
	}

	for _, token := range tokens {
		if containsDigit(token) {
			continue // 编号：精确通道，不纠错
		}
		tokenRunes := len([]rune(token))
		threshold := maxEditDistanceFor(token)
		tokenPinyin := PinyinOfText(token)
		for delta := -threshold; delta <= threshold; delta++ {
			for _, index := range byLength[tokenRunes+delta] {
				candidate := entries[index]
				if candidate.token == token {
					continue
				}
				// 同音通道：拼音完全相同且长度差 ≤ 1，即使写法不同也算强建议。
				candidatePinyin, cached := pinyinCache[candidate.token]
				if !cached {
					candidatePinyin = PinyinOfText(candidate.token)
					pinyinCache[candidate.token] = candidatePinyin
				}
				if tokenPinyin != "" && candidatePinyin == tokenPinyin &&
					absInt(candidate.runes-tokenRunes) <= 1 {
					consider(FuzzyCorrection{
						Original: token, Suggestion: candidate.token,
						Distance: EditDistance(token, candidate.token),
						Reason:   FuzzyReasonHomophone,
					})
					continue
				}
				distance := EditDistance(token, candidate.token)
				if distance == 0 || distance > threshold {
					continue
				}
				consider(FuzzyCorrection{
					Original: token, Suggestion: candidate.token,
					Distance: distance, Reason: FuzzyReasonTypo,
				})
			}
		}
	}

	out := make([]FuzzyCorrection, 0, len(best))
	for _, correction := range best {
		out = append(out, correction)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Distance != out[j].Distance {
			return out[i].Distance < out[j].Distance
		}
		iHomo := out[i].Reason == FuzzyReasonHomophone
		jHomo := out[j].Reason == FuzzyReasonHomophone
		if iHomo != jHomo {
			return iHomo
		}
		if len(out[i].Suggestion) != len(out[j].Suggestion) {
			return len(out[i].Suggestion) > len(out[j].Suggestion)
		}
		return out[i].Suggestion < out[j].Suggestion
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// absInt returns the absolute value of an int.
func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

// betterCorrection reports whether a beats b for the same (original, suggestion).
//
// 同一条纠正可能同时被两条通道发现；保留"同音 + 更小距离"的那条，理由字段
// 才能解释"为什么建议成它"。
func betterCorrection(a, b FuzzyCorrection) bool {
	if a.Distance != b.Distance {
		return a.Distance < b.Distance
	}
	aHomo := a.Reason == FuzzyReasonHomophone
	bHomo := b.Reason == FuzzyReasonHomophone
	return aHomo && !bHomo
}

// dedupeStrings preserves first-seen order.
func dedupeStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

// QueryRewrite is the §12.1 容错 result for one query.
//
// Original is always preserved and always searched first: 纠错是"多给几条召回
// 路径"，不是"替你换个词"。前端应把 Alternatives 展示为"你是不是想搜…"，
// 而不是静默替换。
type QueryRewrite struct {
	// Original is the query as typed.
	Original string `json:"original"`
	// Pinyin is the tone-less pinyin of the original ("" when it has no Han).
	Pinyin string `json:"pinyin,omitempty"`
	// ChineseFromPinyin are the Han candidates a Latin query could spell
	// (§12.1 拼音搜索). Only populated when the query is pure Latin.
	ChineseFromPinyin []string `json:"chinese_from_pinyin,omitempty"`
	// Corrections are corpus terms the query's tokens likely meant.
	Corrections []FuzzyCorrection `json:"corrections,omitempty"`
	// Alternatives is the ordered list of extra query strings to try after
	// the original, deduped and excluding the original itself.
	Alternatives []string `json:"alternatives,omitempty"`
}

// BuildQueryRewrite assembles the rewrite plan.
//
// vocabulary 必须只含调用者**有权看到**的词（标题片段/术语名）：纠错建议会
// 直接把那个词回显给用户，把不可见文档的标题片段当成候选，等于用纠错接口
// 做了一次存在性探测（§12.6）。
func BuildQueryRewrite(query string, vocabulary []string, limit int) *QueryRewrite {
	trimmed := strings.TrimSpace(query)
	rewrite := &QueryRewrite{Original: trimmed}
	if trimmed == "" {
		return rewrite
	}
	rewrite.Pinyin = PinyinOfText(trimmed)

	// 拼音搜索：纯拉丁输入时，在**语料词表**里找它可能拼的是哪些词。
	// 不在音节表上逐段猜汉字：那会给出"八哦井"这类无意义组合。
	if isLatinOnly(trimmed) {
		rewrite.ChineseFromPinyin = VocabularyPinyinMatches(trimmed, vocabulary, 4)
	}

	rewrite.Corrections = SuggestCorrections(trimmed, vocabulary, limit)

	// 组合备选查询：先把拼音→中文候选放前面（这是"完全不同的写法"），
	// 再放纠错后的整句。
	alternatives := make([]string, 0, len(rewrite.ChineseFromPinyin)+len(rewrite.Corrections))
	alternatives = append(alternatives, rewrite.ChineseFromPinyin...)
	for _, correction := range rewrite.Corrections {
		alternatives = append(alternatives, replaceToken(trimmed, correction))
	}
	rewrite.Alternatives = dedupeStringsExcluding(alternatives, trimmed)
	return rewrite
}

// replaceToken swaps one token inside the original query, preserving the rest.
//
// 只替换命中的片段而不是整句，是因为纠错针对的是分词后的词；把整句换成
// 单个候选词会丢掉用户写下的其它限定条件（"E45 报井处理" → "报警" 就丢了
// 设备号）。
func replaceToken(query string, correction FuzzyCorrection) string {
	if correction.Original == "" {
		return ""
	}
	// 大小写不敏感替换：拼音/英文错字常伴随大小写差异。
	lowered := strings.ToLower(query)
	original := strings.ToLower(correction.Original)
	index := strings.Index(lowered, original)
	if index < 0 {
		return ""
	}
	return query[:index] + correction.Suggestion + query[index+len(correction.Original):]
}

// isLatinOnly reports whether the query contains only ASCII letters/digits/spaces.
func isLatinOnly(query string) bool {
	for _, ch := range query {
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z':
		case ch >= '0' && ch <= '9':
		case unicode.IsSpace(ch):
		default:
			return false
		}
	}
	return true
}

// dedupeStringsExcluding removes blanks, duplicates and the excluded value.
func dedupeStringsExcluding(values []string, exclude string) []string {
	seen := make(map[string]struct{}, len(values)+1)
	if exclude != "" {
		seen[strings.ToLower(exclude)] = struct{}{}
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
}
