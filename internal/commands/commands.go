// Package commands discovers markdown slash commands and expands their bodies.
//
// A command is a `.md` file in `.argent/commands` (project) or
// `.argentgo/commands` (user). Optional frontmatter carries `description`,
// `argument-hint`, `allowed-tools` and `model`; the body is the prompt template.
//
// The substitution rules match the Rust implementation:
//
//   - `$ARGUMENTS` / `$@` — everything after the command name;
//   - `$1` … `$99` — one whitespace-separated argument;
//   - `${1}` — the same, when adjacent text would swallow the digits.
//
// Quoted arguments ("two words" or 'two words') count as one position. With
// ExpandOptions.RemainingAtHighest, the highest-numbered position used by the
// body absorbs every remaining argument, matching the markdown-command
// convention. The safe default keeps the original one-argument-per-position
// behaviour.
//
// A “ !`command` “ span runs a shell command, but only when the caller opts in
// with ExpandOptions.AllowShell; by default the span is left untouched.
package commands

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// ProjectSubdir is where a repository keeps its slash commands.
const ProjectSubdir = ".argent/commands"

// HomeSubdir is where the user keeps commands shared by every project.
const HomeSubdir = ".argentgo/commands"

// Command is one command discovered on disk.
type Command struct {
	// Name is the slash-command name, including any namespace prefix.
	Name string
	// Description is the one-line summary shown by `/help`.
	Description string
	// ArgumentHint is the usage hint (frontmatter `argument-hint`).
	ArgumentHint string
	// AllowedTools narrows the tool allow-list for this prompt.
	AllowedTools []string
	// Model optionally pins the model for this prompt.
	Model string
	// Agent optionally pins the agent/persona for this prompt (frontmatter
	// `agent`). It is exposed for the caller to apply; this package never
	// builds an agent.
	Agent string
	// Body is the template, without frontmatter.
	Body string
	// Path is the file the command came from.
	Path string
	// Plugin is the plugin that provided the command, empty for project and
	// user commands. It is also the name's namespace prefix.
	Plugin string
}

// Source is one directory to scan and the namespace its commands get.
//
// Prefix is empty for project and user directories. A plugin source sets it to
// the plugin name, so its `review` command is reachable as `plugin:review` and
// cannot collide with a user command.
type Source struct {
	Root   string
	Prefix string
}

// DefaultSources returns the command directories for one session, highest
// precedence first: project, then user.
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

// Set is the resolved command list for one session.
type Set struct {
	commands []Command
}

// Discover loads every command under sources.
//
// Sources are searched in order and the first command with a given name wins,
// so a project command shadows a user command of the same name.
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
				// A malformed command file cannot participate in discovery; the
				// error is reported by ParseStrict for callers that want it.
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

// Parse reads one command file. A file without frontmatter still works: its
// stem is the name and its first non-empty line is the description. Malformed
// frontmatter is skipped leniently; use ParseStrict to report file and line.
func Parse(text, path string) Command {
	cmd, err := ParseStrict(text, path)
	if err == nil {
		return cmd
	}
	text = strings.TrimPrefix(text, "\ufeff")
	body := text
	cmd = Command{
		Name: commandNameFromPath(path),
		Body: strings.TrimSpace(body),
		Path: path,
	}
	for _, line := range strings.Split(cmd.Body, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			cmd.Description = trimmed
			break
		}
	}
	return cmd
}

// ParseStrict reads one command file and reports invalid frontmatter with the
// file name and the 1-based line number of the offending line.
func ParseStrict(text, path string) (Command, error) {
	text = strings.TrimPrefix(text, "\ufeff")
	frontmatter, body, ok := splitFrontmatter(text)
	if !ok {
		body = text
		cmd := Command{
			Name: commandNameFromPath(path),
			Body: strings.TrimSpace(body),
			Path: path,
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
		Description:  fields.get("description"),
		ArgumentHint: fields.get("argument-hint"),
		AllowedTools: fields.list("allowed-tools"),
		Model:        fields.get("model"),
		Agent:        fields.get("agent"),
		Body:         strings.TrimSpace(body),
		Path:         path,
	}, nil
}

func commandNameFromPath(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// List returns every command, sorted by name.
func (s *Set) List() []Command {
	if s == nil {
		return nil
	}
	out := make([]Command, len(s.commands))
	copy(out, s.commands)
	return out
}

// Len returns the number of commands.
func (s *Set) Len() int {
	if s == nil {
		return 0
	}
	return len(s.commands)
}

// Get finds one command by name.
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

// Expand renders a command body with the arguments the user typed, using the
// safe defaults (no shell interpolation).
func (c Command) Expand(args string) (string, error) {
	return c.ExpandWith(args, ExpandOptions{})
}

// ExpandWith renders a command body, honouring the options.
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

// Expand is a convenience wrapper around Set.Get + Command.Expand.
func (s *Set) Expand(name, args string) (string, error) {
	cmd, ok := s.Get(name)
	if !ok {
		return "", fmt.Errorf("unknown command: %s", name)
	}
	return cmd.Expand(args)
}

// ExpandOptions controls the optional parts of body expansion.
type ExpandOptions struct {
	// AllowShell enables `` !`command` `` interpolation. It is off by default
	// because the markdown file may come from a plugin the user has not read.
	AllowShell bool
	// Dir is the working directory for interpolated commands.
	Dir string
	// RemainingAtHighest makes the highest $N referenced by the body absorb
	// every remaining argument. Off by default so existing templates that use
	// $1 and $2 as individual positions keep their meaning.
	RemainingAtHighest bool
	// Run overrides how interpolated commands are executed (tests, sandboxes).
	// It receives the command text and returns its output.
	Run func(command string) (string, error)
}

// substituteArgs expands $ARGUMENTS, $@ and $N/${N}. Positions are parsed with
// quote grouping so "two words" remains a single argument.
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

// dollarToken parses one $-prefixed token at i.
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

// splitArguments splits a command line on unquoted whitespace. Quotes only
// group arguments; they are never executed as shell fragments.
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

// fields is the minimal frontmatter reader this package needs. It is a copy of
// the skills parser's scalar half: importing that package would couple two
// otherwise independent directories.
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

// parseFieldsStrict validates frontmatter syntax before reading values, so a
// malformed line can be reported with the line number it came from.
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

// splitFrontmatter is the commands-local copy of the fence splitter.
func splitFrontmatter(text string) (frontmatter, body string, ok bool) {
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
