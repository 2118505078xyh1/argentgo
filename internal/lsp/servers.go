package lsp

import (
	"path/filepath"
	"sort"
	"strings"
)

// DefaultServers 返回内置的启动规格。不会自动
// DefaultServers 返回内置的启动规格。不会自动安装任何内容：PATH 中缺失的服务器会在 Start 时以可操作的消息失败。
// 安装任何内容：PATH 中缺失的服务器会在 Start 时以可操作消息失败。
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

// Registry 把语言或文件映射到服务器。需要不同映射的调用方应注册自己的服务器，而不是更改默认值。
// 不同映射的调用方应注册自己的服务器，而不是更改默认值。
type Registry struct {
	byLanguage map[string]Server
}

// NewRegistry 返回预载了 DefaultServers 的 registry。
func NewRegistry() *Registry {
	registry := &Registry{byLanguage: make(map[string]Server)}
	for _, server := range DefaultServers() {
		registry.Register(server)
	}
	return registry
}

// Register 为其声明的每个语言 id 添加或替换服务器。
func (r *Registry) Register(server Server) {
	if r.byLanguage == nil {
		r.byLanguage = make(map[string]Server)
	}
	for _, language := range server.LanguageIDs {
		language = strings.ToLower(strings.TrimSpace(language))
		if language == "" {
			continue
		}
		r.byLanguage[language] = server
	}
}

// Lookup 返回拥有某语言 id 的服务器。
func (r *Registry) Lookup(language string) (Server, bool) {
	server, ok := r.byLanguage[strings.ToLower(strings.TrimSpace(language))]
	return server, ok
}

// ForPath 按扩展名返回拥有某文件的服务器。
func (r *Registry) ForPath(path string) (Server, bool) {
	return r.Lookup(LanguageForPath(path))
}

// Languages 列出已注册的语言 id。
func (r *Registry) Languages() []string {
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
