package commands

// Command-discovery diagnostics.
//
// Discover and Set.Add deliberately drop candidates whose name is already
// taken, and ParseStrict rejects malformed frontmatter. That keeps the resolved
// Set small but hides why a command disappeared. The functions here are pure,
// read-only inspectors for callers that want to surface those decisions (doctor
// output, UI warnings, tests): they never read files, mutate their input or
// change discovery.

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
)

// DiagnosticKind classifies one command-discovery problem.
type DiagnosticKind string

const (
	DiagnosticInvalidFrontmatter DiagnosticKind = "invalid-frontmatter"
	DiagnosticInvalidName        DiagnosticKind = "invalid-name"
	DiagnosticEmptyBody          DiagnosticKind = "empty-body"
	DiagnosticInvalidContent     DiagnosticKind = "invalid-content"
	DiagnosticMissingArgument    DiagnosticKind = "missing-argument"
	DiagnosticDuplicateName      DiagnosticKind = "duplicate-name"
	DiagnosticDuplicateSource    DiagnosticKind = "duplicate-source"
)

// Diagnostic is one problem found while inspecting command candidates.
// Name is set for name-related diagnostics. Winner is the path of the
// higher-priority candidate discovery keeps for a duplicate name or source.
// Line is the 1-based frontmatter line for syntax errors, zero otherwise.
type Diagnostic struct {
	Kind    DiagnosticKind
	Path    string
	Line    int
	Name    string
	Winner  string
	Message string
}

// DiagnoseCommands reports every problem in a priority-ordered candidate list.
// The first candidate with a given name or path is treated as the winner,
// matching Discover and Set.Add. Nothing is filtered or modified; a nil or
// empty list yields no diagnostics.
//
// Callers should pass the un-deduplicated candidates (one per parsed command
// file, sources in priority order) so shadowed commands remain visible.
func DiagnoseCommands(cmds []Command) []Diagnostic {
	var out []Diagnostic

	// Per-candidate validation, in input order.
	for _, cmd := range cmds {
		path := diagnosticPath(cmd.Path)
		if !validCommandName(cmd.Name) {
			out = append(out, Diagnostic{
				Kind: DiagnosticInvalidName, Path: path,
				Message: fmt.Sprintf("invalid command name %q", cmd.Name),
			})
		}
		if strings.TrimSpace(cmd.Body) == "" {
			out = append(out, Diagnostic{
				Kind: DiagnosticEmptyBody, Path: path,
				Message: "command body is empty",
			})
		}
		for _, warning := range cmd.Warnings {
			out = append(out, Diagnostic{
				Kind: DiagnosticInvalidContent, Path: path,
				Message: clipFinding(warning, 160),
			})
		}
		// A body that reads a positional placeholder should say what the
		// position means. Named placeholders ($ARGUMENTS/$@) are self-describing
		// and need no hint.
		if highest := highestPosition(cmd.Body); highest > 0 && strings.TrimSpace(cmd.ArgumentHint) == "" {
			out = append(out, Diagnostic{
				Kind: DiagnosticMissingArgument, Path: path,
				Message: fmt.Sprintf("body references $%d but argument-hint is missing", highest),
			})
		}
	}

	// Shadowed names. Grouping by trimmed name also catches a name that only
	// differs by surrounding whitespace, which Discover keeps but Get resolves
	// to the first entry.
	first := make(map[string]int, len(cmds))
	for i, cmd := range cmds {
		name := strings.TrimSpace(cmd.Name)
		if name == "" {
			continue // already reported as an invalid name
		}
		if j, ok := first[name]; ok {
			winner := diagnosticPath(cmds[j].Path)
			out = append(out, Diagnostic{
				Kind: DiagnosticDuplicateName, Path: diagnosticPath(cmd.Path),
				Name: name, Winner: winner,
				Message: fmt.Sprintf("command %q is shadowed by %s", name, winner),
			})
			continue
		}
		first[name] = i
	}

	// The same file contributed twice (e.g. two sources pointing at one dir).
	seen := make(map[string]int, len(cmds))
	for i, cmd := range cmds {
		key := pathKey(cmd.Path)
		if key == "" {
			continue
		}
		if j, ok := seen[key]; ok {
			winner := diagnosticPath(cmds[j].Path)
			path := diagnosticPath(cmd.Path)
			out = append(out, Diagnostic{
				Kind: DiagnosticDuplicateSource, Path: path,
				Winner:  winner,
				Message: fmt.Sprintf("command file %s was contributed more than once (first from %s)", path, winner),
			})
			continue
		}
		seen[key] = i
	}
	return out
}

// DiagnoseCommandText diagnoses one raw markdown command file. It is the
// frontmatter-aware entry point: a syntax error becomes a Diagnostic carrying
// the file and its 1-based line, while parseable text is inspected by
// DiagnoseCommands. No file is read.
func DiagnoseCommandText(text, path string) []Diagnostic {
	text = strings.TrimPrefix(text, "\ufeff")
	if frontmatter, _, ok := splitFrontmatter(text); ok {
		if _, err := parseFieldsStrict(frontmatter); err != nil {
			return []Diagnostic{{
				Kind: DiagnosticInvalidFrontmatter, Path: path,
				Line: err.line, Message: err.msg,
			}}
		}
	}
	cmd, err := ParseStrict(text, path)
	if err != nil {
		// Defensive: the checks above accepted the text, so this cannot happen;
		// keep the result a diagnostic rather than a half-parsed command.
		return []Diagnostic{{
			Kind: DiagnosticInvalidFrontmatter, Path: path, Message: err.Error(),
		}}
	}
	return DiagnoseCommands([]Command{cmd})
}

func diagnosticPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return "<unknown>"
	}
	return path
}

// pathKey normalizes a path for duplicate-source comparison. On Windows the
// filesystem is case-insensitive, so case alone must not hide a duplicate.
func pathKey(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	key := filepath.Clean(path)
	if filepath.Separator == '\\' {
		key = strings.ToLower(key)
	}
	return key
}

func validCommandName(name string) bool {
	segments := strings.Split(name, ":")
	if len(segments) > 2 {
		return false
	}
	for _, segment := range segments {
		if !validNameSegment(segment) {
			return false
		}
	}
	return true
}

func validNameSegment(segment string) bool {
	if segment == "" {
		return false
	}
	for _, r := range segment {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

// highestPosition mirrors the token scan in substituteArgs so diagnostics and
// expansion agree about which $-placeholders are real. It expands nothing: a
// diagnostic is only a read-only observation.
func highestPosition(body string) int {
	highest := 0
	for i := 0; i < len(body); {
		end, index, named, ok := dollarToken(body, i)
		if !ok {
			i++
			continue
		}
		if !named && index > highest {
			highest = index
		}
		i = end
	}
	return highest
}
