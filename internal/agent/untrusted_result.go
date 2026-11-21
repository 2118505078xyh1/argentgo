// Package agent —— 不可信工具结果封装原语。
//
// 工具输出在回灌给模型之前缺乏明确的来源标记，是 prompt injection 的一个缺口：
// 模型无法区分“系统/用户指令”和“工具读到的文本”，一段恶意文件内容可能伪装成指令。
// 本文件提供一个纯标准库、无 I/O、无调用方的小原语：把工具输出封装进带固定边界的
// 文本块，并在渲染时转义所有能构成边界的 ASCII 字符，使内容无法伪造或突破边界。
//
// 这是刻意独立的：它不参与任何现有消息组装，也不会改变任何提示词的字节。
// 调用方若要使用它，需要显式调用 WrapToolResult / Render，默认行为完全不受影响。
package agent

import "strings"

// 固定的边界常量。渲染与解析共用同一组常量，保证两端永不漂移。
const (
	toolResultOpenPrefix = "<tool_result "
	toolResultToolAttr   = `tool="`
	toolResultTrustAttr  = ` trust="`
	toolResultOpenEnd    = ">\n"
	toolResultCloseTag   = "</tool_result>"
	toolResultTail       = "\n" + toolResultCloseTag

	// untrustedTrustValue 是 WrapToolResult 写入的默认信任级别。
	untrustedTrustValue = "untrusted"
)

// UntrustedResult 是一份带来源标记的工具输出。
// Content 始终按原始字节保存（封装时不做任何改写）；转义只发生在 Render 阶段。
// Trust 表示信任级别，约定取值如 "untrusted" / "sanitized"。
type UntrustedResult struct {
	Tool    string
	Content string
	Trust   string
}

// WrapToolResult 把工具输出封装成带“不可信”来源标记的结构。
// content 原样保留，字节不变；仅设置 Tool 与默认的 Trust。
func WrapToolResult(tool, content string) UntrustedResult {
	return UntrustedResult{Tool: tool, Content: content, Trust: untrustedTrustValue}
}

// Render 把结果渲染成带明确边界的文本：
//
//	<tool_result tool="read" trust="untrusted">
//	...content...
//	</tool_result>
//
// 边界由固定常量生成；工具名、信任级别与内容中的 < > " \ 都会被编码成 \uXXXX，
// 因此转义后的文本里除了结构性标签之外没有原始的 < > "，内容无法伪造闭合标签或属性。
// 编码是可逆的（见 UnwrapToolResult），且只触碰 ASCII 字节，CJK 等多字节字符保持原样。
func (r UntrustedResult) Render() string {
	var b strings.Builder
	b.WriteString(toolResultOpenPrefix)
	b.WriteString(toolResultToolAttr)
	b.WriteString(escapeUntrusted(r.Tool))
	b.WriteByte('"')
	b.WriteString(toolResultTrustAttr)
	b.WriteString(escapeUntrusted(r.Trust))
	b.WriteByte('"')
	b.WriteString(toolResultOpenEnd)
	b.WriteString(escapeUntrusted(r.Content))
	b.WriteString(toolResultTail)
	return b.String()
}

// IsWrapped 尽力判断 s 是否像一个由 Render 生成的封装块。
// 它只检查首尾边界，刻意宽松——真正的结构解析交给 UnwrapToolResult。
func IsWrapped(s string) bool {
	return strings.HasPrefix(s, toolResultOpenPrefix) && strings.HasSuffix(s, toolResultCloseTag)
}

// UnwrapToolResult 尽力把 Render 生成的文本解析回 UntrustedResult。
// 输入不是合法封装块时返回零值与 false。因为转义后属性值和内容中不会出现原始的
// " < >，解析可以安全地按这些 ASCII 分隔符定位字段，不会被伪造的内容诱导。
func UnwrapToolResult(s string) (UntrustedResult, bool) {
	if !IsWrapped(s) {
		return UntrustedResult{}, false
	}

	rest := s[len(toolResultOpenPrefix):]
	if !strings.HasPrefix(rest, toolResultToolAttr) {
		return UntrustedResult{}, false
	}
	rest = rest[len(toolResultToolAttr):]

	i := strings.IndexByte(rest, '"')
	if i < 0 {
		return UntrustedResult{}, false
	}
	tool := unescapeUntrusted(rest[:i])
	rest = rest[i+1:]

	if !strings.HasPrefix(rest, toolResultTrustAttr) {
		return UntrustedResult{}, false
	}
	rest = rest[len(toolResultTrustAttr):]

	i = strings.IndexByte(rest, '"')
	if i < 0 {
		return UntrustedResult{}, false
	}
	trust := unescapeUntrusted(rest[:i])
	rest = rest[i+1:]

	if !strings.HasPrefix(rest, toolResultOpenEnd) {
		return UntrustedResult{}, false
	}
	rest = rest[len(toolResultOpenEnd):]

	if !strings.HasSuffix(rest, toolResultTail) {
		return UntrustedResult{}, false
	}
	content := unescapeUntrusted(strings.TrimSuffix(rest, toolResultTail))
	return UntrustedResult{Tool: tool, Content: content, Trust: trust}, true
}

// escapeUntrusted 把可能破坏边界的四个 ASCII 字符编码成 \uXXXX。
// 逐字节复制其余字节：UTF-8 的多字节序列不包含这些 ASCII 值，因此 CJK 等字符原样保留。
func escapeUntrusted(s string) string {
	if !strings.ContainsAny(s, "\\\"<>") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			b.WriteString(`\u005c`)
		case '"':
			b.WriteString(`\u0022`)
		case '<':
			b.WriteString(`\u003c`)
		case '>':
			b.WriteString(`\u003e`)
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// unescapeUntrusted 是 escapeUntrusted 的逆操作：单遍扫描，只解码本包产生的四种转义。
// 单遍处理避免了多次 ReplaceAll 在相邻转义边界上的歧义，并保证多字节字符不被破坏。
func unescapeUntrusted(s string) string {
	if !strings.Contains(s, `\u`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if ch, n, ok := decodeUntrustedEscape(s, i); ok {
			b.WriteByte(ch)
			i += n
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// decodeUntrustedEscape 在 s[i:] 开头识别本包产生的转义序列，返回解码字节与消耗长度。
func decodeUntrustedEscape(s string, i int) (byte, int, bool) {
	if i+6 > len(s) || s[i] != '\\' || s[i+1] != 'u' {
		return 0, 0, false
	}
	switch s[i+2 : i+6] {
	case "005c":
		return '\\', 6, true
	case "0022":
		return '"', 6, true
	case "003c":
		return '<', 6, true
	case "003e":
		return '>', 6, true
	default:
		return 0, 0, false
	}
}
