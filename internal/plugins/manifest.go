// Package plugins 发现、验证并安装 ZCode 兼容插件
// bundle，不运行任何插件代码。
//
// bundle 是带有清单的目录以及 MCP 服务器声明的任意组合，
// 本身，所以跳过它而非当作目录/文件。
// Package plugins 发现、验证并安装 ZCode 兼容插件 bundle，不运行任何插件代码。bundle 是带有清单的目录以及 MCP 服务器声明、技能、命令和钩子文档的任意组合。清单名称按此顺序识别，这也使 Claude Code 和 Codex bundle 能按原样加载：
// 可按原样加载：
//
//	.zcode-plugin/plugin.json
//	.claude-plugin/plugin.json
//	.codex-plugin/plugin.json
//	plugin.json
//
// 发现产生 Wiring，调用方把它交给自己的 MCP、技能和
// 钩子加载器；本包从不启动服务器或执行钩子。
package plugins

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// 发现产生 Wiring，调用方把它交给自己的 MCP、技能和钩子加载器；本包从不启动服务器或执行钩子。ManifestPaths 按优先级列出 bundle 内候选清单位置。
// 优先级顺序。
var ManifestPaths = []string{
	".zcode-plugin/plugin.json",
	".claude-plugin/plugin.json",
	".codex-plugin/plugin.json",
	"plugin.json",
}

// 哨兵错误。
var (
	// ErrNoManifest 报告没有可识别清单的目录。
	ErrNoManifest = errors.New("plugins: no plugin manifest found")
	// ErrInvalidName 报告不安全的插件名。
	ErrInvalidName = errors.New("plugins: invalid plugin name")
	// 哨兵错误。ErrNoManifest 报告没有可识别清单的目录。ErrInvalidName 报告不安全的插件名。ErrInvalidManifest 报告不是有效 JSON 或不是对象的清单。
	ErrInvalidManifest = errors.New("plugins: invalid plugin manifest")
)

// MCPServer 是 bundle 声明的一个 MCP 服务器。Hooks 是 bundle 的钩子贡献。清单可以命名钩子文档、内联钩子文档，或两者兼有。Paths 是清单声明的钩子文档路径（相对 bundle）。Inline 是嵌入清单的钩子文档（若有）。
type MCPServer struct {
	Command string
	Args    []string
	Env     map[string]string
	URL     string
}

// Hooks 是 bundle 的钩子贡献。清单可以命名钩子
// 文档、内联钩子文档，或两者兼有。
type Hooks struct {
	// Paths 是清单声明的钩子文档路径，相对 bundle。
	Paths []string
	// Inline 是嵌入清单的钩子文档（若有）。
	Inline json.RawMessage
}

// Manifest 是解析后的、以 bundle 为根的插件清单。
type Manifest struct {
	Name        string
	Version     string
	Description string
	Author      string
	Homepage    string
	MCPServers  map[string]MCPServer
	Skills      []string
	Commands    []string
	Hooks       Hooks
	Tools       []string

	// Root 是 bundle 目录，Path 是清单文件。
	Root string
	Path string
}

// Manifest 是解析后的、以 bundle 为根的插件清单。Root 是 bundle 目录，Path 是清单文件。Problem 是一条非致命验证发现。Resolve 和 Discovery 报告问题而不拒绝整组 bundle。String 渲染问题供日志使用。
// 问题而不拒绝整组 bundle。
type Problem struct {
	Plugin  string `json:"plugin,omitempty"`
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
}

// String 渲染问题供日志使用。
func (p Problem) String() string {
	switch {
	case p.Plugin != "" && p.Path != "":
		return fmt.Sprintf("plugin %q (%s): %s", p.Plugin, p.Path, p.Message)
	case p.Plugin != "":
		return fmt.Sprintf("plugin %q: %s", p.Plugin, p.Message)
	case p.Path != "":
		return fmt.Sprintf("%s: %s", p.Path, p.Message)
	default:
		return p.Message
	}
}

// FindManifest 返回 dir 内的清单路径，不深入子目录。
func FindManifest(dir string) (string, bool) {
	for _, relative := range ManifestPaths {
		candidate := filepath.Join(dir, filepath.FromSlash(relative))
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate, true
		}
	}
	return "", false
}

// LoadManifest 读取 dir 内的清单。
func LoadManifest(dir string) (Manifest, error) {
	manifestPath, ok := FindManifest(dir)
	if !ok {
		return Manifest{}, fmt.Errorf("%s: %w", dir, ErrNoManifest)
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return Manifest{}, fmt.Errorf("plugins: read %s: %w", manifestPath, err)
	}
	return ParseManifest(data, dir, manifestPath)
}

// FindManifest 返回 dir 内的清单路径，不深入子目录。LoadManifest 读取 dir 内的清单。ParseManifest 解析以 dir 为根的 bundle 的清单字节。manifestPath 记录在结果上且仅用于错误文本。
// 记录在结果上且仅用于错误文本。
func ParseManifest(data []byte, dir, manifestPath string) (Manifest, error) {
	if manifestPath == "" {
		manifestPath = filepath.Join(dir, "plugin.json")
	}
	var raw rawManifest
	if err := json.Unmarshal(data, &raw); err != nil {
		return Manifest{}, fmt.Errorf("%s: %w: %v", manifestPath, ErrInvalidManifest, err)
	}

	name := strings.TrimSpace(raw.Name)
	if name == "" {
		name = filepath.Base(filepath.Clean(dir))
	}
	if err := ValidateName(name); err != nil {
		return Manifest{}, fmt.Errorf("%s: %w", manifestPath, err)
	}

	version := strings.TrimSpace(raw.Version)
	if version == "" {
		version = "0.0.0"
	}

	servers := make(map[string]MCPServer, len(raw.MCPServers))
	for serverName, spec := range raw.MCPServers {
		servers[serverName] = MCPServer{
			Command: spec.Command,
			Args:    append([]string(nil), spec.Args...),
			Env:     spec.Env,
			URL:     spec.URL,
		}
	}
	if len(servers) == 0 {
		servers = nil
	}

	hooks, err := parseHooks(raw.Hooks)
	if err != nil {
		return Manifest{}, fmt.Errorf("%s: %w", manifestPath, err)
	}

	return Manifest{
		Name:        name,
		Version:     version,
		Description: raw.Description,
		Author:      parseAuthor(raw.Author),
		Homepage:    raw.Homepage,
		MCPServers:  servers,
		Skills:      parseStringList(raw.Skills),
		Commands:    parseStringList(raw.Commands),
		Hooks:       hooks,
		Tools:       parseStringList(raw.Tools),
		Root:        filepath.Clean(dir),
		Path:        manifestPath,
	}, nil
}

// rawManifest 镜像清单 schema。多态字段保持原始，使
// 解析器接受野外见到的每种布局。
type rawManifest struct {
	Name        string                  `json:"name"`
	Version     string                  `json:"version"`
	Description string                  `json:"description"`
	Author      json.RawMessage         `json:"author"`
	Homepage    string                  `json:"homepage"`
	MCPServers  map[string]rawMCPServer `json:"mcpServers"`
	Skills      json.RawMessage         `json:"skills"`
	Commands    json.RawMessage         `json:"commands"`
	Hooks       json.RawMessage         `json:"hooks"`
	Tools       json.RawMessage         `json:"tools"`
}

type rawMCPServer struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	URL     string            `json:"url"`
}

// rawManifest 镜像清单 schema。多态字段保持原始，使解析器接受野外见到的每种布局。parseAuthor 接受 "someone" 或 {"name": "someone"}。parseStringList 接受单个字符串、字符串数组或无。parseHooks 接受路径、路径数组、内联文档，或带有 "paths" 键和内联事件处理器的对象。
func parseAuthor(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strings.TrimSpace(text)
	}
	var object struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &object); err == nil {
		return strings.TrimSpace(object.Name)
	}
	return ""
}

// parseStringList 接受单个字符串、字符串数组或无。
func parseStringList(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		single = strings.TrimSpace(single)
		if single == "" {
			return nil
		}
		return []string{single}
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return nil
	}
	out := make([]string, 0, len(many))
	for _, item := range many {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// parseHooks 接受路径、路径数组、内联文档，或
// 带有 "paths" 键和内联事件处理器的对象。
func parseHooks(raw json.RawMessage) (Hooks, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return Hooks{}, nil
	}
	if paths := parseStringList(raw); len(paths) > 0 {
		return Hooks{Paths: paths}, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return Hooks{}, fmt.Errorf("%w: hooks must be a path, a list of paths or an object", ErrInvalidManifest)
	}
	var hooks Hooks
	if pathsRaw, ok := object["paths"]; ok {
		hooks.Paths = parseStringList(pathsRaw)
		delete(object, "paths")
	}
	if len(object) > 0 {
		inline, err := json.Marshal(object)
		if err != nil {
			return Hooks{}, fmt.Errorf("%w: encode inline hooks: %v", ErrInvalidManifest, err)
		}
		hooks.Inline = inline
	}
	return hooks, nil
}

// ValidateName 报告 name 是否可安全用作单一目录名。插件名在安装和卸载时成为目录名，因此只接受字母、数字和 '-'、'_'、'.', '@'；拒绝遍历、绝对路径、控制字符和 Windows 设备名。
//
// 插件名在安装和卸载时成为目录名，因此只接受
// 字母、数字和 '-'、'_'、'.', '@'；拒绝遍历、绝对
// 路径、控制字符和 Windows 设备名。
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: name is empty", ErrInvalidName)
	}
	if name == "." || name == ".." {
		return fmt.Errorf("%w: %q is a directory", ErrInvalidName, name)
	}
	for _, r := range name {
		if unicode.IsControl(r) || r == ':' || r == '/' || r == '\\' || r == filepath.Separator {
			return fmt.Errorf("%w: %q contains a path separator, colon or control character", ErrInvalidName, name)
		}
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("-_.@", r) {
			return fmt.Errorf("%w: %q may only contain letters, digits and '-', '_', '.', '@'", ErrInvalidName, name)
		}
	}
	stem := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	reserved := stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL"
	if !reserved && len(stem) == 4 {
		reserved = (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '1' && stem[3] <= '9'
	}
	if reserved {
		return fmt.Errorf("%w: %q is a reserved Windows device name", ErrInvalidName, name)
	}
	return nil
}

// ValidateVersion 报告 v 是否为语义化版本：三个数字
// ValidateVersion 报告 v 是否为语义化版本：三个数字组成部分，带可选的 -prerelease 和 +build 后缀。ResolvePath 把清单相对路径转换为绝对路径。Validate 报告解析后清单的非致命问题：版本格式、引用的文件不存在，以及无法运行的 MCP 声明。
func ValidateVersion(v string) error {
	core := strings.TrimSpace(v)
	if core == "" {
		return errors.New("version is empty")
	}
	if index := strings.IndexByte(core, '+'); index >= 0 {
		if err := validateIdentifiers(core[index+1:]); err != nil {
			return fmt.Errorf("invalid build metadata %q: %v", core[index+1:], err)
		}
		core = core[:index]
	}
	if index := strings.IndexByte(core, '-'); index >= 0 {
		if err := validateIdentifiers(core[index+1:]); err != nil {
			return fmt.Errorf("invalid pre-release %q: %v", core[index+1:], err)
		}
		core = core[:index]
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return fmt.Errorf("version %q is not MAJOR.MINOR.PATCH", v)
	}
	for _, part := range parts {
		if part == "" {
			return fmt.Errorf("version %q has an empty component", v)
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return fmt.Errorf("version %q has a non-numeric component", v)
			}
		}
	}
	return nil
}

func validateIdentifiers(s string) error {
	if s == "" {
		return errors.New("empty")
	}
	for _, part := range strings.Split(s, ".") {
		if part == "" {
			return errors.New("empty component")
		}
		for _, r := range part {
			ok := r == '-' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
			if !ok {
				return fmt.Errorf("illegal character %q", r)
			}
		}
	}
	return nil
}

// ResolvePath 把清单相对路径转换为绝对路径。
func (m Manifest) ResolvePath(relative string) string {
	if filepath.IsAbs(relative) {
		return filepath.Clean(relative)
	}
	return filepath.Clean(filepath.Join(m.Root, filepath.FromSlash(relative)))
}

// Validate 报告解析后清单的非致命问题：版本格式、
// 引用的文件不存在，以及无法运行的 MCP 声明。
func (m Manifest) Validate() []Problem {
	var problems []Problem
	add := func(format string, args ...any) {
		problems = append(problems, Problem{Plugin: m.Name, Path: m.Path, Message: fmt.Sprintf(format, args...)})
	}

	if err := ValidateName(m.Name); err != nil {
		add("%v", err)
	}
	if err := ValidateVersion(m.Version); err != nil {
		add("version %q: %v", m.Version, err)
	}

	reportPaths := func(kind string, paths []string, wantDir bool) {
		for _, relative := range paths {
			resolved := m.ResolvePath(relative)
			if !within(m.Root, resolved) {
				add("%s path %q escapes the bundle", kind, relative)
				continue
			}
			info, err := os.Stat(resolved)
			switch {
			case err != nil:
				add("%s path %q does not exist", kind, relative)
			case wantDir && !info.IsDir():
				add("%s path %q is not a directory", kind, relative)
			case !wantDir && !info.Mode().IsRegular():
				add("%s path %q is not a file", kind, relative)
			}
		}
	}
	reportPaths("skills", m.Skills, true)
	reportPaths("commands", m.Commands, false)
	reportPaths("hooks", m.Hooks.Paths, false)

	for _, name := range sortedServerNames(m.MCPServers) {
		spec := m.MCPServers[name]
		if strings.TrimSpace(spec.Command) == "" && strings.TrimSpace(spec.URL) == "" {
			add("mcp server %q has neither command nor url", name)
		}
	}
	return problems
}

// hasPathEscape 报告任何清单声明的路径是否解析到
// bundle 根之外。带有这种路径的清单对接线是致命的：Merge
// 拒绝该 bundle 的所有贡献，使损坏或恶意的清单
// 无法从其目录外注入技能、命令或钩子。Validate
// 仍把该发现报告为非致命问题，使发现能呈现它。
func (m Manifest) hasPathEscape() bool {
	for _, paths := range [][]string{m.Skills, m.Commands, m.Hooks.Paths} {
		for _, relative := range paths {
			if !within(m.Root, m.ResolvePath(relative)) {
				return true
			}
		}
	}
	return false
}

// hasPathEscape 报告任何清单声明的路径是否解析到 bundle 根之外。带有这种路径的清单对接线是致命的：Merge 拒绝该 bundle 的所有贡献，使损坏或恶意的清单无法从其目录外注入技能、命令或钩子。Validate 仍把该发现报告为非致命问题，使发现能呈现它。within 报告 target 是否停留在 root 内。
func within(root, target string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
