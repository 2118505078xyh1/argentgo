// Package plugins discovers, validates and installs ZCode-compatible plugin
// bundles without running any plugin code.
//
// A bundle is a directory with a manifest and any combination of MCP server
// declarations, skills, commands and hook documents. The manifest names are
// recognised in this order, which also makes Claude Code and Codex bundles
// loadable as-is:
//
//	.zcode-plugin/plugin.json
//	.claude-plugin/plugin.json
//	.codex-plugin/plugin.json
//	plugin.json
//
// Discovery produces Wiring, which the caller hands to its own MCP, skill and
// hook loaders; this package never starts a server or executes a hook.
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

// ManifestPaths lists the candidate manifest locations inside a bundle, in
// precedence order.
var ManifestPaths = []string{
	".zcode-plugin/plugin.json",
	".claude-plugin/plugin.json",
	".codex-plugin/plugin.json",
	"plugin.json",
}

// Sentinel errors.
var (
	// ErrNoManifest reports a directory without a recognised manifest.
	ErrNoManifest = errors.New("plugins: no plugin manifest found")
	// ErrInvalidName reports an unsafe plugin name.
	ErrInvalidName = errors.New("plugins: invalid plugin name")
	// ErrInvalidManifest reports a manifest that is not valid JSON or not an object.
	ErrInvalidManifest = errors.New("plugins: invalid plugin manifest")
)

// MCPServer is one MCP server a bundle declares.
type MCPServer struct {
	Command string
	Args    []string
	Env     map[string]string
	URL     string
}

// Hooks is the hook contribution of a bundle. A manifest may name hook
// documents, inline a hook document, or both.
type Hooks struct {
	// Paths are manifest-declared hook document paths, relative to the bundle.
	Paths []string
	// Inline is a hook document embedded in the manifest, if any.
	Inline json.RawMessage
}

// Manifest is a parsed, bundle-rooted plugin manifest.
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

	// Root is the bundle directory and Path is the manifest file.
	Root string
	Path string
}

// Problem is one non-fatal validation finding. Resolve and Discovery report
// problems without refusing the whole set of bundles.
type Problem struct {
	Plugin  string `json:"plugin,omitempty"`
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
}

// String renders a problem for logs.
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

// FindManifest returns the manifest path inside dir, without descending.
func FindManifest(dir string) (string, bool) {
	for _, relative := range ManifestPaths {
		candidate := filepath.Join(dir, filepath.FromSlash(relative))
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate, true
		}
	}
	return "", false
}

// LoadManifest reads the manifest inside dir.
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

// ParseManifest parses manifest bytes for a bundle rooted at dir. manifestPath
// is recorded on the result and only used for error text.
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

// rawManifest mirrors the manifest schema. Polymorphic fields stay raw so the
// parser accepts every layout seen in the wild.
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

// parseAuthor accepts either "someone" or {"name": "someone"}.
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

// parseStringList accepts a single string, an array of strings, or nothing.
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

// parseHooks accepts a path, an array of paths, an inline document, or an
// object with a "paths" key next to inline event handlers.
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

// ValidateName reports whether name is safe to use as a single directory name.
//
// Plugin names become directory names during install and uninstall, so only
// letters, digits and '-', '_', '.', '@' are accepted; traversal, absolute
// paths, control characters and Windows device names are refused.
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

// ValidateVersion reports whether v is a semantic version: three numeric
// components with optional -prerelease and +build suffixes.
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

// ResolvePath turns a manifest-relative path into an absolute one.
func (m Manifest) ResolvePath(relative string) string {
	if filepath.IsAbs(relative) {
		return filepath.Clean(relative)
	}
	return filepath.Clean(filepath.Join(m.Root, filepath.FromSlash(relative)))
}

// Validate reports non-fatal problems with a parsed manifest: version format,
// referenced files that do not exist, and MCP declarations that cannot run.
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

// within reports whether target stays inside root.
func within(root, target string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
