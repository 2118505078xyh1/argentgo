// Package security is the gate in front of every tool call: a declarative
// policy, an approval prompt, and an append-only audit trail.
//
// Two layers stay independent, as in the Rust reference:
//
//   - Policy decides. A rule that matches blocks; an empty rule set blocks
//     nothing except paths that lexically escape the workspace root.
//   - Audit records. Every decision lands in a JSONL file so "what did this
//     agent do to my machine" has an answer independent of the transcript.
//
// The package is self-contained: stdlib only, no dependency on other
// internal packages.
package security

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Decision is the verdict for one tool call.
type Decision int

const (
	// Allow runs the call without asking anyone.
	Allow Decision = iota
	// Ask defers to the approver.
	Ask
	// Deny stops the call no matter what the approver would say.
	Deny
)

// String returns the lowercase name used in logs.
func (d Decision) String() string {
	switch d {
	case Allow:
		return "allow"
	case Ask:
		return "ask"
	default:
		return "deny"
	}
}

// ParseDecision is the inverse of Decision.String.
func ParseDecision(text string) (Decision, bool) {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "allow", "allowed":
		return Allow, true
	case "ask":
		return Ask, true
	case "deny", "denied":
		return Deny, true
	}
	return Ask, false
}

// Rule is one row of the ordered allow/ask/deny table. Empty fields are
// wildcards; the first matching row wins.
type Rule struct {
	Decision Decision
	Tool     string
	Path     string
	Command  string
}

// PolicyConfig is the declarative rule set. Zero values mean "no rule".
type PolicyConfig struct {
	// DenyPaths are path patterns no tool may touch.
	DenyPaths []string
	// AllowPaths, when non-empty, closes every path not listed.
	AllowPaths []string
	// DenyCommands are substrings or globs that block a command.
	DenyCommands []string
	// DenyCommandPaths block a command mentioning a denied path fragment.
	DenyCommandPaths []string
	// DenyTools are tool names that are never allowed.
	DenyTools []string
	// AllowTools, when non-empty, is the only set of tool names allowed at
	// all.
	AllowTools []string
	// Rules is the ordered allow/ask/deny table.
	Rules []Rule
	// ReadOnlyTools extend the built-in safe-tool list.
	ReadOnlyTools []string
	// MutatingTools extend the built-in mutating-tool list.
	MutatingTools []string
	// WriteRoots extends the roots in which mutating path tools may write. The
	// workspace root is always included, even when this list is empty.
	WriteRoots []string
}

// Result carries the verdict plus the human-readable reason behind it.
type Result struct {
	Decision Decision
	Reason   string
}

var defaultReadOnlyTools = map[string]bool{
	"read": true, "ls": true, "grep": true, "find": true, "glob": true,
	"todo": true, "web_fetch": true, "git_status": true,
}

var defaultMutatingTools = map[string]bool{
	"write": true, "edit": true, "multi_edit": true, "bash": true,
	"undo": true, "restore": true,
}

// Policy is the hard gate evaluated before approval.
type Policy struct {
	root             string
	denyPaths        []string
	allowPaths       []string
	denyCommands     []string
	denyCommandPaths []string
	denyTools        map[string]bool
	allowTools       map[string]bool
	rules            []Rule
	// toolKinds maps a tool name to whether it mutates the workspace.
	toolKinds map[string]bool
	// writeRoots bounds mutating path tools. Read-only tools are not checked.
	writeRoots []string
}

// NewPolicy builds a policy rooted at root (normally the workspace cwd).
// Relative paths in tool arguments are resolved against it.
func NewPolicy(root string, cfg PolicyConfig) *Policy {
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	p := &Policy{
		root:             filepath.Clean(root),
		denyPaths:        cfg.DenyPaths,
		allowPaths:       cfg.AllowPaths,
		denyCommands:     cfg.DenyCommands,
		denyCommandPaths: cfg.DenyCommandPaths,
		denyTools:        make(map[string]bool, len(cfg.DenyTools)),
		allowTools:       make(map[string]bool, len(cfg.AllowTools)),
		rules:            cfg.Rules,
		toolKinds:        make(map[string]bool, len(defaultMutatingTools)+len(cfg.MutatingTools)),
		writeRoots:       WriteRootsFrom(root, cfg.WriteRoots),
	}
	for name := range defaultReadOnlyTools {
		p.toolKinds[name] = false
	}
	for name, mutating := range defaultMutatingTools {
		p.toolKinds[name] = mutating
	}
	for _, name := range cfg.ReadOnlyTools {
		p.toolKinds[name] = false
	}
	for _, name := range cfg.MutatingTools {
		p.toolKinds[name] = true
	}
	for _, name := range cfg.DenyTools {
		p.denyTools[name] = true
	}
	for _, name := range cfg.AllowTools {
		p.allowTools[name] = true
	}
	return p
}

// DefaultPolicy is an empty rule set rooted at root: only workspace escapes
// are blocked.
func DefaultPolicy(root string) *Policy {
	return NewPolicy(root, PolicyConfig{})
}

// PolicyFromJSON parses the `permissions` block of a settings file:
//
//	{"denyPaths":["**/.env"],"denyCommands":["git push --force"],
//	 "denyTools":["bash"],"allowTools":["read","ls"],
//	 "rules":[{"decision":"ask","tool":"bash","command":"git push*"}]}
//
// List fields also accept a comma-separated string.
func PolicyFromJSON(root string, data []byte) (*Policy, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("security: parsing policy: %w", err)
	}
	var cfg PolicyConfig
	var err error
	fields := []struct {
		camel string
		snake string
		into  *[]string
	}{
		{"denyPaths", "deny_paths", &cfg.DenyPaths},
		{"allowPaths", "allow_paths", &cfg.AllowPaths},
		{"denyCommands", "deny_commands", &cfg.DenyCommands},
		{"denyCommandPaths", "deny_command_paths", &cfg.DenyCommandPaths},
		{"denyTools", "deny_tools", &cfg.DenyTools},
		{"allowTools", "allow_tools", &cfg.AllowTools},
		{"readOnlyTools", "read_only_tools", &cfg.ReadOnlyTools},
		{"mutatingTools", "mutating_tools", &cfg.MutatingTools},
		{"writeRoots", "write_roots", &cfg.WriteRoots},
	}
	for _, field := range fields {
		value, ok := raw[field.camel]
		if !ok {
			value, ok = raw[field.snake]
		}
		if ok {
			if *field.into, err = stringList(value); err != nil {
				return nil, fmt.Errorf("security: policy field %q: %w", field.camel, err)
			}
		}
	}
	if value, ok := raw["rules"]; ok {
		if cfg.Rules, err = parseRules(value); err != nil {
			return nil, fmt.Errorf("security: policy rules: %w", err)
		}
	}
	return NewPolicy(root, cfg), nil
}

// Root is the workspace root used to resolve relative paths.
func (p *Policy) Root() string { return p.root }

// IsEmpty reports whether any rule at all is configured.
func (p *Policy) IsEmpty() bool {
	return len(p.denyPaths) == 0 && len(p.allowPaths) == 0 &&
		len(p.denyCommands) == 0 && len(p.denyCommandPaths) == 0 &&
		len(p.denyTools) == 0 && len(p.allowTools) == 0 && len(p.rules) == 0
}

// Check evaluates one tool call. It satisfies the harness Guard contract; use
// Evaluate when the reason is needed too.
func (p *Policy) Check(name string, args json.RawMessage) Decision {
	return p.Evaluate(name, args).Decision
}

// Evaluate returns the verdict and the reason behind it. An empty reason
// accompanies a plain Allow.
func (p *Policy) Evaluate(name string, args json.RawMessage) Result {
	name = strings.TrimSpace(name)
	if name == "" {
		return Result{Deny, "the tool name is empty"}
	}
	if p.denyTools[name] {
		return Result{Deny, fmt.Sprintf("the tool %q is denied by policy", name)}
	}
	if len(p.allowTools) > 0 && !p.allowTools[name] {
		return Result{Deny, fmt.Sprintf("the tool %q is not on the allow list", name)}
	}
	fields, err := parseArgs(args)
	if err != nil {
		return Result{Ask, fmt.Sprintf("the %s arguments are not a JSON object: %v", name, err)}
	}
	for _, path := range pathValues(fields) {
		if res := p.checkPath(path); res.Decision != Allow {
			return res
		}
	}
	if command, ok := fields["command"].(string); ok {
		if res := p.checkCommand(command); res.Decision != Allow {
			return res
		}
	}
	// Writes are bounded by the workspace plus any --add-dir roots. Read-only
	// tools deliberately skip this check: a path can be readable without being
	// writable, and the path rules above still apply to every tool.
	if mutating, known := p.toolKinds[name]; known && mutating {
		if res := p.checkWriteRoots(fields); res.Decision != Allow {
			return res
		}
	}
	if res, ok := p.ruleDecision(name, fields); ok {
		return res
	}
	if mutating, known := p.toolKinds[name]; known {
		if mutating {
			return Result{Ask, fmt.Sprintf("the mutating tool %q needs operator approval", name)}
		}
		return Result{Allow, ""}
	}
	return Result{Ask, fmt.Sprintf("the unknown tool %q needs operator approval", name)}
}

var pathArgKeys = []string{"path", "file", "dir", "directory", "target"}

// checkPath applies the deny/allow path rules to one argument and enforces the
// workspace boundary for every tool, including read-only ones. A path that
// lexically climbs out of the workspace root is always denied, and so is a path
// that looks inside but resolves outside through a symlink or Windows junction.
func (p *Policy) checkPath(path string) Result {
	resolved, escape := p.resolve(path)
	if escape {
		return Result{Deny, fmt.Sprintf("the path %q escapes the workspace root %q", path, p.root)}
	}
	real, err := ResolveRealPath(resolved)
	if err != nil {
		return Result{Deny, fmt.Sprintf(
			"the path %q could not be canonicalized for policy checking (%v); denying to avoid an unresolved symlink target",
			path, err)}
	}
	// Hide-escape check. It runs even when no deny/allow paths are configured:
	// a read of <root>/link/id_rsa must not become a read of an external file
	// just because the policy has no explicit path rules.
	if within(resolved, p.writeRoots) && !within(real, p.realRoots()) {
		return Result{Deny, fmt.Sprintf(
			"the path %q resolves outside the workspace root %q through a symlink or junction", path, p.root)}
	}
	lexical := normalize(resolved)
	canonical := normalize(real)
	for _, pattern := range p.denyPaths {
		if GlobMatch(pattern, lexical) || GlobMatch(pattern, canonical) {
			return Result{Deny, fmt.Sprintf("the path %q is denied by policy (rule %q)", path, pattern)}
		}
	}
	if len(p.allowPaths) > 0 {
		if !anyMatch(p.allowPaths, lexical) || !anyMatch(p.allowPaths, canonical) {
			return Result{Deny, fmt.Sprintf("the path %q is outside the allowed set", path)}
		}
	}
	return Result{Allow, ""}
}

// realRoots resolves each configured root so a root reached through a link
// still counts as inside. A root that cannot be resolved falls back to its
// lexical spelling; explicit roots are operator configuration, not tool input.
func (p *Policy) realRoots() []string {
	out := make([]string, 0, len(p.writeRoots))
	for _, root := range p.writeRoots {
		if real, err := ResolveRealPath(root); err == nil {
			out = append(out, real)
		} else {
			out = append(out, filepath.Clean(root))
		}
	}
	return out
}

// checkWriteRoots applies the write boundary to every path argument of a
// mutating tool. Relative `..` escapes were already rejected by checkPath; here
// InWriteRoots catches absolute paths that land outside the allowed roots.
func (p *Policy) checkWriteRoots(fields map[string]any) Result {
	for _, value := range pathValues(fields) {
		if err := InWriteRoots(value, p.writeRoots); err != nil {
			return Result{Deny, fmt.Sprintf("the path %q is not writable: %v", value, err)}
		}
	}
	return Result{Allow, ""}
}

// pathValues collects every path argument, including the `paths` array used
// by checkpoint restore actions. Order is stable: scalar keys first, then the
// array in the order supplied by the caller.
func pathValues(fields map[string]any) []string {
	var paths []string
	for _, key := range pathArgKeys {
		if value, ok := fields[key].(string); ok {
			paths = append(paths, value)
		}
	}
	if list, ok := fields["paths"].([]any); ok {
		for _, item := range list {
			if value, ok := item.(string); ok {
				paths = append(paths, value)
			}
		}
	}
	return paths
}

// checkCommand applies the command rules. Matching is case-insensitive.
func (p *Policy) checkCommand(command string) Result {
	lowered := strings.ToLower(command)
	for _, pattern := range p.denyCommands {
		if strings.Contains(lowered, strings.ToLower(pattern)) || GlobMatch(pattern, lowered) {
			return Result{Deny, fmt.Sprintf("the command is denied by policy (rule %q)", pattern)}
		}
	}
	for _, pattern := range p.denyCommandPaths {
		needle := strings.ToLower(pattern)
		if strings.Contains(lowered, needle) {
			return Result{Deny, fmt.Sprintf("the command touches a denied path (rule %q)", pattern)}
		}
		if expanded := strings.ToLower(expandHome(pattern)); expanded != needle && strings.Contains(lowered, expanded) {
			return Result{Deny, fmt.Sprintf("the command touches a denied path (rule %q)", pattern)}
		}
	}
	return Result{Allow, ""}
}

// ruleDecision walks the ordered table. The first matching row wins.
func (p *Policy) ruleDecision(name string, fields map[string]any) (Result, bool) {
	for _, rule := range p.rules {
		if rule.Tool != "" && rule.Tool != "*" && rule.Tool != name {
			continue
		}
		if rule.Path != "" {
			matched := false
			for _, key := range pathArgKeys {
				value, ok := fields[key].(string)
				if !ok {
					continue
				}
				resolved, escape := p.resolve(value)
				if escape {
					matched = true
					break
				}
				lexical := normalize(resolved)
				if GlobMatch(rule.Path, lexical) {
					matched = true
					break
				}
				if canonical, err := ResolveRealPath(resolved); err == nil && GlobMatch(rule.Path, normalize(canonical)) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		if rule.Command != "" {
			command, ok := fields["command"].(string)
			if !ok || !commandMatches(rule.Command, command) {
				continue
			}
		}
		reason := ""
		switch rule.Decision {
		case Deny:
			reason = fmt.Sprintf("the %s call is denied by the permissions table", name)
		case Ask:
			reason = fmt.Sprintf("the %s call requires approval per the permissions table", name)
		}
		return Result{rule.Decision, reason}, true
	}
	return Result{}, false
}

// resolve turns a tool path argument into an absolute path. The bool reports
// a lexical escape from the workspace root (a relative path climbing out).
func (p *Policy) resolve(path string) (string, bool) {
	candidate := expandHome(strings.TrimSpace(path))
	if candidate == "" {
		candidate = "."
	}
	relative := !filepath.IsAbs(candidate)
	if relative {
		candidate = filepath.Join(p.root, candidate)
	}
	candidate = filepath.Clean(candidate)
	if relative {
		if rel, err := filepath.Rel(p.root, candidate); err != nil || rel == ".." ||
			strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return candidate, true
		}
	}
	return candidate, false
}

func isMissing(err error) bool {
	return os.IsNotExist(err) || strings.Contains(strings.ToLower(err.Error()), "not a directory")
}

func anyMatch(patterns []string, path string) bool {
	for _, pattern := range patterns {
		if GlobMatch(pattern, path) {
			return true
		}
	}
	return false
}

func parseArgs(args json.RawMessage) (map[string]any, error) {
	trimmed := strings.TrimSpace(string(args))
	if trimmed == "" || trimmed == "null" {
		return map[string]any{}, nil
	}
	var fields map[string]any
	if err := json.Unmarshal(args, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		fields = map[string]any{}
	}
	normalizeCommandField(fields)
	return fields, nil
}

// normalizeCommandField mirrors the tool layer's alias handling. Tools such as
// powershell accept `script` as an alias for `command` and execute it whenever
// `command` is empty (whitespace included). checkCommand and ruleDecision only
// read `command`, so without this normalization a hard deny can be downgraded
// to Ask simply by renaming the field — and auto-executed under --yolo.
//
// The fallback is deliberately conservative: an explicit, non-empty `command`
// always wins and is never overwritten. Only the map used by the policy
// evaluator is touched; the raw arguments forwarded to the tool stay untouched.
func normalizeCommandField(fields map[string]any) {
	if command, ok := fields["command"].(string); ok && strings.TrimSpace(command) != "" {
		return
	}
	script, ok := fields["script"].(string)
	if !ok || strings.TrimSpace(script) == "" {
		return
	}
	fields["command"] = script
}

func stringList(raw json.RawMessage) ([]string, error) {
	var items []string
	if err := json.Unmarshal(raw, &items); err == nil {
		return items, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return nil, fmt.Errorf("expected a list of strings or a comma-separated string")
	}
	out := make([]string, 0, 4)
	for _, item := range strings.Split(text, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out, nil
}

func parseRules(raw json.RawMessage) ([]Rule, error) {
	var items []struct {
		Decision string `json:"decision"`
		Tool     string `json:"tool"`
		Path     string `json:"path"`
		Command  string `json:"command"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	rules := make([]Rule, 0, len(items))
	for _, item := range items {
		decision, ok := ParseDecision(item.Decision)
		if !ok {
			continue
		}
		rules = append(rules, Rule{
			Decision: decision,
			Tool:     strings.TrimSpace(item.Tool),
			Path:     strings.TrimSpace(item.Path),
			Command:  strings.TrimSpace(item.Command),
		})
	}
	return rules, nil
}

// MutatingTool reports whether the built-in table considers a tool mutating.
func MutatingTool(name string) bool {
	return defaultMutatingTools[name]
}

func commandMatches(pattern, command string) bool {
	lowered := strings.ToLower(command)
	needle := strings.ToLower(pattern)
	if strings.Contains(lowered, needle) {
		return true
	}
	return matchChars([]rune(needle), []rune(lowered))
}

func expandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, `~\`) {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}
