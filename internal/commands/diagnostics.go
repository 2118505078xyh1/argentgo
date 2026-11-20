package commands

// 命令发现诊断。
//
// Discover 和 Set.Add 会刻意丢弃名称已被占用的候选，
// 而 ParseStrict 拒绝格式错误的 frontmatter。这使解析后的
// Set 保持精简，却掩盖了命令消失的原因。这里的函数是纯的、
// 只读的检查器，供希望暴露这些决策的调用方使用（doctor
// 输出、UI 警告、测试）：它们从不读取文件、修改输入或
// 改变发现行为。

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
)

// DiagnosticKind 对一种命令发现问题进行分类。
type DiagnosticKind string

const (
	DiagnosticInvalidFrontmatter DiagnosticKind = "invalid-frontmatter"
	DiagnosticInvalidName        DiagnosticKind = "invalid-name"
	DiagnosticEmptyBody          DiagnosticKind = "empty-body"
	DiagnosticInvalidContent     DiagnosticKind = "invalid-content"
	DiagnosticMissingArgument    DiagnosticKind = "missing-argument"
	DiagnosticDuplicateName      DiagnosticKind = "duplicate-name"
	DiagnosticDuplicateSource    DiagnosticKind = "duplicate-source"
)

// Diagnostic 表示检查命令候选时发现的一个问题。
// 与名称相关的诊断会设置 Name。Winner 是
// discovery 为重复名称或来源保留的、优先级更高的候选的路径。
// Line 是语法错误在 frontmatter 中的 1 基行号，其他情况为零。
type Diagnostic struct {
	Kind    DiagnosticKind
	Path    string
	Line    int
	Name    string
	Winner  string
	Message string
}

// DiagnoseCommands 报告按优先级排序的候选列表中的所有问题。
// 同名或同路径的第一个候选被视为胜者，
// 与 Discover 和 Set.Add 一致。不进行任何过滤或修改；nil 或
// 空列表不产生任何诊断。
//
// 调用方应传入未去重的候选（每个解析过的命令文件一项，
// 来源按优先级排序），以便被遮蔽的命令仍然可见。
func DiagnoseCommands(cmds []Command) []Diagnostic {
	var out []Diagnostic

	// 逐候选校验，按输入顺序进行。
	for _, cmd := range cmds {
		path := diagnosticPath(cmd.Path)
		if !validCommandName(cmd.Name) {
			out = append(out, Diagnostic{
				Kind: DiagnosticInvalidName, Path: path,
				Message: fmt.Sprintf("invalid command name %q", cmd.Name),
			})
		}
		if strings.TrimSpace(cmd.Body) == "" {
			out = append(out, Diagnostic{
				Kind: DiagnosticEmptyBody, Path: path,
				Message: "command body is empty",
			})
		}
		for _, warning := range cmd.Warnings {
			out = append(out, Diagnostic{
				Kind: DiagnosticInvalidContent, Path: path,
				Message: clipFinding(warning, 160),
			})
		}
		// 读取位置占位符的正文应说明该
		// 位置的含义。命名占位符（$ARGUMENTS/$@）自带说明，
		// 无需提示。
		if highest := highestPosition(cmd.Body); highest > 0 && strings.TrimSpace(cmd.ArgumentHint) == "" {
			out = append(out, Diagnostic{
				Kind: DiagnosticMissingArgument, Path: path,
				Message: fmt.Sprintf("body references $%d but argument-hint is missing", highest),
			})
		}
	}

	// 被遮蔽的名称。按裁剪后的名称分组还能捕获仅在首尾空白处
	// 有差异的名称，Discover 会保留这类名称，但 Get 解析
	// 为第一个条目。
	first := make(map[string]int, len(cmds))
	for i, cmd := range cmds {
		name := strings.TrimSpace(cmd.Name)
		if name == "" {
			continue // already reported as an invalid name
		}
		if j, ok := first[name]; ok {
			winner := diagnosticPath(cmds[j].Path)
			out = append(out, Diagnostic{
				Kind: DiagnosticDuplicateName, Path: diagnosticPath(cmd.Path),
				Name: name, Winner: winner,
				Message: fmt.Sprintf("command %q is shadowed by %s", name, winner),
			})
			continue
		}
		first[name] = i
	}

	// 同一文件被贡献了两次（例如两个来源指向同一目录）。
	seen := make(map[string]int, len(cmds))
	for i, cmd := range cmds {
		key := pathKey(cmd.Path)
		if key == "" {
			continue
		}
		if j, ok := seen[key]; ok {
			winner := diagnosticPath(cmds[j].Path)
			path := diagnosticPath(cmd.Path)
			out = append(out, Diagnostic{
				Kind: DiagnosticDuplicateSource, Path: path,
				Winner:  winner,
				Message: fmt.Sprintf("command file %s was contributed more than once (first from %s)", path, winner),
			})
			continue
		}
		seen[key] = i
	}
	return out
}

// DiagnoseCommandText 诊断单个原始 markdown 命令文件。它是
// 感知 frontmatter 的入口：语法错误变成携带文件及其 1 基行号的
// Diagnostic，而可解析的文本交由 DiagnoseCommands
// 检查。不读取任何文件。
func DiagnoseCommandText(text, path string) []Diagnostic {
	text = strings.TrimPrefix(text, "\ufeff")
	if frontmatter, _, ok := splitFrontmatter(text); ok {
		if _, err := parseFieldsStrict(frontmatter); err != nil {
			return []Diagnostic{{
				Kind: DiagnosticInvalidFrontmatter, Path: path,
				Line: err.line, Message: err.msg,
			}}
		}
	}
	cmd, err := ParseStrict(text, path)
	if err != nil {
		// 防御性处理：上述检查已接受该文本，因此这不可能发生；
		// 但仍将结果保留为诊断，而非半解析的命令。
		return []Diagnostic{{
			Kind: DiagnosticInvalidFrontmatter, Path: path, Message: err.Error(),
		}}
	}
	return DiagnoseCommands([]Command{cmd})
}

func diagnosticPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return "<unknown>"
	}
	return path
}

// pathKey 规范化路径以便比较重复来源。在 Windows 上
// 文件系统不区分大小写，因此仅大小写不同不能掩盖重复。
func pathKey(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	key := filepath.Clean(path)
	if filepath.Separator == '\\' {
		key = strings.ToLower(key)
	}
	return key
}

func validCommandName(name string) bool {
	segments := strings.Split(name, ":")
	if len(segments) > 2 {
		return false
	}
	for _, segment := range segments {
		if !validNameSegment(segment) {
			return false
		}
	}
	return true
}

func validNameSegment(segment string) bool {
	if segment == "" {
		return false
	}
	for _, r := range segment {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

// highestPosition 镜像 substituteArgs 中的标记扫描，使诊断与
// 展开对哪些 $-占位符是真实的一致。它不做任何展开：
// 诊断只是只读观察。
func highestPosition(body string) int {
	highest := 0
	for i := 0; i < len(body); {
		end, index, named, ok := dollarToken(body, i)
		if !ok {
			i++
			continue
		}
		if !named && index > highest {
			highest = index
		}
		i = end
	}
	return highest
}
