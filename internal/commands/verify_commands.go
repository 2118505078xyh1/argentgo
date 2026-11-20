package commands

// 带语言前缀的 verify 命令。
//
// 验证命令可以按 aider 的 --lint-cmd 约定以扁平字符串列表形式到来。
// 每一项要么是裸命令，要么是前面带有语言名和冒号的
// 命令：
//
//	"go: go vet ./..."
//	"python: flake8 --select=E"
//
// ParseVerifyCommands 将该列表转换为 语言 -> argv 的映射。它是纯函数：
// 不读取任何内容、不启动任何进程，也从不修改输入。拆分
// 刻意保守，因此属于命令的冒号（http://、
// C:\tools）绝不会被误认为语言分隔符。
//
// Argv 切片来自 strings.Fields，与 config.VerifyCommands 的
// 解析方式一致，因此这些值可直接交给 app 的验证白名单。
// 因此这里不解释引号和 shell 元字符。

import (
	"fmt"
	"strings"
	"unicode"
)

// VerifyAllLanguages 是无语言前缀命令的映射键。
// Aider 会将这类命令应用于所有语言；当没有匹配的语言专属条目时，
// 调用方应使用该键作为默认值。
const VerifyAllLanguages = "*"

// IssueKind 对一种 verify 命令解析问题进行分类。
type IssueKind string

const (
	IssueEmptyCommand      IssueKind = "empty-command"
	IssueEmptyLanguage     IssueKind = "empty-language"
	IssueDuplicateLanguage IssueKind = "duplicate-language"
)

// Issue 是解析 verify 命令参数时发现的一个问题。Index 是
// Arg 在输入切片中从 0 开始的位置；Message 是稳定的、
// 易读的说明，适合 doctor 输出或 UI 警告。
type Issue struct {
	Kind    IssueKind
	Index   int
	Arg     string
	Message string
}

// ParseVerifyCommands 将扁平的 verify 命令参数列表解析为
// 语言 -> argv 的映射。每个参数按如下方式解释：
//
//   - "lang: command" —— 针对某一种语言（或文件扩展名）的命令。
//     语言会被裁剪、转小写、去掉前导点，并通过下面的别名表
//     规范化，因此 "go"、"golang" 和
//     ".go" 都共用键 "go"。
//   - "command" —— 无语言前缀；存于 VerifyAllLanguages 下。
//
// 分隔符是第一个后面跟空白（或位于参数末尾）且左侧为
// 单个类语言标记的冒号。而后面跟
// "/"、"\" 或其他非空白字符的冒号仍属于命令的
// 一部分，因此 "http://example" 和 "C:\tools\vet.exe" 保持完整。
//
// 无效条目被跳过（不产生映射项），并在返回的 issues 中
// 描述。当两个条目规范化为同一语言时，以后者
// 为准，并报告 IssueDuplicateLanguage。返回的映射
// 始终非 nil；参数切片从不被修改。
func ParseVerifyCommands(args []string) (map[string][]string, []Issue) {
	out := make(map[string][]string, len(args))
	var issues []Issue

	for i, arg := range args {
		lang, cmdText, emptyLanguage := splitVerifyArg(arg)
		if emptyLanguage {
			issues = append(issues, Issue{
				Kind: IssueEmptyLanguage, Index: i, Arg: arg,
				Message: "verify command has a language separator but no language name",
			})
			continue
		}

		argv := strings.Fields(cmdText)
		if len(argv) == 0 {
			message := "verify command is empty"
			if lang != VerifyAllLanguages {
				message = fmt.Sprintf("verify command for language %q is empty", lang)
			}
			issues = append(issues, Issue{
				Kind: IssueEmptyCommand, Index: i, Arg: arg, Message: message,
			})
			continue
		}

		if _, exists := out[lang]; exists {
			issues = append(issues, Issue{
				Kind: IssueDuplicateLanguage, Index: i, Arg: arg,
				Message: fmt.Sprintf("verify command for language %q is defined more than once; keeping the last", lang),
			})
		}
		out[lang] = argv
	}
	return out, issues
}

// splitVerifyArg 将可选的语言前缀与命令文本分离。
// emptyLanguage 仅对前导 ":"（一种格式错误的前缀形式）为 true，
// 它绝不能被静默重新解释为裸命令。
func splitVerifyArg(arg string) (lang, cmd string, emptyLanguage bool) {
	trimmed := strings.TrimSpace(arg)
	if trimmed == "" {
		return VerifyAllLanguages, "", false
	}
	if trimmed[0] == ':' {
		return "", "", true
	}
	if i := strings.IndexByte(trimmed, ':'); i >= 0 &&
		(i+1 == len(trimmed) || isVerifySpace(trimmed[i+1])) {
		prefix := strings.TrimSpace(trimmed[:i])
		if prefix != "" && validVerifyLangPrefix(prefix) {
			return canonicalVerifyLang(prefix), strings.TrimSpace(trimmed[i+1:]), false
		}
	}
	return VerifyAllLanguages, trimmed, false
}

func isVerifySpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\r' || b == '\n' || b == '\v' || b == '\f'
}

// validVerifyLangPrefix 接受单个类语言标记：字母、数字
// 以及真实语言名使用的标点（".go"、"c++"、"c#"、
// "objective-c"、"node.js"）。空格和引号会使前缀不合格，
// 从而使整个参数变成裸命令。
func validVerifyLangPrefix(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
		case r == '.', r == '_', r == '-', r == '+', r == '#':
		default:
			return false
		}
	}
	return true
}

// verifyLangAliases 镜像 app.verifyLangForms 使用的别名组，使
// 从 CLI 解析出的键与配置映射键采用相同的规范化方式。这是
// 本地副本，因为导入 app 会反转包依赖关系。
var verifyLangAliases = map[string]string{
	"golang": "go",
	"py":     "python",
	"js":     "javascript",
	"jsx":    "javascript",
	"mjs":    "javascript",
	"cjs":    "javascript",
	"ts":     "typescript",
	"tsx":    "typescript",
	"rs":     "rust",
	"sh":     "shell",
	"bash":   "shell",
	"rb":     "ruby",
	"c++":    "cpp",
	"cc":     "cpp",
	"cxx":    "cpp",
	"cs":     "csharp",
	"c#":     "csharp",
	"kt":     "kotlin",
	"md":     "markdown",
	"yml":    "yaml",
}

func canonicalVerifyLang(s string) string {
	key := strings.ToLower(strings.TrimSpace(s))
	key = strings.TrimPrefix(key, ".")
	if key == "" {
		return ""
	}
	if canonical, ok := verifyLangAliases[key]; ok {
		return canonical
	}
	return key
}
