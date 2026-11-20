package tui

import (
	"net/url"
	"strings"
	"unicode"
)

// OSC 8 超链接用 ST（ESC '\'）终结参数段：开链为 OSC 8;;URI ST，
// 其后紧跟显示文本，最后用空 URI 的闭链 OSC 8;; ST 结束链接范围。
// 这里刻意选用 ST 而非 BEL：ST 是 ECMA-48 的标准字符串终结符，
// 与 stripCursorControl/scanOSCSequence 的解析逻辑完全一致。
const (
	osc8Prefix = "\x1b]8;;"         // 开链/闭链前缀，后接 URI 与终结符
	oscST      = "\x1b\\"           // ST：字符串终结符
	osc8Reset  = osc8Prefix + oscST // 空 URI 闭链：结束当前链接范围
)

// makeOSC8Link 为可信的 http/https 地址生成可被终端点击的 OSC 8 超链接，
// 形如 ESC ] 8;;URL ST text ESC ] 8;; ST。
//
// 它是纯函数，不依赖渲染器或全局状态。安全约束如下：
//   - 只接受 http/https 且带主机的绝对 URL；其余协议（javascript、data、
//     file 等）或格式错误的 URL 一律降级为无链接的纯文本；
//   - URL 与文本中的控制字符（含 ESC 与 BEL）会被丢弃，避免提前终结
//     OSC 序列或向终端注入新的控制指令；
//   - text 为空（或清洗后为空）时改用 URL 作为显示文本；
//   - 逐 rune 处理，CJK 汉字与 emoji 等多字节字符保持字节不变。
func makeOSC8Link(urlValue, text string) string {
	if !isSafeHTTPURL(urlValue) {
		// 非法 URL 绝不生成链接：静默降级为去掉控制字符的纯文本。
		return scrubControlChars(text)
	}
	if text == "" {
		text = urlValue
	}
	label := scrubControlChars(text)
	if label == "" {
		// 文本全是控制字符时，退回已验证的 URL，保证有可读标签。
		label = urlValue
	}
	return osc8Prefix + urlValue + oscST + label + osc8Reset
}

// isSafeHTTPURL 报告 raw 是否为允许放进 OSC 8 的安全绝对 URL。
// 控制字符先被拒绝（url.Parse 对部分控制字符也会报错，但这里显式拦截
// ESC/BEL 这类会直接破坏 OSC 帧的字节），再由 net/url 校验结构与协议。
func isSafeHTTPURL(raw string) bool {
	if raw == "" {
		return false
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return false
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Scheme, "http") || strings.EqualFold(u.Scheme, "https")
}

// scrubControlChars 丢弃 s 中的所有 Unicode 控制字符，保留其余 rune
// （包括 CJK 与 emoji）不变。按 rune 遍历可同时覆盖 ASCII C0、DEL、
// C1 控制区，而不会碰多字节 UTF-8 序列的内部字节。
func scrubControlChars(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// preserveOSC8 报告 s 是否含有一个结构完整的 OSC 8 超链接开链：
// 以 OSC 8; 开头、带非空 URI、并以 BEL 或 ST 终结。它有意不校验
// URI 协议，只判断链接结构是否完整 —— stripCursorControl 会原样
// 保留所有 OSC 序列，因此这里命中的链接经过 normalizeToolOutput
// 之后依然存在。空 URI 的闭链（链接结束标记）不算作链接。
func preserveOSC8(s string) bool {
	for i := 0; i+1 < len(s); i++ {
		if s[i] != 0x1b || s[i+1] != ']' {
			continue
		}
		end := scanOSCSequence(s, i+2)
		termStart, terminated := oscTerminatorStart(s, i+2, end)
		if terminated {
			if uri, ok := osc8URI(s[i+2 : termStart]); ok && uri != "" {
				return true
			}
		}
		// 无论是否终结，都跳过整段 OSC，避免把其载荷误判为新的转义。
		i = end - 1
	}
	return false
}

// oscTerminatorStart 返回 start..end 范围内 OSC 终结符的起始下标。
// end 由 scanOSCSequence 给出，是终结符之后的位置；未终结时返回
// ok=false，避免把输入末尾误当作链接的结束。
func oscTerminatorStart(s string, start, end int) (termStart int, ok bool) {
	if end <= start || end > len(s) {
		return 0, false
	}
	if s[end-1] == 0x07 { // BEL
		return end - 1, true
	}
	if end >= start+2 && s[end-1] == '\\' && s[end-2] == 0x1b { // ST
		return end - 2, true
	}
	return 0, false
}

// osc8URI 从 OSC 载荷（不含终结符）中取出 URI。载荷格式为
// "8;<params>;<URI>"，params 允许为空；缺少第二个分号或 URI 为空时
// 返回 ok=false（后者对应关闭链接的空 OSC 8;; 序列）。
func osc8URI(payload string) (uri string, ok bool) {
	if !strings.HasPrefix(payload, "8;") {
		return "", false
	}
	rest := payload[len("8;"):]
	semi := strings.IndexByte(rest, ';')
	if semi < 0 {
		return "", false
	}
	return rest[semi+1:], true
}
