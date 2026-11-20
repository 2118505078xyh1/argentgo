package cli

import (
	"strings"
	"unicode/utf8"
)

// confirmAdvice 生成交互式确认提示文案。它是纯函数：不读 stdin、不访问终端、
// 不触碰全局状态，只读参数并返回字符串。因此既能安全地用于表驱动测试，也能
// 在将来由真实的确认路径 opt-in 调用，而不改变当前任何命令的字节级输出
// （当前尚未接线，见接线点报告）。
//
// isTTY 决定文案走哪条分支：
//   - true（有交互终端）：提示"将执行 X，输入 y 确认"；
//   - false（无终端或流被重定向）：提示改用 --yes 标志，避免在无法读取
//     输入的环境里阻塞等待。
//
// action 是待确认操作的简短描述，插入文案前会先脱敏：Unicode 安全的
// TrimSpace 去掉两侧空白（多字节字符不会被切断），控制字符与 ANSI 转义序列
// 被剥离（防止提示被注入的转义序列操纵终端），危险词按不区分大小写的方式
// 替换为占位符，使敏感或破坏性片段不会原样出现在提示里。
//
// 空 action 在 TTY 分支退化为通用的"将执行操作"，不会生成"将执行 ，输入 y"
// 这种带空参数的提示；非 TTY 分支与 action 无关，始终返回同一个 --yes 提示。
func confirmAdvice(action string, isTTY bool) string {
	if !isTTY {
		return confirmNonTTYAdvice
	}
	a := sanitizeConfirmAction(action)
	if a == "" {
		return "将执行操作，输入 y 确认"
	}
	return "将执行 " + a + "，输入 y 确认"
}

const (
	// confirmNonTTYAdvice 是无终端时给出的统一提示文案。
	confirmNonTTYAdvice = "非交互环境请用 --yes 标志"
	// confirmRedactedPlaceholder 代替命中危险词的片段。
	confirmRedactedPlaceholder = "[已脱敏]"
)

// confirmDangerousWords 是需要在确认提示中脱敏的词表。表中项均为 ASCII，
// 匹配时不区分大小写，命中即替换为 confirmRedactedPlaceholder。这里刻意
// 保守：误报的代价只是一段文字被遮蔽，比泄露敏感片段或原样展示破坏性命令
// 更安全。若将来接入 i18n，可在调用方做 locale 映射，不改变本函数语义。
var confirmDangerousWords = []string{
	// 破坏性命令。
	"rm -rf", "mkfs", "dd if=", "chmod 777", "drop table", ":(){", "shutdown", "reboot",
	// 提权词。
	"sudo", "runas",
	// 凭据词。
	"password", "passwd", "secret", "token", "api_key", "apikey", "private_key",
}

// sanitizeConfirmAction 清理 action：先按 rune 安全地去掉两侧空白，再剥离
// 控制字符和转义序列，最后脱敏危险词。全部步骤都不按字节截断多字符，
// 因此 CJK 名称会原样保留。
func sanitizeConfirmAction(action string) string {
	clean := stripConfirmControls(strings.TrimSpace(action))
	if clean == "" {
		return ""
	}
	return redactConfirmDangerous(clean)
}

// stripConfirmControls 剥离 action 中的控制字符和 ANSI CSI/OSC 转义序列。
// 确认提示会直接写进终端，若原样嵌入 ESC 序列，恶意的 action 就能改写光标、
// 清屏或伪造提示；按 rune 过滤既保留 CJK 字符，又保证提示是一行纯文本。
func stripConfirmControls(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r != 0x1b {
			i += size
			if !isConfirmControl(r) {
				b.WriteRune(r)
			}
			continue
		}
		// ESC：跳过整条转义序列，不把它写入结果。
		i += size
		if i >= len(s) {
			break
		}
		next, nextSize := utf8.DecodeRuneInString(s[i:])
		i += nextSize
		switch next {
		case '[': // CSI：以 0x40..0x7E 范围内的最终字节结束。
			for i < len(s) {
				cr, sz := utf8.DecodeRuneInString(s[i:])
				i += sz
				if cr >= 0x40 && cr <= 0x7e {
					break
				}
			}
		case ']': // OSC：以 BEL(0x07) 或 ST(ESC \) 结束。
			for i < len(s) {
				cr, sz := utf8.DecodeRuneInString(s[i:])
				i += sz
				if cr == 0x07 {
					break
				}
				if cr == 0x1b {
					if i < len(s) && s[i] == '\\' {
						i++
					}
					break
				}
			}
		default:
			// 其他两字节 Fe 序列：ESC 与 next 都已跳过。
		}
	}
	return b.String()
}

// isConfirmControl 报告 r 是否为应被剔除的控制字符，包含 C0（换行、制表等）、
// C1 和 DEL。UTF-8 中的 CJK rune 都大于此范围，不会被误判。
func isConfirmControl(r rune) bool {
	if r < 0x20 || r == 0x7f {
		return true
	}
	return r >= 0x80 && r <= 0x9f
}

// redactConfirmDangerous 把 action 中命中的危险词替换为占位符。危险词都是
// ASCII，而多字节 rune 的任一字节都 >= 0x80，不会与 ASCII 字母相等，因此
// 匹配只能发生在 rune 边界；未命中时按整个 rune 前进，CJK 字符不会被切断。
// 使用 EqualFold 实现不区分大小写，同时保留非危险部分原有的大小写。
func redactConfirmDangerous(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		matched := ""
		for _, w := range confirmDangerousWords {
			if len(w) <= len(s)-i && strings.EqualFold(s[i:i+len(w)], w) {
				matched = w
				break
			}
		}
		if matched != "" {
			b.WriteString(confirmRedactedPlaceholder)
			i += len(matched)
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		b.WriteRune(r)
		i += size
	}
	return b.String()
}
