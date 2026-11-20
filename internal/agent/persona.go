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

// OrchestratorPrompt is the portable, dependency-free delegation strategy.
// The caller decides where to place it in the conversation; this package only
// owns the text.
//
//go:embed prompts/orchestrator.txt
var orchestratorText string

// OrchestratorPrompt returns the wave-based orchestration instructions as a
// trimmed string, so embedded trailing newlines never leak into a system prompt.
func OrchestratorPrompt() string {
	return strings.TrimSpace(orchestratorText)
}

// Limits keep a workspace from loading an unbounded set of persona files into
// the agent loop by accident.
const (
	maxPersonaFiles = 256
	maxPersonaBytes = 64 * 1024
)

// Persona is one named subagent definition loaded from a markdown file.
type Persona struct {
	// Name identifies the persona when a task requests a role.
	Name string
	// System is the markdown body, used as the child's system prompt.
	System string
	// AllowedTools is the optional tool allow-list for the child.
	AllowedTools []string
}

// SubagentConfig adapts a persona to the built-in `task` tool's child config.
// Name has no SubagentConfig field: it is the lookup key, not child state. The
// adapter is deliberately thin - budgets, depth limits and event bridging stay
// with the delegation caller.
func (p Persona) SubagentConfig() SubagentConfig {
	return SubagentConfig{
		System:       p.System,
		AllowedTools: append([]string(nil), p.AllowedTools...),
	}
}

// LoadPersonas reads subagent definitions from the *.md files directly inside
// dir (for example `.argent/agents`). Subdirectories are ignored.
//
// A file counts as a persona only when its frontmatter marks it as a subagent
// (`subagent: true`, or the Kilo-style `mode: subagent`). Other markdown files
// are skipped. The `name` key is optional: when absent it is derived from the
// filename, and `tools` accepts either a comma-separated list or a JSON array.
//
// Loading is best-effort: a malformed file is skipped rather than aborting the
// scan. The first error is returned alongside whatever personas were collected,
// so callers can either surface it or ignore it for a lenient startup. Files
// without a complete frontmatter fence are treated as "not a persona" rather
// than reported as errors.
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

// parsePersona parses one markdown file. ok is false for files that are simply
// not subagent definitions; err is non-nil for malformed frontmatter.
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

// splitPersonaFrontmatter separates a leading `---` fence from the markdown
// body. A missing or unterminated fence means the file is not a persona.
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

// parsePersonaFields is a small, local frontmatter reader. Importing
// internal/commands would couple independent directories, so this mirrors
// only the scalar half of that parser.
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

// parsePersonaTools accepts `read, write` and `["read", "write"]` alike.
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
