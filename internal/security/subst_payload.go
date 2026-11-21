package security

import "unicode/utf8"

// 本文件提供命令替换（$(...) 与反引号）的纯词法提取原语。它刻意不接入任何
// 运行时护栏：strict 模式的接线由调用方（internal/app）完成。存在的原因是
// sanitizeCommand 会把双引号字符串整体遮盖，导致 `echo "$(mv a b)"` 中的修改类
// 命令对 FindFileEditingCommand 不可见。这里提供有界、纯函数式的提取器，供
// 接线点在分类前恢复替换载荷。
//
// 实现是纯字节扫描。所有分隔符（$ ( ) ` ' " \）都是 ASCII，UTF-8 的续字节
// 均 >= 0x80，绝不会与分隔符冲突，因此扫描不会在 CJK 字符中间切开；仅在按长度
// 截断载荷时通过 truncateSubstitutionPayload 保证不切断 rune。

const (
	// maxSubstitutionDepth 限制递归进入替换载荷的层数。每层递归至少消耗一对
	// 替换定界符，因此终止天然有保证；上限使深层嵌套输入也能被低成本处理。
	maxSubstitutionDepth = 8

	// maxSubstitutionPayloadBytes 限制单个载荷提取的最大字节数，避免恶意超长
	// 替换导致内存与耗时失控。
	maxSubstitutionPayloadBytes = 8192
)

// ExtractCommandSubstitutions 按出现顺序提取 command 中所有命令替换的内部命令
// 文本，包括 $(...) 和反引号 `...`。它纯词法扫描：处理引号内的转义（\$、\`、
// \" 等不会被当成替换起点）、单引号内的字面量不解析替换、$(...) 与反引号可以
// 相互嵌套且括号深度正确配对。返回的新切片不共享入参内存，入参绝不被修改。
//
// 深度与长度都有上界，极深嵌套或超长载荷也会被安全地截断并返回。没有替换时
// 返回 nil。
func ExtractCommandSubstitutions(command string) []string {
	if command == "" {
		return nil
	}
	var out []string
	scanSubstitutions(command, 0, len(command), 0, &out)
	return out
}

// ClassifyWithSubstitutions 判断 command 是否含修改类命令：主命令本身被
// FindFileEditingCommand 判定为修改类，或任一命令替换载荷被判定为修改类，
// 均返回 true。它是纯函数，不改变任何全局状态，也不修改入参。
//
// 该函数是"默认关闭"的原语：在本包内除测试外没有调用点，strict 模式需要它时
// 由上层显式接线。
func ClassifyWithSubstitutions(command string) bool {
	if FindFileEditingCommand(command) != "" {
		return true
	}
	for _, payload := range ExtractCommandSubstitutions(command) {
		if FindFileEditingCommand(payload) != "" {
			return true
		}
	}
	return false
}

// scanSubstitutions 扫描 s[start:end]，把发现的替换载荷追加到 out，并递归扫描
// 每个载荷以提取更深层的替换。depth 是当前递归深度。
func scanSubstitutions(s string, start, end, depth int, out *[]string) {
	if depth > maxSubstitutionDepth {
		return
	}
	for i := start; i < end; {
		switch c := s[i]; {
		case c == '\\':
			// 未加引号的反斜杠转义下一个字节，因此 \$ / \( 不会开启替换。
			if i+1 < end {
				i += 2
			} else {
				i++
			}
		case c == '\'':
			// 单引号内的文本是字面量，不发生替换。
			i = skipSubstitutionSingleQuote(s, i, end)
		case c == '"':
			// 双引号内替换仍然生效（这正是要补的漏洞），但单引号在其中只是普通
			// 字节，因此交给专门的扫描器处理。
			i = scanSubstitutionDoubleQuoted(s, i, end, depth, out)
		default:
			if next, ok := collectSubstitutionAt(s, i, end, depth, out); ok {
				i = next
				continue
			}
			i++
		}
	}
}

// scanSubstitutionDoubleQuoted 处理从 i 处开始的双引号区域：跳过转义字节，遇
// 到未转义的结尾双引号即返回其后偏移；区域内的 $(...) 与反引号仍会被提取。
func scanSubstitutionDoubleQuoted(s string, i, end, depth int, out *[]string) int {
	i++ // 跳过开头的 "
	for i < end {
		c := s[i]
		if c == '\\' {
			// 双引号内的 \$、\`、\"、\\ 都是转义，不能当成替换起点。
			if i+1 < end {
				i += 2
			} else {
				i++
			}
			continue
		}
		if c == '"' {
			return i + 1
		}
		if next, ok := collectSubstitutionAt(s, i, end, depth, out); ok {
			i = next
			continue
		}
		i++
	}
	return end // 未闭合：尽力扫描到末尾（失败即安全）
}

// collectSubstitutionAt 若 s[i] 处是替换起点（$( 或反引号），则提取载荷、记录到
// out 并递归扫描载荷内部的替换，返回载荷结束后的偏移。
func collectSubstitutionAt(s string, i, end, depth int, out *[]string) (int, bool) {
	if s[i] != '`' && !(s[i] == '$' && i+1 < end && s[i+1] == '(') {
		return i, false
	}
	payload, next := readBalancedSubstitution(s, i, end)
	if payload != "" {
		*out = append(*out, payload)
		if depth < maxSubstitutionDepth {
			scanSubstitutions(payload, 0, len(payload), depth+1, out)
		}
	}
	return next, true
}

// readBalancedSubstitution 从 i 处的开界定符（$( 或反引号）读取到与之配对的闭
// 界定符，返回内部文本与闭界定符之后的偏移。它用一个"期望闭符"栈同时处理
// $(...) 与反引号的相互嵌套，并把引号内的内容视为字面量跳过，避免引号中的 ) 或
// 反引号误配对外层。栈深度与载荷长度均有上界。
func readBalancedSubstitution(s string, i, end int) (string, int) {
	var closers []byte
	if s[i] == '$' && i+1 < end && s[i+1] == '(' {
		closers = append(closers, ')')
		i += 2
	} else {
		closers = append(closers, '`')
		i++
	}
	start := i
	j := i
	for j < end && len(closers) > 0 {
		c := s[j]
		switch {
		case c == '\\':
			// 反斜杠转义下一个字节，使其中的定界符不参与配对。
			if j+1 < end {
				j += 2
			} else {
				j++
			}
		case c == '\'':
			j = skipSubstitutionSingleQuote(s, j, end)
		case c == '"':
			j = skipSubstitutionDoubleQuote(s, j, end)
		default:
			top := closers[len(closers)-1]
			switch {
			case c == '`' && top == '`':
				closers = closers[:len(closers)-1]
				j++
				if len(closers) == 0 {
					return truncateSubstitutionPayload(s[start : j-1]), j
				}
			case c == '`': // 反引号嵌套在 $(...) 内
				if len(closers) >= maxSubstitutionDepth {
					return truncateSubstitutionPayload(s[start:end]), end
				}
				closers = append(closers, '`')
				j++
			case c == '$' && j+1 < end && s[j+1] == '(':
				if len(closers) >= maxSubstitutionDepth {
					return truncateSubstitutionPayload(s[start:end]), end
				}
				closers = append(closers, ')')
				j += 2
			case c == '(':
				// 普通子 shell 括号，也需配对，否则内层 ) 会提前闭合外层。
				if len(closers) >= maxSubstitutionDepth {
					return truncateSubstitutionPayload(s[start:end]), end
				}
				closers = append(closers, ')')
				j++
			case c == ')' && top == ')':
				closers = closers[:len(closers)-1]
				j++
				if len(closers) == 0 {
					return truncateSubstitutionPayload(s[start : j-1]), j
				}
			default:
				j++
			}
		}
	}
	return truncateSubstitutionPayload(s[start:j]), j
}

// skipSubstitutionSingleQuote 跳过从 i 处开始的单引号字面量，返回其后偏移。
func skipSubstitutionSingleQuote(s string, i, end int) int {
	i++ // 跳过开头的 '
	for i < end {
		if s[i] == '\'' {
			return i + 1
		}
		i++
	}
	return end
}

// skipSubstitutionDoubleQuote 跳过从 i 处开始的双引号字面量（含其中的转义），
// 返回其后偏移。引号内的替换定界符在这里被视作字面量，配对工作由外层递归完成。
func skipSubstitutionDoubleQuote(s string, i, end int) int {
	i++ // 跳过开头的 "
	for i < end {
		if s[i] == '\\' && i+1 < end {
			i += 2
			continue
		}
		if s[i] == '"' {
			return i + 1
		}
		i++
	}
	return end
}

// truncateSubstitutionPayload 把载荷限制在 maxSubstitutionPayloadBytes 字节以内，
// 并在 rune 边界处截断，确保 CJK 等多字节字符不会被切开。
func truncateSubstitutionPayload(s string) string {
	if len(s) <= maxSubstitutionPayloadBytes {
		return s
	}
	cut := maxSubstitutionPayloadBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
