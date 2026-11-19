package mcp

import (
	"errors"
	"strings"
	"sync"
	"unicode"
)

// Sentinel errors returned before a request reaches the transport. Callers can
// match them with errors.Is and show the wrapped message to the model.
var (
	// ErrDisabled reports a spec whose Enabled field is explicitly false.
	ErrDisabled = errors.New("mcp: server is disabled")
	// ErrReadOnly reports a mutating call blocked by a read-only server.
	ErrReadOnly = errors.New("mcp: server is read-only")
	// ErrToolNotAllowed reports a call excluded by allowedTools/deniedTools.
	ErrToolNotAllowed = errors.New("mcp: tool not allowed")
)

// IsEnabled reports whether the spec may be connected. A nil Enabled means the
// zero-value default: enabled.
func (s Spec) IsEnabled() bool {
	return s.Enabled == nil || *s.Enabled
}

// splitQualified separates "server__tool" into its two parts. A plain name has
// an empty server part.
func splitQualified(name string) (server, tool string) {
	if i := strings.LastIndex(name, "__"); i >= 0 {
		return strings.TrimSpace(name[:i]), strings.TrimSpace(name[i+2:])
	}
	return "", strings.TrimSpace(name)
}

// matchesTool compares an allow/deny entry with a tool. Plain entries match the
// short tool name on any server; qualified entries (or a qualified advertised
// name) also require the server to agree. "*" is accepted as a wildcard part.
func matchesTool(pattern, server, name string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}
	pServer, pTool := splitQualified(pattern)
	tServer, tTool := splitQualified(name)
	if pTool == "*" || strings.EqualFold(pTool, tTool) {
		if pServer == "" || pServer == "*" {
			return true
		}
		if tServer != "" && strings.EqualFold(pServer, tServer) {
			return true
		}
		return strings.EqualFold(pServer, strings.TrimSpace(server))
	}
	return false
}

// toolPermitted applies deny first so deny always wins over allow.
func toolPermitted(spec Spec, tool Tool) bool {
	name := strings.TrimSpace(tool.Name)
	if name == "" {
		return false
	}
	server := strings.TrimSpace(tool.Server)
	if server == "" {
		server = spec.Name
	}
	for _, pattern := range spec.DeniedTools {
		if matchesTool(pattern, server, name) {
			return false
		}
	}
	if len(spec.AllowedTools) == 0 {
		return true
	}
	for _, pattern := range spec.AllowedTools {
		if matchesTool(pattern, server, name) {
			return true
		}
	}
	return false
}

// FilterTools applies spec's allow/deny lists to tools while preserving their
// order. A disabled spec exposes no tools at all.
func FilterTools(spec Spec, tools []Tool) []Tool {
	if !spec.IsEnabled() {
		return nil
	}
	out := make([]Tool, 0, len(tools))
	for _, tool := range tools {
		if toolPermitted(spec, tool) {
			out = append(out, tool)
		}
	}
	return out
}

// EnabledSpecs returns only enabled specs, in the original order.
func EnabledSpecs(specs []Spec) []Spec {
	out := make([]Spec, 0, len(specs))
	for _, spec := range specs {
		if spec.IsEnabled() {
			out = append(out, spec)
		}
	}
	return out
}

// Tools returns the tools cached by the last successful ListTools call, after
// permission filtering. It is empty until ListTools has been called.
func (c *client) Tools() []Tool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.spec.IsEnabled() {
		return nil
	}
	return append([]Tool(nil), c.tools...)
}

// Spec returns the configuration this client was built with.
func (c *client) Spec() Spec { return c.spec }

// Specs returns this server's spec when enabled, or nothing when disabled. It
// lets callers build a uniform view from a group of clients.
func (c *client) Specs() []Spec {
	if !c.spec.IsEnabled() {
		return nil
	}
	return []Spec{c.spec}
}

// Clients is an ordered group of connected MCP servers, for example the
// t.clients slice in the app layer. Tools and Specs expose only enabled
// servers and apply each server's tool filters; foreign Client implementations
// contribute nothing because they have no cached, filtered tool view.
type Clients []Client

// Specs returns one spec per enabled server in connection order.
func (cs Clients) Specs() []Spec {
	out := make([]Spec, 0, len(cs))
	for _, c := range cs {
		if impl, ok := c.(*client); ok {
			out = append(out, impl.Specs()...)
		}
	}
	return out
}

// Tools returns the filtered tools of every enabled server in connection order.
func (cs Clients) Tools() []Tool {
	out := make([]Tool, 0, len(cs))
	for _, c := range cs {
		if impl, ok := c.(*client); ok {
			out = append(out, impl.Tools()...)
		}
	}
	return out
}

// The registry is process-wide so callers can teach the heuristic about custom
// tool names before any server is connected.
var (
	mutatingMu    sync.RWMutex
	mutatingExtra = make(map[string]bool)
)

// MarkMutating registers an extra tool name as mutating. Matching uses the
// short (unqualified) name and is case-insensitive, so both "deploy" and
// "prod__deploy" are covered.
func MarkMutating(name string) {
	key := strings.ToLower(shortName(name))
	if key == "" {
		return
	}
	mutatingMu.Lock()
	mutatingExtra[key] = true
	mutatingMu.Unlock()
}

// IsMutating reports whether name looks like a tool that changes state. It
// combines a conservative built-in heuristic with names added by MarkMutating;
// unknown names are treated as read-only so a novel tool is not silently
// allowed to write.
func IsMutating(name string) bool {
	key := shortName(name)
	if key == "" {
		return false
	}
	mutatingMu.RLock()
	extra := mutatingExtra[strings.ToLower(key)]
	mutatingMu.RUnlock()
	return extra || builtinMutating(key)
}

func shortName(name string) string {
	_, tool := splitQualified(strings.TrimSpace(name))
	return tool
}

// mutatingVerbs covers common write verbs and their abbreviations. Tokens are
// compared exactly, so "read_file" is not flagged while "writeFile" is.
var mutatingVerbs = map[string]bool{
	"add": true, "append": true, "apply": true, "bash": true, "bootstrap": true,
	"checkout": true, "chmod": true, "chown": true, "clean": true, "clear": true,
	"commit": true, "configure": true, "copy": true, "cp": true, "create": true,
	"del": true, "delete": true, "deploy": true, "disable": true, "drop": true,
	"edit": true, "enable": true, "exec": true, "execute": true, "fix": true,
	"fmt": true, "format": true, "grant": true, "init": true, "insert": true,
	"install": true, "kill": true, "migrate": true, "mkdir": true, "modify": true,
	"move": true, "mv": true, "new": true, "overwrite": true, "patch": true,
	"post": true, "publish": true, "put": true, "remove": true, "rename": true,
	"replace": true, "reset": true, "restart": true, "revoke": true, "rm": true,
	"run": true, "save": true, "seed": true, "send": true, "set": true,
	"shell": true, "spawn": true, "start": true, "stop": true, "toggle": true,
	"touch": true, "truncate": true, "uninstall": true, "unlink": true,
	"update": true, "upload": true, "upsert": true, "write": true,
}

func builtinMutating(name string) bool {
	if mutatingVerbs[strings.ToLower(strings.TrimSpace(name))] {
		return true
	}
	for _, token := range toolTokens(name) {
		if mutatingVerbs[token] {
			return true
		}
	}
	return false
}

// toolTokens splits snake_case, kebab-case, camelCase and letter/digit changes
// into lowercase words without pulling in a regexp dependency.
func toolTokens(name string) []string {
	runes := []rune(name)
	var tokens []string
	var word strings.Builder
	flush := func() {
		if word.Len() > 0 {
			tokens = append(tokens, strings.ToLower(word.String()))
			word.Reset()
		}
	}
	for i, r := range runes {
		separator := !unicode.IsLetter(r) && !unicode.IsDigit(r)
		boundary := word.Len() > 0 && i > 0 && unicode.IsUpper(r) && unicode.IsLower(runes[i-1])
		digitEdge := word.Len() > 0 && i > 0 &&
			((unicode.IsDigit(r) && unicode.IsLetter(runes[i-1])) ||
				(unicode.IsLetter(r) && unicode.IsDigit(runes[i-1])))
		if separator || boundary || digitEdge {
			flush()
		}
		if !separator {
			word.WriteRune(r)
		}
	}
	flush()
	return tokens
}
