package lsp

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// DefaultServers 返回内置的启动规格。不会自动安装任何内容：PATH 中缺失的服务器会在 Start 时以可操作的消息失败。
func DefaultServers() []Server {
	return []Server{
		{Name: "gopls", Command: "gopls", LanguageIDs: []string{"go"}},
		{Name: "rust-analyzer", Command: "rust-analyzer", LanguageIDs: []string{"rust"}},
		{
			Name:        "typescript",
			Command:     "typescript-language-server",
			Args:        []string{"--stdio"},
			LanguageIDs: []string{"typescript", "typescriptreact", "javascript", "javascriptreact"},
		},
		{
			Name:        "pyright",
			Command:     "pyright-langserver",
			Args:        []string{"--stdio"},
			LanguageIDs: []string{"python"},
		},
	}
}

// 注册相关的哨兵错误，调用方可用 errors.Is 判断。
var (
	// ErrEmptyServerName 表示注册项的名称为空。
	ErrEmptyServerName = errors.New("lsp: empty server name")
	// ErrDuplicateServer 表示同名服务器被重复注册。
	ErrDuplicateServer = errors.New("lsp: duplicate server name")
)

// serverRegistration 是一条带显式顺序的服务器注册项。
// order 决定 Servers/Names 的输出顺序，与 pluginapi 的 registration 语义对齐。
type serverRegistration struct {
	name   string
	order  int
	server Server
}

// Registry 把语言或文件映射到服务器，并按 (order, name) 维护一份稳定的服务器快照。
// 需要不同映射的调用方应注册自己的服务器，而不是更改默认值。并发安全，零值可用。
type Registry struct {
	mu         sync.RWMutex
	servers    map[string]serverRegistration
	byLanguage map[string]Server
	nextOrder  int
}

// NewRegistry 返回预载了 DefaultServers 的 registry。
func NewRegistry() *Registry {
	registry := &Registry{}
	for _, server := range DefaultServers() {
		_ = registry.Register(server)
	}
	return registry
}

// Register 为其声明的每个语言 id 添加或替换服务器，并记录一条按名称索引的注册项。
// 可选的 order 给出显式顺序；省略时按注册先后自动分配递增序号。
// 同名重复注册返回 ErrDuplicateServer，空名称返回 ErrEmptyServerName。
// 兼容旧调用：registry.Register(server) 依旧合法并忽略返回的错误。
func (r *Registry) Register(server Server, order ...int) error {
	name := strings.TrimSpace(server.Name)
	if name == "" {
		return ErrEmptyServerName
	}
	explicit := len(order) > 0
	assigned := 0
	if explicit {
		assigned = order[0]
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.servers == nil {
		r.servers = make(map[string]serverRegistration)
	}
	if r.byLanguage == nil {
		r.byLanguage = make(map[string]Server)
	}
	if _, exists := r.servers[name]; exists {
		return fmt.Errorf("%w: %q", ErrDuplicateServer, name)
	}
	if !explicit {
		assigned = r.nextOrder
	}
	if assigned >= r.nextOrder {
		r.nextOrder = assigned + 1
	}

	r.servers[name] = serverRegistration{name: name, order: assigned, server: server}
	for _, language := range server.LanguageIDs {
		language = strings.ToLower(strings.TrimSpace(language))
		if language == "" {
			continue
		}
		r.byLanguage[language] = server
	}
	return nil
}

// RegisterOrdered 以显式 order 注册服务器，等价于 Register(server, order)。
// 它提供与 pluginapi.Register(name, order, ...) 对齐的显式顺序入口。
func (r *Registry) RegisterOrdered(server Server, order int) error {
	return r.Register(server, order)
}

// ordered 返回按 (order, name) 稳定排序的注册项快照。
func (r *Registry) ordered() []serverRegistration {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.servers) == 0 {
		return nil
	}
	list := make([]serverRegistration, 0, len(r.servers))
	for _, item := range r.servers {
		list = append(list, item)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].order != list[j].order {
			return list[i].order < list[j].order
		}
		return list[i].name < list[j].name
	})
	return list
}

// Names 返回按 (order, name) 稳定排序的服务器名称快照。
func (r *Registry) Names() []string {
	list := r.ordered()
	if len(list) == 0 {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		out = append(out, item.name)
	}
	return out
}

// Servers 返回按 (order, name) 稳定排序的服务器快照，修改返回值不影响注册表。
func (r *Registry) Servers() []Server {
	list := r.ordered()
	if len(list) == 0 {
		return nil
	}
	out := make([]Server, 0, len(list))
	for _, item := range list {
		out = append(out, item.server)
	}
	return out
}

// Len 返回已注册的服务器数量。
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.servers)
}

// Lookup 返回拥有某语言 id 的服务器。
func (r *Registry) Lookup(language string) (Server, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	server, ok := r.byLanguage[strings.ToLower(strings.TrimSpace(language))]
	return server, ok
}

// ForPath 按扩展名返回拥有某文件的服务器。
func (r *Registry) ForPath(path string) (Server, bool) {
	return r.Lookup(LanguageForPath(path))
}

// Languages 列出已注册的语言 id。
func (r *Registry) Languages() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	languages := make([]string, 0, len(r.byLanguage))
	for language := range r.byLanguage {
		languages = append(languages, language)
	}
	sort.Strings(languages)
	return languages
}

// LanguageForPath 把文件路径映射到其 LSP 语言 id。
func LanguageForPath(path string) string {
	extension := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	if language, ok := extensionLanguages[extension]; ok {
		return language
	}
	if extension == "" {
		return "plaintext"
	}
	return extension
}

var extensionLanguages = map[string]string{
	"go":    "go",
	"rs":    "rust",
	"ts":    "typescript",
	"tsx":   "typescriptreact",
	"js":    "javascript",
	"mjs":   "javascript",
	"cjs":   "javascript",
	"jsx":   "javascriptreact",
	"py":    "python",
	"pyi":   "python",
	"c":     "c",
	"h":     "c",
	"cc":    "cpp",
	"cpp":   "cpp",
	"cxx":   "cpp",
	"hpp":   "cpp",
	"hh":    "cpp",
	"json":  "json",
	"jsonc": "json",
	"md":    "markdown",
	"toml":  "toml",
	"yaml":  "yaml",
	"yml":   "yaml",
	"lua":   "lua",
	"zig":   "zig",
}
