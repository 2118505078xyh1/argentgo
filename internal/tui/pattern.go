package tui

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// TermKind 标识模式中的某一项如何与行匹配。
// 这些类别仿照 fzf 的查询操作符，外加两个结构值：TermInverse
// （取反的模糊项，写作 "!text"）和 TermOr（组分隔符，写作
// "|"）。同一 OR 组内的正项用 AND 组合。
type TermKind uint8

const (
	// TermFuzzy 是无前缀的项；由调用方提供的模糊回调决定。
	TermFuzzy TermKind = iota
	// TermExact 是子串匹配，写作 "'text"。
	TermExact
	// TermPrefix 是锚定在开头的匹配，写作 "^text"。
	TermPrefix
	// TermSuffix 是锚定在末尾的匹配，写作 "text$"。
	TermSuffix
	// TermEqual 是两侧都有词边界的匹配，写作 "'text'"。
	TermEqual
	// TermInverse 是取反的模糊匹配，写作 "!text"。
	TermInverse
	// TermOr 分隔 AND 组；任一组匹配时整行匹配。
	TermOr
)

// String 使测试失败和调试输出可读。
func (k TermKind) String() string {
	switch k {
	case TermFuzzy:
		return "fuzzy"
	case TermExact:
		return "exact"
	case TermPrefix:
		return "prefix"
	case TermSuffix:
		return "suffix"
	case TermEqual:
		return "equal"
	case TermInverse:
		return "inverse"
	case TermOr:
		return "or"
	default:
		return "unknown"
	}
}

// SearchTerm 是模式查询的一个已解析 token。CaseSensitive 记录
// 智能大小写判定：Text 含大写 rune 时为 true，否则
// 为 false。Text 是不带操作符前缀或后缀的、未转义的模式正文。
// TermOr 携带空的 Text。
type SearchTerm struct {
	Kind          TermKind
	Text          string
	CaseSensitive bool
}

// ParsePattern 把查询转为扁平的搜索项列表。空白
// 分隔 AND 项，"|" 分隔 OR 组，前导反斜杠转义
// 下一个字节，使操作符和空格可被字面搜索。空（或
// 仅含空白）的查询返回空列表；调用方通过 MatchPattern
// 将其视为"匹配所有"。
func ParsePattern(query string) []SearchTerm {
	tokens := tokenizePattern(query)
	terms := make([]SearchTerm, 0, len(tokens))
	for _, tok := range tokens {
		if tok == "|" {
			terms = append(terms, SearchTerm{Kind: TermOr})
			continue
		}
		terms = append(terms, parsePatternToken(tok))
	}
	return terms
}

// tokenizePattern 在未转义的空白和 "|" 处切分，同时保留
// 反斜杠，使 parsePatternToken 能区分真正的操作符和
// 被转义的字面量。因此被转义的空格（"\ "）留在同一 token 内。
func tokenizePattern(query string) []string {
	var tokens []string
	var cur []byte
	flush := func() {
		if len(cur) > 0 {
			tokens = append(tokens, string(cur))
			cur = cur[:0]
		}
	}
	for i := 0; i < len(query); i++ {
		switch c := query[i]; {
		case c == '\\' && i+1 < len(query):
			cur = append(cur, c, query[i+1])
			i++
		case c == '|':
			flush()
			tokens = append(tokens, "|")
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			flush()
		default:
			cur = append(cur, c)
		}
	}
	flush()
	return tokens
}

// parsePatternToken 解释单个 token 的首尾操作符字节，
// 并对结果正文应用智能大小写。
func parsePatternToken(tok string) SearchTerm {
	term := SearchTerm{Kind: TermFuzzy}
	body := tok

	if len(body) > 0 && body[0] == '!' {
		term.Kind = TermInverse
		body = body[1:]
	} else if len(body) > 0 {
		switch body[0] {
		case '\'':
			term.Kind = TermExact
			body = body[1:]
			// 闭合引号把子串匹配变成有界匹配。
			if len(body) > 0 && body[len(body)-1] == '\'' && !escapedAt(body, len(body)-1) {
				term.Kind = TermEqual
				body = body[:len(body)-1]
			}
		case '^':
			term.Kind = TermPrefix
			body = body[1:]
		}
	}

	// "$" 仅在原本是普通项时作为锚点，因此 "^x$" 保留
	// "$" 字面量，而不是悄悄改变其含义。
	if term.Kind == TermFuzzy && len(body) > 0 && body[len(body)-1] == '$' && !escapedAt(body, len(body)-1) {
		term.Kind = TermSuffix
		body = body[:len(body)-1]
	}

	term.Text = unescapePattern(body)
	term.CaseSensitive = hasUpperRune(term.Text)
	return term
}

// escapedAt 报告 s[i] 之前是否有奇数个反斜杠。
func escapedAt(s string, i int) bool {
	n := 0
	for j := i - 1; j >= 0 && s[j] == '\\'; j-- {
		n++
	}
	return n%2 == 1
}

// unescapePattern 把每个 "\x" 对折叠为字面 "x"。
func unescapePattern(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func hasUpperRune(s string) bool {
	for _, r := range s {
		if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

// MatchPattern 报告 text 是否满足已解析的项。AND 绑定
// 比 OR 更紧：项在每个 TermOr 处被分组，一行
// 当某组的所有项都匹配时才匹配该组（TermInverse
// 项取反），各组结果再做 OR。空的项列表匹配
// 所有内容，这是空查询的自然结果。
//
// fuzzy 被注入，使本包永不依赖匹配器实现；
// 它接收（已做大小写归一化的）pattern 和 text，返回
// 通常的得分、位置和匹配标志。nil 的 fuzzy 回退到子串
// 检查，使纯模式的调用方保持简单。
func MatchPattern(terms []SearchTerm, text string, fuzzy func(p, t string) (int, []int, bool)) bool {
	if len(terms) == 0 {
		return true
	}

	sawGroup := false
	start := 0
	for i := 0; i <= len(terms); i++ {
		if i != len(terms) && terms[i].Kind != TermOr {
			continue
		}
		if i > start {
			sawGroup = true
			if groupMatches(terms[start:i], text, fuzzy) {
				return true
			}
		}
		start = i + 1
	}
	// 只有分隔符（例如查询为 "|"）—— 没有真正的约束可应用。
	if !sawGroup {
		return true
	}
	return false
}

func groupMatches(group []SearchTerm, text string, fuzzy func(p, t string) (int, []int, bool)) bool {
	for _, term := range group {
		matched := matchOne(term, text, fuzzy)
		if term.Kind == TermInverse {
			if matched {
				return false
			}
			continue
		}
		if !matched {
			return false
		}
	}
	return true
}

// matchOne 返回单个正项的原始（未取反）结果。
func matchOne(term SearchTerm, text string, fuzzy func(p, t string) (int, []int, bool)) bool {
	pattern, candidate := term.Text, text
	if !term.CaseSensitive {
		pattern = strings.ToLower(pattern)
		candidate = strings.ToLower(candidate)
	}

	switch term.Kind {
	case TermExact:
		return strings.Contains(candidate, pattern)
	case TermPrefix:
		return strings.HasPrefix(candidate, pattern)
	case TermSuffix:
		return strings.HasSuffix(candidate, pattern)
	case TermEqual:
		return boundedMatch(candidate, pattern)
	case TermFuzzy, TermInverse:
		if fuzzy != nil {
			_, _, ok := fuzzy(pattern, candidate)
			return ok
		}
		return strings.Contains(candidate, pattern)
	default: // TermOr and unknown kinds are neutral inside a group.
		return true
	}
}

// boundedMatch 实现 "'word'" 操作符：pattern 必须作为一个完整
// 的词出现，即两侧的 rune 都不是字母、数字或下划线。
func boundedMatch(text, pattern string) bool {
	if pattern == "" {
		return true
	}
	for i := 0; i+len(pattern) <= len(text); i++ {
		if text[i:i+len(pattern)] != pattern {
			continue
		}
		if i > 0 {
			before, _ := utf8.DecodeLastRuneInString(text[:i])
			if isWordRune(before) {
				continue
			}
		}
		after := i + len(pattern)
		if after < len(text) {
			r, _ := utf8.DecodeRuneInString(text[after:])
			if isWordRune(r) {
				continue
			}
		}
		return true
	}
	return false
}

func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}
