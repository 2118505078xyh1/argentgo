package agent

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// OrchestratorPrompt 是可移植、无依赖的委派策略。
// 由调用方决定把它放在对话中的什么位置；本包只负责文本内容。
//
//go:embed prompts/orchestrator.txt
var orchestratorText string

// OrchestratorPrompt 返回经过修剪的、基于波次的编排指令字符串，
// 使内嵌的尾部换行不会泄漏进系统提示。
func OrchestratorPrompt() string {
	return strings.TrimSpace(orchestratorText)
}

// 这些限制用于避免工作区意外地把无数量上限的角色文件加载进 agent 循环。
const (
	maxPersonaFiles = 256
	maxPersonaBytes = 64 * 1024
)

// Persona 是从 markdown 文件加载的一个具名子 agent 定义。
type Persona struct {
	// 当任务请求某个角色时，Name 用于标识该角色。
	Name string
	// System 是 markdown 主体，用作子 agent 的系统提示。
	System string
	// AllowedTools 是子 agent 的可选工具白名单。
	AllowedTools []string
}

// SubagentConfig 把一个角色适配为内置 `task` 工具的子配置。
// Name 没有对应的 SubagentConfig 字段：它是查找键，而非子级状态。
// 该适配器刻意很薄——预算、深度限制和事件桥接仍由委派调用方负责。
func (p Persona) SubagentConfig() SubagentConfig {
	return SubagentConfig{
		System:       p.System,
		AllowedTools: append([]string(nil), p.AllowedTools...),
	}
}

// LoadPersonas 从 dir（例如 `.argent/agents`）直接包含的 *.md 文件中读取子 agent 定义。
// 子目录会被忽略。
//
// 只有当文件的 frontmatter 将其标记为子 agent 时才算作角色
// （`subagent: true`，或 Kilo 风格的 `mode: subagent`）。其他 markdown 文件会被跳过。
// `name` 键是可选的：缺省时从文件名派生；`tools` 接受逗号分隔列表或 JSON 数组。
//
// 加载是尽力而为的：格式错误的文件会被跳过，而不是中止扫描。
// 第一个错误会与已收集到的角色一起返回，因此调用方既可以选择呈现它，
// 也可以为了宽松启动而忽略它。缺少完整 frontmatter 围栏的文件会被视为"不是角色"，
// 而不是报告为错误。
func LoadPersonas(dir string) (map[string]Persona, error) {
	out := make(map[string]Persona)
	if strings.TrimSpace(dir) == "" {
		return out, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return out, fmt.Errorf("read personas in %s: %w", dir, err)
	}

	var firstErr error
	record := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}

	count := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			continue
		}
		if count >= maxPersonaFiles {
			record(fmt.Errorf("persona file limit of %d exceeded in %s", maxPersonaFiles, dir))
			break
		}
		info, err := entry.Info()
		if err != nil {
			record(fmt.Errorf("persona %s: %w", entry.Name(), err))
			continue
		}
		if info.Size() > maxPersonaBytes {
			record(fmt.Errorf("persona %s: file exceeds %d bytes", entry.Name(), maxPersonaBytes))
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			record(fmt.Errorf("persona %s: %w", entry.Name(), err))
			continue
		}
		persona, ok, err := parsePersona(string(data), entry.Name())
		if err != nil {
			record(fmt.Errorf("persona %s: %w", entry.Name(), err))
			continue
		}
		if !ok {
			continue
		}
		if _, exists := out[persona.Name]; exists {
			record(fmt.Errorf("persona %s: duplicate name %q", entry.Name(), persona.Name))
			continue
		}
		out[persona.Name] = persona
		count++
	}
	return out, firstErr
}

// parsePersona 解析一个 markdown 文件。对于根本不是子 agent 定义的文件，ok 为 false；
// frontmatter 格式错误时 err 非 nil。
func parsePersona(text, filename string) (Persona, bool, error) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	front, body, ok := splitPersonaFrontmatter(text)
	if !ok {
		return Persona{}, false, nil
	}
	fields, err := parsePersonaFields(front)
	if err != nil {
		return Persona{}, false, err
	}

	subagent := false
	if raw := strings.TrimSpace(fields["subagent"]); raw != "" {
		value, err := strconv.ParseBool(strings.ToLower(raw))
		if err != nil {
			return Persona{}, false, fmt.Errorf("invalid subagent value %q", raw)
		}
		subagent = value
	}
	mode := strings.EqualFold(strings.TrimSpace(fields["mode"]), "subagent")
	if !subagent && !mode {
		return Persona{}, false, nil
	}

	name := strings.TrimSpace(fields["name"])
	if name == "" {
		name = strings.TrimSpace(strings.TrimSuffix(filename, filepath.Ext(filename)))
	}
	if name == "" {
		return Persona{}, false, fmt.Errorf("persona has no name")
	}

	tools, err := parsePersonaTools(fields["tools"])
	if err != nil {
		return Persona{}, false, err
	}
	return Persona{Name: name, System: strings.TrimSpace(body), AllowedTools: tools}, true, nil
}

// splitPersonaFrontmatter 把开头的 `---` 围栏与 markdown 主体分离。
// 围栏缺失或未闭合意味着该文件不是角色定义。
func splitPersonaFrontmatter(text string) (front, body string, ok bool) {
	if !strings.HasPrefix(text, "---\n") {
		return "", "", false
	}
	lines := strings.Split(text[len("---\n"):], "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "---" {
			return strings.Join(lines[:i], "\n"), strings.Join(lines[i+1:], "\n"), true
		}
	}
	return "", "", false
}

// parsePersonaFields 是一个小型的本地 frontmatter 读取器。
// 导入 internal/commands 会耦合独立的目录，因此这里只镜像该解析器的标量部分。
func parsePersonaFields(front string) (map[string]string, error) {
	fields := make(map[string]string)
	for i, line := range strings.Split(front, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		idx := strings.IndexByte(line, ':')
		if idx <= 0 {
			return nil, fmt.Errorf("invalid frontmatter on line %d: expected key: value", i+2)
		}
		key := strings.ToLower(strings.TrimSpace(line[:idx]))
		if !validPersonaKey(key) {
			return nil, fmt.Errorf("invalid frontmatter key %q on line %d", key, i+2)
		}
		fields[key] = unquotePersona(strings.TrimSpace(line[idx+1:]))
	}
	return fields, nil
}

func validPersonaKey(key string) bool {
	if key == "" {
		return false
	}
	for _, r := range key {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func unquotePersona(value string) string {
	if len(value) >= 2 {
		first, last := value[0], value[len(value)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			return value[1 : len(value)-1]
		}
	}
	return value
}

// parsePersonaTools 同时接受 `read, write` 和 `["read", "write"]` 两种写法。
func parsePersonaTools(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if strings.HasPrefix(raw, "[") {
		var items []string
		if err := json.Unmarshal([]byte(raw), &items); err != nil {
			return nil, fmt.Errorf("invalid tools array: %w", err)
		}
		return cleanToolNames(items), nil
	}
	return cleanToolNames(strings.Split(raw, ",")), nil
}

func cleanToolNames(items []string) []string {
	out := make([]string, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		name := strings.TrimSpace(strings.Trim(strings.TrimSpace(item), `"'`))
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
