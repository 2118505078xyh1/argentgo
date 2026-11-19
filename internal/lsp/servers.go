package lsp

import (
	"path/filepath"
	"sort"
	"strings"
)

// DefaultServers returns the built-in launch specifications. Nothing is
// installed automatically: a server missing from PATH fails at Start with an
// actionable message.
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

// Registry maps a language or a file to a server. A caller that needs a
// different mapping registers its own server instead of changing the defaults.
type Registry struct {
	byLanguage map[string]Server
}

// NewRegistry returns a registry preloaded with DefaultServers.
func NewRegistry() *Registry {
	registry := &Registry{byLanguage: make(map[string]Server)}
	for _, server := range DefaultServers() {
		registry.Register(server)
	}
	return registry
}

// Register adds or replaces a server for every language id it declares.
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

// Lookup returns the server owning a language id.
func (r *Registry) Lookup(language string) (Server, bool) {
	server, ok := r.byLanguage[strings.ToLower(strings.TrimSpace(language))]
	return server, ok
}

// ForPath returns the server owning a file, chosen by extension.
func (r *Registry) ForPath(path string) (Server, bool) {
	return r.Lookup(LanguageForPath(path))
}

// Languages lists the registered language ids.
func (r *Registry) Languages() []string {
	languages := make([]string, 0, len(r.byLanguage))
	for language := range r.byLanguage {
		languages = append(languages, language)
	}
	sort.Strings(languages)
	return languages
}

// LanguageForPath maps a file path to its LSP language id.
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
