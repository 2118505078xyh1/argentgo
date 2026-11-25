package commands

// 内置 /review：对脏 git 工作树的只读检查。它唯一
// 可发出的命令是 `git status --porcelain` 和
// `git diff --unified=3`，两者都通过注入的 runner 执行，以便调用方
// 强制只读边界，测试也无需真实仓库。

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"example.com/argentgo/internal/security"
)

// reviewLargeDiffLines 标记某个文件的 diff 新增了异常多的
// 行数。仅靠 git status 无法报告文件大小，因此这里用 diff 量作为
// 只读的替代指标。
const reviewLargeDiffLines = 200

// CommandRunner 运行单个只读进程并返回其 stdout。
// 实现应拒绝 Review 请求的这两个 git 读取命令之外的
// 任何命令。
type CommandRunner interface {
	Run(name string, args ...string) ([]byte, error)
}

// RunnerFunc 将函数适配为 CommandRunner。
type RunnerFunc func(name string, args ...string) ([]byte, error)

// Run 实现 CommandRunner。
func (f RunnerFunc) Run(name string, args ...string) ([]byte, error) { return f(name, args...) }

// Finding 是一条紧凑的 review 观察结果。Detail 始终经过脱敏。
type Finding struct {
	Kind   string // todo, debug, large-file, secret
	File   string
	Line   int
	Detail string
}

// ReviewReport 是只读 diff 审查的结果。
type ReviewReport struct {
	Clean    bool
	Files    []string
	Findings []Finding
}

// String 渲染紧凑、易读的发现报告。
func (r ReviewReport) String() string {
	if r.Clean {
		return "no changes"
	}
	var out strings.Builder
	fmt.Fprintf(&out, "%d changed file(s):\n", len(r.Files))
	for _, file := range r.Files {
		fmt.Fprintf(&out, "  %s\n", file)
	}
	if len(r.Findings) == 0 {
		out.WriteString("findings: none\n")
		return out.String()
	}
	fmt.Fprintf(&out, "findings (%d):\n", len(r.Findings))
	for _, finding := range r.Findings {
		location := finding.File
		if finding.Line > 0 {
			location += ":" + strconv.Itoa(finding.Line)
		}
		fmt.Fprintf(&out, "  [%s] %s %s\n", finding.Kind, location, finding.Detail)
	}
	return out.String()
}

// Review 用两条只读 git 命令检查工作树。它从不
// 写文件、暂存更改或调用其他任何程序。
func Review(runner CommandRunner) (ReviewReport, error) {
	if runner == nil {
		return ReviewReport{}, errors.New("commands: nil git command runner")
	}
	status, err := runner.Run("git", "status", "--porcelain")
	if err != nil {
		return ReviewReport{}, fmt.Errorf("commands: git status: %w", err)
	}
	files := changedFiles(string(status))
	if len(files) == 0 {
		return ReviewReport{Clean: true}, nil
	}
	diff, err := runner.Run("git", "diff", "--unified=3")
	if err != nil {
		return ReviewReport{}, fmt.Errorf("commands: git diff: %w", err)
	}
	return ReviewReport{Files: files, Findings: diffFindings(string(diff))}, nil
}

// init 把 /review 的构造器静态注册到内置命令注册表。
// 使用显式 order，确保输出顺序不依赖 init() 的执行先后。
func init() {
	builtinRegistry.MustRegister("review", builtinOrderReview, ReviewCommand)
}

// ReviewCommand 是内置斜杠命令描述符。它随包初始化注册到内置命令表，
// 调用方在 Discover 之后调用 NewSetWithBuiltins，并将
// `/review` 路由到 ReviewHook.Run。
func ReviewCommand() Command {
	return Command{
		Name:        "review",
		Description: "Read-only review of the dirty git tree without modifying it",
		Body:        "Review the working tree diff: report TODO/FIXME markers, debug prints, large diffs and secret-looking literals. Do not modify files.",
		Path:        "<builtin:review>",
	}
}

// ReviewHook 是 /review 斜杠命令的注册接缝。调用方
// 提供只读的 CommandRunner（通常由 internal/proc 支撑），并
// 用用户的参数调用 Run。
type ReviewHook struct {
	Runner CommandRunner
}

// Run 执行审查并返回可直接用于提示词的发现报告。
func (h ReviewHook) Run(_ string) (string, error) {
	report, err := Review(h.Runner)
	if err != nil {
		return "", err
	}
	return report.String(), nil
}

// Add 将内置命令注册到已发现的集合中。来自 markdown 文件的显式命令
// 优先；Add 从不覆盖已有名称。
func (s *Set) Add(cmds ...Command) {
	if s == nil {
		return
	}
	for _, cmd := range cmds {
		if cmd.Name == "" {
			continue
		}
		if _, exists := s.Get(cmd.Name); exists {
			continue
		}
		s.commands = append(s.commands, cmd)
	}
	sort.Slice(s.commands, func(i, j int) bool { return s.commands[i].Name < s.commands[j].Name })
}

func changedFiles(status string) []string {
	seen := make(map[string]bool)
	var files []string
	for _, line := range strings.Split(status, "\n") {
		if len(line) < 4 {
			continue
		}
		path := strings.TrimSpace(line[3:])
		if index := strings.Index(path, " -> "); index >= 0 {
			path = path[index+4:]
		}
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		files = append(files, path)
	}
	sort.Strings(files)
	return files
}

type addedLine struct {
	file string
	line int
	text string
}

var todoPattern = regexp.MustCompile(`\b(?:TODO|FIXME)\b`)

// quotedSecretPattern 覆盖 `apiKey := "value"` 这种
// security.Redact 的简单键值扫描可能遗漏的形态。本地处理先运行，
// 以便在通用脱敏器重写该行之前去掉带引号的值。
var quotedSecretPattern = regexp.MustCompile(`(?i)(api[_-]?key|secret|password|passwd|token|access[_-]?key|private[_-]?key)\s*(?::=|=|:)\s*["']([^"']{6,})["']`)

func redactFinding(text string) string {
	return security.Redact(quotedSecretPattern.ReplaceAllString(text, `${1}="[redacted:secret]"`))
}

var debugMarkers = []string{
	"fmt.Print(", "fmt.Println(", "console.log(", "console.debug(",
	"println(", "print(", "debugger;", "dbg!", "pprint(",
}

func diffFindings(diff string) []Finding {
	var added []addedLine
	counts := make(map[string]int)
	file, lineNo := "", 0
	for _, raw := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(raw, "+++ "):
			file = strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(raw, "+++ ")), "b/")
			if file == "/dev/null" {
				file = ""
			}
		case strings.HasPrefix(raw, "@@ "):
			lineNo = hunkStart(raw)
		case strings.HasPrefix(raw, " ") && !strings.HasPrefix(raw, "+++"):
			// 上下文行在新文件中也占据一行。
			lineNo++
		case strings.HasPrefix(raw, "+") && !strings.HasPrefix(raw, "+++"):
			if file == "" {
				continue
			}
			counts[file]++
			added = append(added, addedLine{file: file, line: lineNo, text: strings.TrimPrefix(raw, "+")})
			lineNo++
		}
	}

	findings := make([]Finding, 0, len(added))
	for _, item := range added {
		if redacted := redactFinding(item.text); redacted != item.text {
			findings = append(findings, Finding{
				Kind: "secret", File: item.file, Line: item.line, Detail: clipFinding(redacted, 120),
			})
		}
		if todoPattern.MatchString(item.text) {
			findings = append(findings, Finding{
				Kind: "todo", File: item.file, Line: item.line, Detail: clipFinding(item.text, 120),
			})
		}
		if marker := debugMarker(item.text); marker != "" {
			findings = append(findings, Finding{
				Kind: "debug", File: item.file, Line: item.line, Detail: marker + ": " + clipFinding(item.text, 120),
			})
		}
	}
	for file, count := range counts {
		if count > reviewLargeDiffLines {
			findings = append(findings, Finding{
				Kind: "large-file", File: file,
				Detail: fmt.Sprintf("diff adds %d lines", count),
			})
		}
	}
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].File != findings[j].File {
			return findings[i].File < findings[j].File
		}
		if findings[i].Line != findings[j].Line {
			return findings[i].Line < findings[j].Line
		}
		return findings[i].Kind < findings[j].Kind
	})
	return findings
}

func hunkStart(header string) int {
	index := strings.Index(header, " +")
	if index < 0 {
		return 0
	}
	part := strings.TrimSpace(header[index+2:])
	if end := strings.IndexAny(part, " ,"); end >= 0 {
		part = part[:end]
	}
	value, _ := strconv.Atoi(part)
	return value
}

func debugMarker(text string) string {
	for _, marker := range debugMarkers {
		if strings.Contains(text, marker) {
			return marker
		}
	}
	return ""
}

func clipFinding(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	if limit <= 0 || len(text) <= limit {
		return text
	}
	// 追加省略号前先退回到 rune 边界，否则
	// 在多字节 rune 中间截断会留下无效的 UTF-8。
	cut := limit - 1
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…"
}
