package types

import (
	"embed"
	"sort"
	"strings"
	"sync"
	"unicode"
)

// ============================================================================
// 查询容错（KnowAxis V3.0 §12.1 "容错：拼音搜索、错别字纠正、同义词扩展"）。
//
// 平台已有同义词扩展（术语表别名），但没有拼音与错别字：现场同事用手机打
// "baojing" 或把"报警"打成"报井"，检索就完全落空——而这恰恰是知识库最该
// 兜住的场景。
//
// 三条设计约束：
//  1. 纠错只在**候选侧**放宽，不擅自改用户的原词。原查询永远保留，纠错结果
//     作为额外召回路径追加。改词会让"我明明搜的是这个词"变得无从解释。
//  2. 拼音与纠错都是纯计算 + 少量候选词，不引第三方库（见 pinyin_table.txt
//     的生成说明）：一次查询里做 O(词数 × 音节数) 的字符串比较，成本可以
//     忽略，而多一个依赖就多一处升级与体积负担。
//  3. 纠错必须**有界**：只在与候选词等长或 ±1 个字符、且编辑距离 ≤ 2 时
//     才建议。"差不多就行"的纠错会把检索引到完全不相关的内容上，比不纠错
//     更糟。
// ============================================================================

//go:embed pinyin_table.txt
var pinyinTableFS embed.FS

// pinyinTableOnce guards the one-time parse of the embedded table.
var (
	pinyinTableOnce sync.Once
	// pinyinByChar maps one Han character to its tone-less syllables.
	pinyinByChar map[rune][]string
	// pinyinCharacters keeps a sorted copy for deterministic iteration.
	pinyinCharacters []rune
)

// loadPinyinTable parses the embedded "syllable:chars" table once.
//
// 多音字会在这里展开成"一个汉字 → 多个音节"：表里重/长/乐这些字出现在两个
// 音节行下，反向索引自然累积出多个读音。
func loadPinyinTable() {
	pinyinTableOnce.Do(func() {
		pinyinByChar = make(map[rune][]string, 7000)
		raw, err := pinyinTableFS.ReadFile("pinyin_table.txt")
		if err != nil {
			// 表缺失时拼音能力静默降级为"无拼音召回"，不影响其它检索路径。
			// 之所以不在 init 里 panic：拼音是增强项，不该让整个服务起不来。
			return
		}
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			colon := strings.IndexByte(line, ':')
			if colon <= 0 || colon == len(line)-1 {
				continue
			}
			syllable := strings.TrimSpace(line[:colon])
			if syllable == "" {
				continue
			}
			for _, ch := range line[colon+1:] {
				pinyinByChar[ch] = append(pinyinByChar[ch], syllable)
			}
		}
		pinyinCharacters = make([]rune, 0, len(pinyinByChar))
		for ch := range pinyinByChar {
			pinyinCharacters = append(pinyinCharacters, ch)
		}
		sort.Slice(pinyinCharacters, func(i, j int) bool {
			return pinyinCharacters[i] < pinyinCharacters[j]
		})
	})
}

// PinyinOf returns the tone-less syllables of one Han character.
//
// 非汉字（含全角标点、数字、字母）返回 nil：调用方据此判断"这段查询里有
// 没有可转写的内容"。
func PinyinOf(ch rune) []string {
	loadPinyinTable()
	return pinyinByChar[ch]
}

// PinyinSyllables splits text into its tone-less syllable sequence.
//
// 只转写汉字，非汉字原样按小写字符片段保留（例如 "E45 报警" →
// ["e45", "bao", "jing"]）。这样"设备 E45 报警"的拼音查询能同时命中编号
// 与中文词，而不是把编号丢掉。
func PinyinSyllables(text string) []string {
	loadPinyinTable()
	out := make([]string, 0, len(text)/2+2)
	var latin []rune
	flushLatin := func() {
		if len(latin) > 0 {
			out = append(out, string(latin))
			latin = latin[:0]
		}
	}
	for _, ch := range text {
		if syllables, ok := pinyinByChar[ch]; ok && len(syllables) > 0 {
			flushLatin()
			// 多音字：主读音放最前（表内顺序），其余作为备选由调用方按需取。
			out = append(out, syllables[0])
			continue
		}
		if unicode.IsLetter(ch) || unicode.IsDigit(ch) {
			latin = append(latin, unicode.ToLower(ch))
			continue
		}
		// 空格/标点：结束当前片段，但不产生占位。
		flushLatin()
	}
	flushLatin()
	return out
}

// PinyinOfText renders the full tone-less pinyin of text as one string.
//
// 返回的是音节拼接（"baojing"），不是带空格的序列：拼音输入的用户打的就是
// 连写形式，用连写比对才算"同一个输入"。
func PinyinOfText(text string) string {
	return strings.Join(PinyinSyllables(text), "")
}

// VocabularyPinyinMatches finds vocabulary terms a Latin query could be spelling.
//
// 这才是"拼音搜索"的正确做法：不是在音节表上逐段猜汉字（"baojing" 会被切成
// ba+o+jing 得到"八哦井"这种无意义组合），而是拿输入去**比对语料里真实存在
// 的词**。语料里的每个词都有确定的拼音，输入与它相等或是它的前缀，就说明
// 用户想搜的就是这个词。
//
// 匹配规则：
//   - 输入按空格/连字符切段后逐段拼接（"bao jing" 与 "baojing" 等价）；
//   - 与词的全拼相等 → 满分；是词全拼的前缀 → 也算（用户可能没打完）；
//   - 也接受"每段各自是若干连续音节的前缀"这种部分输入（"bj" 不算，太短
//     没有区分度；"baoj" 算）。
//
// 返回按匹配长度降序（越长的前缀越确定），同长度按词长降序、字典序。
func VocabularyPinyinMatches(latin string, vocabulary []string, limit int) []string {
	if limit <= 0 {
		limit = 4
	}
	normalized := normalizeLatinQuery(latin)
	if len(normalized) < 2 {
		return nil
	}
	type match struct {
		term      string
		matched   int
		exact     bool
		termRunes int
	}
	matches := make([]match, 0, 8)
	seen := make(map[string]struct{}, len(vocabulary))
	for _, term := range vocabulary {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		if _, duplicate := seen[term]; duplicate {
			continue
		}
		seen[term] = struct{}{}
		// 只对含汉字的词做拼音匹配：纯拉丁词自己就是写法，不需要转写。
		if !containsHan(term) {
			continue
		}
		pinyin := PinyinOfText(term)
		if pinyin == "" {
			continue
		}
		if pinyin == normalized {
			matches = append(matches, match{
				term: term, matched: len(normalized), exact: true,
				termRunes: len([]rune(term)),
			})
			continue
		}
		if strings.HasPrefix(pinyin, normalized) {
			matches = append(matches, match{
				term: term, matched: len(normalized),
				termRunes: len([]rune(term)),
			})
		}
	}
	if len(matches) == 0 {
		return nil
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].exact != matches[j].exact {
			return matches[i].exact
		}
		if matches[i].matched != matches[j].matched {
			return matches[i].matched > matches[j].matched
		}
		if matches[i].termRunes != matches[j].termRunes {
			return matches[i].termRunes < matches[j].termRunes
		}
		return matches[i].term < matches[j].term
	})
	out := make([]string, 0, limit)
	for _, m := range matches {
		out = append(out, m.term)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// normalizeLatinQuery lowercases and strips separators from a pinyin input.
//
// 用户可能打 "bao jing"、"bao-jing" 或 "BaoJing"：三种都是同一个输入。
func normalizeLatinQuery(latin string) string {
	var builder strings.Builder
	builder.Grow(len(latin))
	for _, ch := range strings.ToLower(latin) {
		switch {
		case ch >= 'a' && ch <= 'z':
			builder.WriteRune(ch)
		case ch >= '0' && ch <= '9':
			builder.WriteRune(ch)
		case unicode.IsSpace(ch), ch == '-', ch == '_', ch == '\'':
			// 分隔符：丢掉，等价于连写
		default:
			return ""
		}
	}
	return builder.String()
}

// containsHan reports whether text has at least one Han character.
func containsHan(text string) bool {
	for _, ch := range text {
		if unicode.Is(unicode.Han, ch) {
			return true
		}
	}
	return false
}
