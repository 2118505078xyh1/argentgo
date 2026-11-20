package tui

import (
	"os/exec"
	"runtime"
	"unicode"
)

// editorLookPath 是 Windows 上使用的 PATH 解析器，把裸命令
// 名转成完整路径，使 .cmd/.bat 包装（如 code -> code.cmd）能
// 正确启动。它是包变量，仿照 keys.go 中的 clipboardWrite，
// 使测试可以换成确定性的解析器；生产使用 exec.LookPath。
var editorLookPath = exec.LookPath

// ResolveExternalEditor 从 VISUAL 和 EDITOR 环境变量解析
// 外部编辑器命令。VISUAL 优先于 EDITOR，
// 像 "code --wait" 这样的原始值会被拆成命令及其参数。
//
// 环境查找是注入的（而非调用 os.Getenv），因此这是一个纯
// 查询：调用方提供环境，测试从不触碰真实环境。
// 在 Windows 上命令还会通过 PATH 解析；
// 当解析失败时保留裸名不变，与原实现的包装行为
// 一致。
//
// 当两个变量都未设置，或所选值没有命令 token 时
// 返回 ok=false。
func ResolveExternalEditor(lookup func(string) (string, bool)) (cmd string, args []string, ok bool) {
	if lookup == nil {
		return "", nil, false
	}
	raw, found := lookup("VISUAL")
	if !found {
		raw, found = lookup("EDITOR")
	}
	if !found {
		return "", nil, false
	}
	parts := splitEditorCommand(raw)
	if len(parts) == 0 {
		return "", nil, false
	}
	cmd = parts[0]
	if len(parts) > 1 {
		args = parts[1:]
	}
	if runtime.GOOS == "windows" {
		if resolved, err := editorLookPath(cmd); err == nil && resolved != "" {
			cmd = resolved
		}
	}
	return cmd, args, true
}

// splitEditorCommand 把编辑器命令字符串按字段拆分，遵循
// 单引号和双引号，使含空格的路径留在同一字段。反斜杠
// 只转义紧跟其后的引号或反斜杠，因此普通的 Windows
// 路径分隔符原样保留。
func splitEditorCommand(raw string) []string {
	rs := []rune(raw)
	var fields []string
	var cur []rune
	var quote rune
	flush := func() {
		if len(cur) > 0 {
			fields = append(fields, string(cur))
			cur = nil
		}
	}
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if r == '\\' && i+1 < len(rs) {
			next := rs[i+1]
			if next == quote || next == '\\' || (quote == 0 && (next == '"' || next == '\'')) {
				i++
				cur = append(cur, next)
				continue
			}
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				cur = append(cur, r)
			}
			continue
		}
		switch {
		case r == '\'' || r == '"':
			quote = r
		case unicode.IsSpace(r):
			flush()
		default:
			cur = append(cur, r)
		}
	}
	flush()
	return fields
}
