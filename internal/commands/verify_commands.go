package commands

// Language-prefixed verify commands.
//
// Verification commands can arrive as a flat list of strings, following aider's
// --lint-cmd convention. Each entry is either a bare command or a command
// preceded by a language name and a colon:
//
//	"go: go vet ./..."
//	"python: flake8 --select=E"
//
// ParseVerifyCommands turns that list into a language -> argv map. It is pure:
// it reads nothing, spawns nothing and never mutates its input. The split is
// deliberately conservative so a colon that belongs to the command (http://,
// C:\tools) is never mistaken for a language separator.
//
// Argv slices come from strings.Fields, matching how config.VerifyCommands is
// parsed, so the values can be handed straight to the app verification allowlist.
// Quoting and shell metacharacters are therefore not interpreted here.

import (
	"fmt"
	"strings"
	"unicode"
)

// VerifyAllLanguages is the map key for a command with no language prefix.
// Aider applies such a command to every language; callers should use this key
// as the default when no language-specific entry matches.
const VerifyAllLanguages = "*"

// IssueKind classifies one verify-command parsing problem.
type IssueKind string

const (
	IssueEmptyCommand      IssueKind = "empty-command"
	IssueEmptyLanguage     IssueKind = "empty-language"
	IssueDuplicateLanguage IssueKind = "duplicate-language"
)

// Issue is one problem found while parsing verify-command arguments. Index is
// the 0-based position of Arg in the input slice; Message is a stable,
// human-readable explanation suitable for doctor output or UI warnings.
type Issue struct {
	Kind    IssueKind
	Index   int
	Arg     string
	Message string
}

// ParseVerifyCommands parses a flat list of verify-command arguments into a
// language -> argv map. Each argument is interpreted as:
//
//   - "lang: command" — a command for one language (or file extension). The
//     language is trimmed, lowercased, stripped of a leading dot and
//     canonicalized through the alias table below, so "go", "golang" and
//     ".go" all share the key "go".
//   - "command" — no language prefix; stored under VerifyAllLanguages.
//
// The separator is the first colon that is followed by whitespace (or ends the
// argument) and whose left side is a single language-like token. A colon
// followed by "/", "\" or any other non-space character stays part of the
// command, so "http://example" and "C:\tools\vet.exe" remain intact.
//
// Invalid entries are skipped (they produce no map entry) and described in the
// returned issues. When two entries normalize to the same language the later
// one wins and an IssueDuplicateLanguage is reported. The returned map is
// always non-nil; the argument slice is never modified.
func ParseVerifyCommands(args []string) (map[string][]string, []Issue) {
	out := make(map[string][]string, len(args))
	var issues []Issue

	for i, arg := range args {
		lang, cmdText, emptyLanguage := splitVerifyArg(arg)
		if emptyLanguage {
			issues = append(issues, Issue{
				Kind: IssueEmptyLanguage, Index: i, Arg: arg,
				Message: "verify command has a language separator but no language name",
			})
			continue
		}

		argv := strings.Fields(cmdText)
		if len(argv) == 0 {
			message := "verify command is empty"
			if lang != VerifyAllLanguages {
				message = fmt.Sprintf("verify command for language %q is empty", lang)
			}
			issues = append(issues, Issue{
				Kind: IssueEmptyCommand, Index: i, Arg: arg, Message: message,
			})
			continue
		}

		if _, exists := out[lang]; exists {
			issues = append(issues, Issue{
				Kind: IssueDuplicateLanguage, Index: i, Arg: arg,
				Message: fmt.Sprintf("verify command for language %q is defined more than once; keeping the last", lang),
			})
		}
		out[lang] = argv
	}
	return out, issues
}

// splitVerifyArg separates an optional language prefix from the command text.
// emptyLanguage is true only for a leading ":" (a malformed prefix form), which
// must not be silently reinterpreted as a bare command.
func splitVerifyArg(arg string) (lang, cmd string, emptyLanguage bool) {
	trimmed := strings.TrimSpace(arg)
	if trimmed == "" {
		return VerifyAllLanguages, "", false
	}
	if trimmed[0] == ':' {
		return "", "", true
	}
	if i := strings.IndexByte(trimmed, ':'); i >= 0 &&
		(i+1 == len(trimmed) || isVerifySpace(trimmed[i+1])) {
		prefix := strings.TrimSpace(trimmed[:i])
		if prefix != "" && validVerifyLangPrefix(prefix) {
			return canonicalVerifyLang(prefix), strings.TrimSpace(trimmed[i+1:]), false
		}
	}
	return VerifyAllLanguages, trimmed, false
}

func isVerifySpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\r' || b == '\n' || b == '\v' || b == '\f'
}

// validVerifyLangPrefix accepts a single language-like token: letters, digits
// and the punctuation used by real language names (".go", "c++", "c#",
// "objective-c", "node.js"). Spaces and quotes disqualify a prefix, which then
// makes the whole argument a bare command instead.
func validVerifyLangPrefix(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
		case r == '.', r == '_', r == '-', r == '+', r == '#':
		default:
			return false
		}
	}
	return true
}

// verifyLangAliases mirrors the alias groups used by app.verifyLangForms so a
// key parsed from the CLI normalizes the same way as a config map key. This is
// a local copy because importing app would invert the package dependency.
var verifyLangAliases = map[string]string{
	"golang": "go",
	"py":     "python",
	"js":     "javascript",
	"jsx":    "javascript",
	"mjs":    "javascript",
	"cjs":    "javascript",
	"ts":     "typescript",
	"tsx":    "typescript",
	"rs":     "rust",
	"sh":     "shell",
	"bash":   "shell",
	"rb":     "ruby",
	"c++":    "cpp",
	"cc":     "cpp",
	"cxx":    "cpp",
	"cs":     "csharp",
	"c#":     "csharp",
	"kt":     "kotlin",
	"md":     "markdown",
	"yml":    "yaml",
}

func canonicalVerifyLang(s string) string {
	key := strings.ToLower(strings.TrimSpace(s))
	key = strings.TrimPrefix(key, ".")
	if key == "" {
		return ""
	}
	if canonical, ok := verifyLangAliases[key]; ok {
		return canonical
	}
	return key
}
