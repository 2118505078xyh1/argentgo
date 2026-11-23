package tools

import (
	"fmt"
	"regexp"
	"strings"
)

// regexFallback 描述正则编译最终走到的降级层级。零值表示原始模式可直接编译。
type regexFallback int

const (
	regexExact             regexFallback = iota // 原始模式编译成功
	regexBracesFixed                            // 消毒 ${x} 后成功
	regexParensFixed                            // 转义孤立/不支持的括号后成功
	regexLookaroundLiteral                      // lookaround 被转成字面量后成功
	regexLiteralFallback                        // 最终走 regexp.QuoteMeta 字面量兜底
)

// String 返回给模型的可读降级说明；正常编译返回空串。
func (f regexFallback) String() string {
	switch f {
	case regexBracesFixed:
		return "提示：模式中的 ${...} 已按字面量处理。"
	case regexParensFixed:
		return "提示：模式中未配对的括号已按字面量转义。"
	case regexLookaroundLiteral:
		return "提示：Go 正则（RE2）不支持 lookaround（如 (?=...) / (?!...)），已改按字面量搜索。"
	case regexLiteralFallback:
		return "提示：模式无法作为正则编译，已退化为字面量搜索（regexp.QuoteMeta）。"
	default:
		return ""
	}
}

// dollarBrace 匹配 ${name} 形式的环境/模板变量，这类内容在 shell 里常见，
// 但作为正则时 {name} 不是合法的重复次数，会导致整轮搜索失败。
var dollarBrace = regexp.MustCompile(`\$\{[^}]*\}`)

// sanitizeDollarBraces 把 ${name} 变成匹配其字面文本的正则片段，
// 同时保留合法的 {n,m} 重复次数与 \p{Greek} 等 Unicode 类。
func sanitizeDollarBraces(pattern string) string {
	return dollarBrace.ReplaceAllStringFunc(pattern, func(m string) string {
		inner := m[2 : len(m)-1]
		return `\$\{` + inner + `\}`
	})
}

// neutralizeLookaround 把 RE2 不支持的 lookaround 前缀转义成字面量。
// 返回处理后的模式以及是否做过替换。
func neutralizeLookaround(pattern string) (string, bool) {
	var b strings.Builder
	found := false
	for i := 0; i < len(pattern); {
		if pattern[i] == '\\' && i+1 < len(pattern) {
			b.WriteByte(pattern[i])
			b.WriteByte(pattern[i+1])
			i += 2
			continue
		}
		if pattern[i] == '(' && i+1 < len(pattern) && pattern[i+1] == '?' {
			rest := pattern[i+2:]
			switch {
			case strings.HasPrefix(rest, "<="):
				b.WriteString(`\(\?<=`)
				i += 4
				found = true
				continue
			case strings.HasPrefix(rest, "<!"):
				b.WriteString(`\(\?<!`)
				i += 4
				found = true
				continue
			case strings.HasPrefix(rest, "="):
				b.WriteString(`\(\?=`)
				i += 3
				found = true
				continue
			case strings.HasPrefix(rest, "!"):
				b.WriteString(`\(\?!`)
				i += 3
				found = true
				continue
			}
		}
		b.WriteByte(pattern[i])
		i++
	}
	return b.String(), found
}

// escapeUnbalancedParens 转义未配对的括号。合法分组（含 (?:...) / (?i) /
// (?P<name>...) 等）保持不变；孤立 '(' 或多余的 ')' 变成字面量。
func escapeUnbalancedParens(pattern string) string {
	var opens []int
	var closes []int
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '\\':
			i++ // 跳过被转义的下一个字节
		case '(':
			opens = append(opens, i)
		case ')':
			if len(opens) > 0 {
				opens = opens[:len(opens)-1]
			} else {
				closes = append(closes, i)
			}
		}
	}
	if len(opens) == 0 && len(closes) == 0 {
		return pattern
	}
	mark := make([]bool, len(pattern))
	for _, i := range opens {
		mark[i] = true
	}
	for _, i := range closes {
		mark[i] = true
	}
	var b strings.Builder
	b.Grow(len(pattern) + len(opens) + len(closes))
	for i := 0; i < len(pattern); i++ {
		if mark[i] {
			b.WriteByte('\\')
		}
		b.WriteByte(pattern[i])
		if pattern[i] == '\\' && i+1 < len(pattern) {
			i++
			b.WriteByte(pattern[i])
		}
	}
	return b.String()
}

// compileSearchPattern 是搜索工具的正则编译器，按以下降级链工作：
//
//  1. 原始模式（可选加 (?i)）直接编译；
//  2. 消毒 ${...} 后再试；
//  3. lookaround 转字面量后试；
//  4. 孤立/不支持的括号转义后试；
//  5. regexp.QuoteMeta 整串字面量兜底。
//
// 返回值依次是：编译后的正则、实际使用的模式、降级层级、可读提示。
// 任何一步成功即返回，保证合法模式（a{2,4}、\p{Greek}）逐字节不变。
func compileSearchPattern(pattern string, insensitive bool) (*regexp.Regexp, string, regexFallback, string) {
	prefix := ""
	if insensitive {
		prefix = "(?i)"
	}
	try := func(src string) *regexp.Regexp {
		re, err := regexp.Compile(prefix + src)
		if err != nil {
			return nil
		}
		return re
	}

	// ${...} 必须优先消毒：$ 在正则里是行尾锚点，若先编译成功，
	// "${HOME}" 会变成"行尾后跟 {HOME}"而永不命中，反而不报错。
	fixed := sanitizeDollarBraces(pattern)
	if fixed != pattern {
		if re := try(fixed); re != nil {
			return re, fixed, regexBracesFixed, regexBracesFixed.String()
		}
	}
	if re := try(pattern); re != nil {
		return re, pattern, regexExact, ""
	}

	noLook, hadLook := neutralizeLookaround(fixed)
	if hadLook {
		if re := try(noLook); re != nil {
			return re, noLook, regexLookaroundLiteral, regexLookaroundLiteral.String()
		}
	}

	noParens := escapeUnbalancedParens(noLook)
	if re := try(noParens); re != nil {
		level := regexParensFixed
		if hadLook {
			level = regexLookaroundLiteral
		}
		return re, noParens, level, level.String()
	}

	literal := prefix + regexp.QuoteMeta(pattern)
	re, err := regexp.Compile(literal)
	if err != nil {
		// QuoteMeta 理论上永不失败；真失败时退回最保守的空匹配，
		// 保证搜索工具不会因模式问题整轮崩溃。
		re = regexp.MustCompile(`(?!)`)
	}
	return re, literal, regexLiteralFallback, regexLiteralFallback.String()
}

// regexHintSuffix 把降级提示拼成输出尾注；没有降级时返回空串。
func regexHintSuffix(level regexFallback) string {
	msg := level.String()
	if msg == "" {
		return ""
	}
	return " [" + strings.TrimSuffix(msg, "。") + "]"
}

// describeRegexError 为彻底失败（理论上不会）的情形给出可读错误。
func describeRegexError(pattern string, err error) error {
	return fmt.Errorf("grep: bad pattern %q: %w", pattern, err)
}
