// Package commands 发现 markdown 斜杠命令并展开其正文。
//
// 命令是 `.md` 文件，位于 `.argent/commands`（项目级）或
// `.argentgo/commands`（用户级）。可选的 frontmatter 携带 `description`、
// `argument-hint`、`allowed-tools`、`model` 和 `aliases`；正文即
// 提示词模板。
//
// 替换规则与 Rust 实现一致：
//
//   - `$ARGUMENTS` / `$@` —— 命令名之后的全部内容；
//   - `$1` … `$99` —— 一个以空白分隔的参数；
//   - `${1}` —— 同上，当相邻文本会吞掉数字时使用。
//
// 带引号的参数（"two words" 或 'two words'）算作一个位置。当
// ExpandOptions.RemainingAtHighest 启用时，正文中使用的最高编号位置
// 吸收其余所有参数，符合 markdown 命令的惯例。安全的默认行为保留
// 原有的一参数一位置的语义。
// behaviour.
//
// “ !`command` “ 片段会执行 shell 命令，但仅当调用方通过
// ExpandOptions.AllowShell 显式选择启用；默认保持该片段不变。
package commands

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// ProjectSubdir 是仓库存放斜杠命令的位置。
const ProjectSubdir = ".argent/commands"

// HomeSubdir 是用户存放跨项目共享命令的位置。
const HomeSubdir = ".argentgo/commands"

// Command 表示磁盘上发现的一条命令。
type Command struct {
	// Name 是斜杠命令名，包含可能的命名空间前缀。
	Name string
	// Aliases 是 CompleteCommands 接受的备用名称，来自
	// frontmatter 的 `aliases` 字段（逗号分隔）；空切片
	// 表示该命令没有别名。
	Aliases []string
	// Description 是 `/help` 显示的一行摘要。
	Description string
	// ArgumentHint 是用量提示（frontmatter `argument-hint`）。
	ArgumentHint string
	// AllowedTools 缩小该提示词允许使用的工具列表。
	AllowedTools []string
	// Model 可选地为该提示词固定使用的模型。
	Model string
	// Agent 可选地为该提示词固定使用的 agent/persona（frontmatter
	// `agent`）。它暴露给调用方应用；本包从不
	// 构建 agent。
	Agent string
	// Body 是模板正文，不含 frontmatter。
	Body string
	// Path 是该命令的来源文件。
	Path string
	// Plugin 是提供该命令的插件，对于项目和
	// 用户命令为空。它同时也是名称的命名空间前缀。
	Plugin string
	// Warnings 报告在声明性内容中发现的不可见字符。
	// 它仅是元数据：解析过程永不因它改变，也永不阻塞。
	Warnings []string
}

// Source 表示一个待扫描目录及其命令获得的命名空间。
//
// 对项目和用户目录，Prefix 为空。插件来源将其设为
// 插件名，因此其 `review` 命令可通过 `plugin:review` 访问，且
// 不会与用户命令冲突。
type Source struct {
	Root   string
	Prefix string
}

// DefaultSources 返回某个会话的命令目录，优先级从高到低：
// 先是项目级，再是用户级。
func DefaultSources(cwd, home string) []Source {
	var sources []Source
	if cwd != "" {
		sources = append(sources, Source{Root: filepath.Join(cwd, filepath.FromSlash(ProjectSubdir))})
	}
	if home != "" {
		sources = append(sources, Source{Root: filepath.Join(home, filepath.FromSlash(HomeSubdir))})
	}
	return sources
}

// Set 是某个会话解析后的命令列表。
type Set struct {
	commands []Command
}

// Discover 加载 sources 下的所有命令。
//
// 按序搜索各来源，同名命令以第一个为准，
// 因此项目命令会遮蔽同名的用户命令。
func Discover(sources []Source) *Set {
	set := &Set{}
	seen := make(map[string]bool)
	for _, source := range sources {
		for _, file := range commandFiles(source.Root) {
			text, err := os.ReadFile(file)
			if err != nil {
				continue
			}
			cmd, err := ParseStrict(string(text), file)
			if err != nil {
				// 格式错误的命令文件无法参与发现；
				// 需要的调用方可从 ParseStrict 获得该错误报告。
				continue
			}
			if prefix := strings.TrimSpace(source.Prefix); prefix != "" {
				cmd.Plugin = prefix
				cmd.Name = prefix + ":" + cmd.Name
			}
			if cmd.Name == "" || seen[cmd.Name] {
				continue
			}
			seen[cmd.Name] = true
			set.commands = append(set.commands, cmd)
		}
	}
	sort.Slice(set.commands, func(i, j int) bool { return set.commands[i].Name < set.commands[j].Name })
	return set
}

func commandFiles(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			files = append(files, filepath.Join(root, entry.Name()))
		}
	}
	sort.Strings(files)
	return files
}

// Parse 读取单个命令文件。没有 frontmatter 的文件同样可用：其
// 文件名主干即名称，第一个非空行即描述。格式错误的
// frontmatter 会被宽松跳过；可用 ParseStrict 报告文件和行号。
func Parse(text, path string) Command {
	cmd, err := ParseStrict(text, path)
	if err == nil {
		return cmd
	}
	text = strings.TrimPrefix(text, "\ufeff")
	body := text
	cmd = Command{
		Name:     commandNameFromPath(path),
		Body:     strings.TrimSpace(body),
		Path:     path,
		Warnings: security.ScanArtifact(text).Warnings(),
	}
	for _, line := range strings.Split(cmd.Body, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			cmd.Description = trimmed
			break
		}
	}
	return cmd
}

// ParseStrict 读取单个命令文件，并报告无效的 frontmatter，
// 附带文件名和出错行的 1 基行号。
func ParseStrict(text, path string) (Command, error) {
	text = strings.TrimPrefix(text, "\ufeff")
	frontmatter, body, ok := splitFrontmatter(text)
	if !ok {
		body = text
		cmd := Command{
			Name:     commandNameFromPath(path),
			Body:     strings.TrimSpace(body),
			Path:     path,
			Warnings: security.ScanArtifact(text).Warnings(),
		}
		for _, line := range strings.Split(cmd.Body, "\n") {
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				cmd.Description = trimmed
				break
			}
		}
		return cmd, nil
	}
	fields, err := parseFieldsStrict(frontmatter)
	if err != nil {
		return Command{}, fmt.Errorf("%s:%d: %w", path, err.line, err)
	}
	name := fields.get("name")
	if name == "" {
		name = commandNameFromPath(path)
	}
	return Command{
		Name:         name,
		Aliases:      fields.list("aliases"),
		Description:  fields.get("description"),
		ArgumentHint: fields.get("argument-hint"),
		AllowedTools: fields.list("allowed-tools"),
		Model:        fields.get("model"),
		Agent:        fields.get("agent"),
		Body:         strings.TrimSpace(body),
		Path:         path,
		Warnings:     security.ScanArtifact(text).Warnings(),
	}, nil
}

func commandNameFromPath(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// List 返回所有命令，按名称排序。
func (s *Set) List() []Command {
	if s == nil {
		return nil
	}
	out := make([]Command, len(s.commands))
	copy(out, s.commands)
	return out
}

// Len 返回命令数量。
func (s *Set) Len() int {
	if s == nil {
		return 0
	}
	return len(s.commands)
}

// Get 按名称查找单条命令。
func (s *Set) Get(name string) (Command, bool) {
	if s == nil {
		return Command{}, false
	}
	name = strings.TrimSpace(name)
	for _, cmd := range s.commands {
		if cmd.Name == name {
			return cmd, true
		}
	}
	return Command{}, false
}

// Expand 用用户键入的参数渲染命令正文，使用
// 安全默认设置（不进行 shell 插值）。
func (c Command) Expand(args string) (string, error) {
	return c.ExpandWith(args, ExpandOptions{})
}

// ExpandWith 渲染命令正文，并遵守给定选项。
func (c Command) ExpandWith(args string, opts ExpandOptions) (string, error) {
	body := c.Body
	if opts.AllowShell {
		var err error
		body, err = expandShell(body, opts)
		if err != nil {
			return "", err
		}
	}
	return substituteArgs(body, args, opts.RemainingAtHighest), nil
}

// Expand 是 Set.Get + Command.Expand 的便捷封装。
func (s *Set) Expand(name, args string) (string, error) {
	cmd, ok := s.Get(name)
	if !ok {
		return "", fmt.Errorf("unknown command: %s", name)
	}
	return cmd.Expand(args)
}

// ExpandOptions 控制正文展开的可选部分。
type ExpandOptions struct {
	// AllowShell 启用 `` !`command` `` 插值。默认关闭，
	// 因为 markdown 文件可能来自用户尚未阅读过的插件。
	AllowShell bool
	// Dir 是被插值命令的工作目录。
	Dir string
	// RemainingAtHighest 使正文中引用的最高 $N 吸收
	// 其余所有参数。默认关闭，以便使用 $1 和 $2 作为独立
	// 位置的既有模板保持原义。
	RemainingAtHighest bool
	// Run 覆盖被插值命令的执行方式（测试、沙箱）。
	// 它接收命令文本并返回其输出。
	Run func(command string) (string, error)
}

// substituteArgs 展开 $ARGUMENTS、$@ 和 $N/${N}。位置按
// 引号分组解析，因此 "two words" 仍是一个参数。
func substituteArgs(body, args string, remainingAtHighest bool) string {
	fields := splitArguments(args)
	highest := 0
	for i := 0; i < len(body); {
		end, index, _, ok := dollarToken(body, i)
		if !ok {
			i++
			continue
		}
		if index > highest {
			highest = index
		}
		i = end
	}

	var out strings.Builder
	out.Grow(len(body) + len(args))
	for i := 0; i < len(body); {
		if body[i] != '$' {
			out.WriteByte(body[i])
			i++
			continue
		}
		end, index, named, ok := dollarToken(body, i)
		if !ok {
			out.WriteByte('$')
			i++
			continue
		}
		if named {
			out.WriteString(args)
			i = end
			continue
		}
		if index > 0 && index <= len(fields) {
			if remainingAtHighest && index == highest {
				out.WriteString(strings.Join(fields[index-1:], " "))
			} else {
				out.WriteString(fields[index-1])
			}
		}
		i = end
	}
	return out.String()
}

// dollarToken 解析 i 处一个以 $ 开头的标记。
func dollarToken(body string, i int) (end, index int, named, ok bool) {
	if i+1 >= len(body) {
		return i + 1, 0, false, false
	}
	switch body[i+1] {
	case '{':
		digits, value := readDigits(body, i+2)
		if digits > 0 && i+2+digits < len(body) && body[i+2+digits] == '}' {
			return i + 3 + digits, value, false, true
		}
	case '@':
		return i + 2, 0, true, true
	case 'A':
		if strings.HasPrefix(body[i:], "$ARGUMENTS") {
			return i + len("$ARGUMENTS"), 0, true, true
		}
	default:
		if body[i+1] >= '0' && body[i+1] <= '9' {
			digits, value := readDigits(body, i+1)
			return i + 1 + digits, value, false, true
		}
	}
	return i + 1, 0, false, false
}

func readDigits(body string, at int) (count, value int) {
	for at+count < len(body) && body[at+count] >= '0' && body[at+count] <= '9' && count < 8 {
		value = value*10 + int(body[at+count]-'0')
		count++
	}
	return count, value
}

// splitArguments 按未加引号的空白拆分命令行。引号只
// 用于分组参数；它们永远不会作为 shell 片段执行。
func splitArguments(args string) []string {
	var out []string
	var current strings.Builder
	var quote rune
	escaped := false
	for _, r := range args {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && (quote == 0 || quote == '"') {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				current.WriteRune(r)
			}
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
		case ' ', '\t', '\n', '\r':
			if current.Len() > 0 {
				out = append(out, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		out = append(out, current.String())
	}
	return out
}

func expandShell(body string, opts ExpandOptions) (string, error) {
	var out strings.Builder
	rest := body
	for {
		start := strings.Index(rest, "!`")
		if start < 0 {
			out.WriteString(rest)
			return out.String(), nil
		}
		end := strings.Index(rest[start+2:], "`")
		if end < 0 {
			out.WriteString(rest)
			return out.String(), nil
		}
		out.WriteString(rest[:start])
		command := rest[start+2 : start+2+end]
		text, err := runShell(command, opts)
		if err != nil {
			return "", fmt.Errorf("command interpolation %q: %w", command, err)
		}
		out.WriteString(strings.TrimSpace(text))
		rest = rest[start+2+end+1:]
	}
}

func runShell(command string, opts ExpandOptions) (string, error) {
	if opts.Run != nil {
		return opts.Run(command)
	}
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/C", command)
	} else {
		cmd = exec.Command("sh", "-c", command)
	}
	cmd.Dir = opts.Dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), err
	}
	return string(output), nil
}

// fields 是本包所需的最小 frontmatter 读取器。它是
// skills 解析器标量部分的副本：导入该包会耦合两个
// 本应独立的目录。
type fields map[string]string

func (f fields) get(key string) string { return f[strings.ToLower(key)] }

func (f fields) list(key string) []string {
	raw := f.get(key)
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if item := unquote(strings.TrimSpace(part)); item != "" {
			out = append(out, item)
		}
	}
	return out
}

type frontmatterError struct {
	line int
	msg  string
}

func (e *frontmatterError) Error() string { return e.msg }

// parseFieldsStrict 在读取值之前校验 frontmatter 语法，以便
// 格式错误的行能附带其行号被报告。
func parseFieldsStrict(frontmatter string) (fields, *frontmatterError) {
	lines := strings.Split(strings.ReplaceAll(frontmatter, "\r\n", "\n"), "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || indentOf(line) > 0 {
			continue
		}
		idx := strings.Index(line, ":")
		if idx <= 0 {
			return nil, &frontmatterError{line: i + 2, msg: "invalid frontmatter: expected key: value"}
		}
		key := strings.ToLower(strings.TrimSpace(line[:idx]))
		if !validKey(key) {
			return nil, &frontmatterError{line: i + 2, msg: fmt.Sprintf("invalid frontmatter key %q", key)}
		}
	}
	return parseFields(frontmatter), nil
}

func parseFields(frontmatter string) fields {
	out := fields{}
	lines := strings.Split(strings.ReplaceAll(frontmatter, "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || indentOf(line) > 0 {
			continue
		}
		idx := strings.Index(line, ":")
		if idx <= 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(line[:idx]))
		if !validKey(key) {
			continue
		}
		value := strings.TrimSpace(line[idx+1:])
		if value == "" || strings.HasPrefix(value, ">") || strings.HasPrefix(value, "|") {
			var block []string
			j := i + 1
			for ; j < len(lines); j++ {
				next := lines[j]
				if strings.TrimSpace(next) == "" {
					block = append(block, "")
					continue
				}
				if indentOf(next) == 0 {
					break
				}
				block = append(block, strings.TrimSpace(next))
			}
			i = j - 1
			out[key] = strings.TrimSpace(strings.Join(block, " "))
			continue
		}
		out[key] = unquote(value)
	}
	return out
}

func validKey(key string) bool {
	for _, r := range key {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.') {
			return false
		}
	}
	return key != ""
}

func indentOf(line string) int {
	n := 0
	for _, r := range line {
		if r != ' ' && r != '\t' {
			break
		}
		n++
	}
	return n
}

func unquote(value string) string {
	if len(value) < 2 {
		return value
	}
	first, last := value[0], value[len(value)-1]
	if first == '"' && last == '"' {
		inner := value[1 : len(value)-1]
		inner = strings.ReplaceAll(inner, `\"`, `"`)
		inner = strings.ReplaceAll(inner, `\n`, "\n")
		return inner
	}
	if first == '\'' && last == '\'' {
		return strings.ReplaceAll(value[1:len(value)-1], "''", "'")
	}
	return value
}

// splitFrontmatter 是本包内的分隔符切分器副本。
func splitFrontmatter(text string) (frontmatter, body string, ok bool) {
	// 先规范化 CRLF，与 skills.SplitFrontmatter 一致：在 Windows 上，
	// 以 CRLF 保存的文件其起始分隔符为 "---\r\n"，若不这样做，
	// 下面的前缀检查会失败，整个 frontmatter 会泄漏到
	// 正文中。仅含 LF 的文本不受影响。
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return "", "", false
	}
	rest := text[len("---\n"):]
	offset := 0
	for {
		end := strings.IndexByte(rest[offset:], '\n')
		lineEnd := len(rest)
		var line string
		if end < 0 {
			line = rest[offset:]
		} else {
			line = rest[offset : offset+end]
			lineEnd = offset + end
		}
		if strings.TrimSpace(line) == "---" {
			bodyStart := lineEnd
			if end >= 0 {
				bodyStart++
			}
			if bodyStart > len(rest) {
				bodyStart = len(rest)
			}
			return rest[:offset], rest[bodyStart:], true
		}
		if end < 0 {
			break
		}
		offset = lineEnd + 1
	}
	return "", "", false
}
