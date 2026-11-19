// Package commands discovers markdown slash commands and expands their bodies.
//
// A command is a `.md` file in `.argent/commands` (project) or
// `.argent-go/commands` (user). Optional frontmatter carries `description`,
// `argument-hint`, `allowed-tools` and `model`; the body is the prompt template.
//
// The substitution rules match the Rust implementation:
//
//   - `$ARGUMENTS` / `$@` — everything after the command name;
//   - `$1` … `$9` — one whitespace-separated argument;
//   - `${1}` — the same, when adjacent text would swallow the digits.
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
const HomeSubdir = ".argent-go/commands"

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
			cmd := Parse(string(text), file)
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
// stem is the name and its first non-empty line is the description.
func Parse(text, path string) Command {
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
		return cmd
	}
	fields := parseFields(frontmatter)
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
		Body:         strings.TrimSpace(body),
		Path:         path,
	}
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
	return substituteArgs(body, args), nil
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
	// Run overrides how interpolated commands are executed (tests, sandboxes).
	// It receives the command text and returns its output.
	Run func(command string) (string, error)
}

func substituteArgs(body, args string) string {
	fields := strings.Fields(args)
	pairs := make([]string, 0, 22)
	pairs = append(pairs, "$ARGUMENTS", args, "$@", args)
	for i := 1; i <= 9; i++ {
		value := ""
		if i <= len(fields) {
			value = fields[i-1]
		}
		pairs = append(pairs, fmt.Sprintf("${%d}", i), value, fmt.Sprintf("$%d", i), value)
	}
	return strings.NewReplacer(pairs...).Replace(body)
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
