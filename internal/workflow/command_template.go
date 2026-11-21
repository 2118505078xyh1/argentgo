package workflow

import (
	"errors"
	"fmt"
	"strings"
)

// ErrMissingVar 表示命令模板引用了未在 Vars 中提供、也没有默认值的变量。
// 调用方可用 errors.Is 精确匹配这一类渲染失败。
var ErrMissingVar = errors.New("workflow: missing command template variable")

// CommandTemplate 是一条命令模板及其变量值。Template 中的 {{var}} 与
// {{var:-default}} 占位符在渲染时被替换；Vars 永远不会被修改。
type CommandTemplate struct {
	Template string
	Vars     map[string]string
}

// parseTemplateToken 把占位符内部文本拆成变量名与可选的默认值。只有第一次
// 出现的 ":-" 被当作分隔符，其后的内容（包括更多的 ":-"）原样作为默认值。
// 名字与默认值两端都会被裁剪空白。
func parseTemplateToken(inner string) (name, def string, hasDefault bool) {
	if i := strings.Index(inner, ":-"); i >= 0 {
		return strings.TrimSpace(inner[:i]), strings.TrimSpace(inner[i+2:]), true
	}
	return strings.TrimSpace(inner), "", false
}

// safeShellWord 报告 s 能否不加引用就作为单个 shell 词。只放行 ASCII
// 字母、数字与一小部分不含 shell 元字符的标点；空串一律需要引用，以免
// 在命令行里丢掉一个空参数。CJK 等非 ASCII 字符不在放行集合里，会被引用，
// 但它们本身不含 shell 元字符，引用后依旧安全且不会被破坏。
func safeShellWord(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("_@%+=:,./-", r):
		default:
			return false
		}
	}
	return true
}

// quoteShellValue 在值为空或含特殊字符时，用单引号包裹并把内嵌的单引号
// 改写成「先结束引用、再用反斜杠转义一个引号、最后重新进入引用」的三段序列。这是 POSIX
// shell 下无需依赖其他转义规则就能安全嵌入任意字节的方式，可降低把变量
// 值拼进命令时的注入风险。安全词保持原样，避免给普通路径平添噪音。
func quoteShellValue(s string) string {
	if safeShellWord(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// RenderCommand 渲染命令模板。缺失且无默认值的变量返回包裹 ErrMissingVar
// 的错误（可用 errors.Is 匹配）；{{var:-default}} 在变量缺席时改用默认值。
// 替换进来的值与默认值都经过 shell 引用处理。未被识别的标记原样保留，且
// 替换产生的内容不再被重扫，因此包含 "{{...}}" 的值无法注入新标记。
// 函数是纯函数：不修改 t、t.Vars，也不共享可变状态。
func RenderCommand(t CommandTemplate) (string, error) {
	if !strings.Contains(t.Template, "{{") {
		return t.Template, nil
	}
	var out strings.Builder
	rest := t.Template
	for {
		start, tokenEnd, inner, ok := nextPlaceholder(rest)
		if !ok {
			out.WriteString(rest)
			break
		}
		name, def, hasDefault := parseTemplateToken(inner)
		out.WriteString(rest[:start])
		if name == "" {
			// 空变量名是无效标记：原样保留，交由调用方处理。
			out.WriteString(rest[start:tokenEnd])
		} else {
			value, found := t.Vars[name]
			if !found {
				if !hasDefault {
					return "", fmt.Errorf("%w: %q", ErrMissingVar, name)
				}
				value = def
			}
			out.WriteString(quoteShellValue(value))
		}
		rest = rest[tokenEnd:]
	}
	return out.String(), nil
}

// TemplateVars 按首次出现的顺序返回模板中的变量名（去重）。对于
// {{var:-default}} 只报告 var，无效的空名标记与未终止的标记被忽略。
// 函数是纯函数。
func TemplateVars(s string) []string {
	seen := make(map[string]bool)
	var names []string
	rest := s
	for {
		_, tokenEnd, inner, ok := nextPlaceholder(rest)
		if !ok {
			break
		}
		name, _, _ := parseTemplateToken(inner)
		if name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
		rest = rest[tokenEnd:]
	}
	return names
}
