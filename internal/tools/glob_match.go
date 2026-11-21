package tools

// 本文件提供自研 shell 的通配符匹配原语。与 glob.go 中基于 path.Match 的
// globMatch 不同，这里的语义完全由本包掌控：按 rune 比较、反斜杠转义、
// 支持 [abc]/[a-z]/[!abc] 字符类，且通配符永不跨越路径分隔符 '/'。
// 三个导出函数都是纯函数：不访问文件系统，也不修改调用方传入的参数。

// globTokenKind 是模式解析后的 token 类别。
type globTokenKind uint8

const (
	globLit   globTokenKind = iota // 字面量 rune（含被转义的通配符）
	globStar                       // '*'：任意序列（不含 '/'）
	globAny                        // '?'：单个 rune（不含 '/'）
	globClass                      // '[...]'：字符类
)

// globRange 表示字符类中的一段闭区间 [lo, hi]。单字符用 lo == hi 表示。
type globRange struct {
	lo, hi rune
}

// globCharClass 描述一个字符类。negated 为真时表示 [!...] 取反。
type globCharClass struct {
	negated bool
	ranges  []globRange
}

// globToken 是模式中的一个解析单元。
type globToken struct {
	kind  globTokenKind
	lit   rune          // 仅 globLit 使用
	class globCharClass // 仅 globClass 使用
}

// parseGlob 把模式解析为 token 序列。
//
// 返回值中 hasWildcard 表示存在未被转义的通配符（*、?、[）；valid 表示
// 模式语法是否正确。下列情况视为无效模式：尾部孤立反斜杠、未闭合的字符类、
// 空字符类（[] 与 [!]）、以及字符类内逆序的范围（如 [z-a]）。
//
// 解析只读入参 pattern，不会修改它。
func parseGlob(pattern []rune) (tokens []globToken, hasWildcard bool, valid bool) {
	valid = true
	for i := 0; i < len(pattern); {
		switch pattern[i] {
		case '\\':
			// 转义：把下一个 rune 当作字面量。尾部反斜杠无意义。
			if i+1 >= len(pattern) {
				return nil, hasWildcard, false
			}
			tokens = append(tokens, globToken{kind: globLit, lit: pattern[i+1]})
			i += 2
		case '*':
			hasWildcard = true
			tokens = append(tokens, globToken{kind: globStar})
			i++
		case '?':
			hasWildcard = true
			tokens = append(tokens, globToken{kind: globAny})
			i++
		case '[':
			hasWildcard = true
			cls, next, ok := parseGlobClass(pattern, i+1)
			if !ok {
				return nil, hasWildcard, false
			}
			tokens = append(tokens, globToken{kind: globClass, class: cls})
			i = next
		default:
			tokens = append(tokens, globToken{kind: globLit, lit: pattern[i]})
			i++
		}
	}
	return tokens, hasWildcard, valid
}

// parseGlobClass 从 start 开始解析一个字符类，返回类定义与 ']' 之后的下标。
// start 指向 '[' 之后（已消费可选的 '!' 判断）。
func parseGlobClass(pattern []rune, start int) (cls globCharClass, next int, ok bool) {
	i := start
	// 首字符 '!' 表示取反，需要放在字符类最前面。
	if i < len(pattern) && pattern[i] == '!' {
		cls.negated = true
		i++
	}

	// first 为真时，']' 被当作字面量（与 filepath.Match 的 []]
	// 用法一致），从而让空类 []/[!] 因找不到闭合括号而判为无效。
	first := true
	for {
		if i >= len(pattern) {
			return cls, i, false // 未闭合
		}
		if pattern[i] == ']' && !first {
			return cls, i + 1, true
		}

		lo, afterLo, okLo := globClassRune(pattern, i)
		if !okLo {
			return cls, i, false // 尾部反斜杠
		}
		i = afterLo

		hi := lo
		// '-' 只在既非首位、其后又不是 ']' 时才构成范围。
		if i+1 < len(pattern) && pattern[i] == '-' && pattern[i+1] != ']' {
			h, afterHi, okHi := globClassRune(pattern, i+1)
			if !okHi {
				return cls, i, false
			}
			hi = h
			i = afterHi
		}
		if lo > hi {
			return cls, i, false // 逆序范围
		}
		cls.ranges = append(cls.ranges, globRange{lo: lo, hi: hi})
		first = false
	}
}

// globClassRune 读取字符类中的一个 rune，支持 '\' 转义。
func globClassRune(pattern []rune, i int) (r rune, next int, ok bool) {
	if i >= len(pattern) {
		return 0, i, false
	}
	if pattern[i] == '\\' {
		if i+1 >= len(pattern) {
			return 0, i, false
		}
		return pattern[i+1], i + 2, true
	}
	return pattern[i], i + 1, true
}

// tokenMatchesSingle 判断单个 token 能否匹配 name 中的一个 rune。
// '*' 不在此处理；通配符与字符类都不会匹配路径分隔符 '/'。
func tokenMatchesSingle(tok globToken, r rune) bool {
	switch tok.kind {
	case globLit:
		return tok.lit == r
	case globAny:
		return r != '/'
	case globClass:
		if r == '/' {
			return false
		}
		in := false
		for _, rg := range tok.class.ranges {
			if r >= rg.lo && r <= rg.hi {
				in = true
				break
			}
		}
		if tok.class.negated {
			return !in
		}
		return in
	default:
		return false
	}
}

// matchGlobTokens 用带回溯的双指针算法匹配 token 序列与 name。
// 时间开销在最坏情况下随 name 增长，但不会像纯递归那样指数爆炸。
func matchGlobTokens(tokens []globToken, name []rune) bool {
	ti, ni := 0, 0
	starIdx := -1 // 最近一个 '*' 在 tokens 中的下标
	starAt := 0   // 该 '*' 已消耗的 name rune 数

	for ni < len(name) {
		switch {
		case ti < len(tokens) && tokenMatchesSingle(tokens[ti], name[ni]):
			ti++
			ni++
		case ti < len(tokens) && tokens[ti].kind == globStar:
			// 先让 '*' 匹配零个字符，失败时再回溯。
			starIdx = ti
			starAt = ni
			ti++
		case starIdx >= 0:
			// 回溯：让 '*' 多吃一个字符，但不能越过路径分隔符。
			if name[starAt] == '/' {
				return false
			}
			starAt++
			ni = starAt
			ti = starIdx + 1
		default:
			return false
		}
	}
	// name 耗尽后，剩余 token 只能全是 '*'。
	for ti < len(tokens) && tokens[ti].kind == globStar {
		ti++
	}
	return ti == len(tokens)
}

// MatchGlob 按自研 shell 的通配符语法判断 name 是否匹配 pattern。
//
// 支持：'*' 匹配任意 rune 序列、'?' 匹配单个 rune、[abc]/[a-z]/[!abc]
// 字符类、以及 '\' 转义下一个字符（如 '\*' 匹配字面量 '*'）。CJK 字符按
// rune 比较，一个汉字算一个字符。通配符与字符类都不匹配路径分隔符 '/'，
// 但模式中字面的 '/' 可以匹配 name 中的 '/'。无效模式一律返回 false。
//
// 纯函数：不修改 pattern 或 name。
func MatchGlob(pattern, name string) bool {
	tokens, _, valid := parseGlob([]rune(pattern))
	if !valid {
		return false
	}
	return matchGlobTokens(tokens, []rune(name))
}

// MatchAny 依次用 MatchGlob 尝试 patterns，任一命中即返回 true。
// 空列表返回 false。纯函数：不修改 patterns。
func MatchAny(patterns []string, name string) bool {
	for _, pattern := range patterns {
		if MatchGlob(pattern, name) {
			return true
		}
	}
	return false
}

// GlobPatternInfo 返回模式的元信息：hasWildcard 表示是否存在未转义的通配符，
// valid 表示模式语法是否正确。纯函数：不修改 pattern。
func GlobPatternInfo(pattern string) (hasWildcard bool, valid bool) {
	_, hasWildcard, valid = parseGlob([]rune(pattern))
	return hasWildcard, valid
}
