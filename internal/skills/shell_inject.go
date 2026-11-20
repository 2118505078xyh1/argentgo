package skills

import (
	"strings"
	"unicode/utf8"
)

// MaxShellCommands 限制单个技能正文可暴露的不同 shell 命令数量。超过此限制的额外唯一命令被忽略，这同时限制了审批队列和调用方可能被诱骗去做的工作。
// 可暴露的不同 shell 命令数量。超过此限制的额外唯一命令被忽略，这
// 同时限制了审批队列和调用方可能被诱骗去做的工作。
const MaxShellCommands = 32

// ShellKillSwitchEnv 是调用方检查以彻底禁用
// 技能 shell 注入的环境变量。本包从不读取环境；
// ShellKillSwitchEnv 是调用方检查以彻底禁用技能 shell 注入的环境变量。本包从不读取环境；门控是下面纯函数 ShellInjectAllowed。
const ShellKillSwitchEnv = "KILO_DISABLE_SKILL_SHELL"

// Span 是交给 ParseShellCommands 的技能正文中半开字节区间 [Start, End)。它覆盖整个可执行标记，即起始反引号、前导 '!'、命令和结束反引号。
// 的技能正文中半开字节区间 [Start, End)。它覆盖整个可执行标记，即
// 起始反引号、前导 '!'、命令和结束反引号。
// 字节偏移（而非 rune 偏移）匹配 Go 的字符串切片和 regexp 索引。
type Span struct {
	Start int
	End   int
}

// ParseShellCommands 扫描一次技能正文，返回写在可执行位置的 shell 命令及每个命令占据的区间。
// 写在可执行位置的 shell 命令及每个命令占据的区间。
//
// 唯一的可执行形式是首字符为
// 唯一的可执行形式是首字符为 '!' 的内联代码跨度：`!git status` 产生 "git status"。惰性区域完全跳过：
//
//   - 围栏代码块（``` ... ```），包括其起始和结束栅栏
//     markers;
//   - 不以 '!' 开头的普通内联代码（`echo hi`）
//   - 反斜杠转义的反引号（\`），它们被转义且绝不划定跨度。
//
// 解析刻意保守：游离的栅栏或未终止的
//   - 反斜杠转义的反引号（\`），它们被转义且绝不划定跨度。
//
// 解析刻意保守：游离的栅栏或未终止的内联跨度只会抑制命令，绝不会凭空发明命令。扫描按 rune 感知（使 CJK 内容不会破坏偏移）且该函数从不
// 执行任何内容。重复命令被丢弃，保留第一次
// 执行任何内容。重复命令被丢弃，保留第一次出现，且最多返回 MaxShellCommands 个唯一命令。
func ParseShellCommands(body string) (commands []string, spans []Span) {
	seen := make(map[string]bool)
	for i := 0; i < len(body); {
		r, size := utf8.DecodeRuneInString(body[i:])
		switch {
		case r == '\\':
			// 转义的 rune 是字面量：跳过反斜杠及其目标，使转义的反引号无法开启内联跨度。
			// 使转义的反引号无法开启内联跨度。
			i += size
			if i < len(body) {
				_, next := utf8.DecodeRuneInString(body[i:])
				i += next
			}
		case r == '`':
			if strings.HasPrefix(body[i:], "```") {
				// 围栏块：在匹配的结束栅栏（或 EOF）之前保持惰性。
				i += 3
				for i < len(body) {
					if strings.HasPrefix(body[i:], "```") {
						i += 3
						break
					}
					_, s := utf8.DecodeRuneInString(body[i:])
					i += s
				}
				continue
			}

			open := i
			contentStart := i + 1
			executable := contentStart < len(body) && body[contentStart] == '!'
			if executable {
				contentStart++ // the '!' is a marker, not part of the command
			}
			close, ok := findClosingBacktick(body, i+1)
			if !ok {
				// 没有结束反引号：把起始的反引号当作字面文本。
				i += size
				continue
			}
			if executable {
				cmd := strings.TrimSpace(body[contentStart:close])
				if cmd != "" && !seen[cmd] && len(commands) < MaxShellCommands {
					seen[cmd] = true
					commands = append(commands, cmd)
					spans = append(spans, Span{Start: open, End: close + 1})
				}
			}
			i = close + 1
		default:
			i += size
		}
	}
	return commands, spans
}

// 没有结束反引号：把起始的反引号当作字面文本。
// findClosingBacktick 返回 from 或之后第一个未转义反引号的字节索引。转义的反引号（\`）不是分隔符。
func findClosingBacktick(body string, from int) (int, bool) {
	for j := from; j < len(body); {
		switch body[j] {
		case '\\':
			j++
			if j < len(body) {
				_, size := utf8.DecodeRuneInString(body[j:])
				j += size
			}
		case '`':
			return j, true
		default:
			_, size := utf8.DecodeRuneInString(body[j:])
			j += size
		}
	}
	return 0, false
}

// ShellInjectAllowed 是对已解析命令采取行动的门控。只有在技能可信且
// ShellInjectAllowed 是对已解析命令采取行动的门控。只有在技能可信且 kill-switch 未设置时命令才可被提交执行。它是纯函数：读取 KILO_DISABLE_SKILL_SHELL 和判断
// kill-switch 未设置时命令才可被提交执行。它是纯函数：读取 KILO_DISABLE_SKILL_SHELL 和判断
// 可信度是调用方的职责。
func ShellInjectAllowed(skillTrusted, killSwitch bool) bool {
	return skillTrusted && !killSwitch
}
