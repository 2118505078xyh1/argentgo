package skills

import (
	"strings"
	"unicode/utf8"
)

// MaxShellCommands caps how many distinct shell commands a single skill body
// may expose. Extra unique commands past this limit are ignored, which bounds
// both the approval queue and the work a caller can be tricked into doing.
const MaxShellCommands = 32

// ShellKillSwitchEnv is the environment variable a caller checks to disable
// skill shell injection outright. This package never reads the environment;
// the gate is the pure ShellInjectAllowed function below.
const ShellKillSwitchEnv = "KILO_DISABLE_SKILL_SHELL"

// Span is a half-open byte range [Start, End) into the skill body that was
// handed to ParseShellCommands. It covers the whole executable token, i.e. the
// opening backtick, the leading '!', the command, and the closing backtick.
// Byte offsets (not rune offsets) match Go's string slicing and regexp indices.
type Span struct {
	Start int
	End   int
}

// ParseShellCommands scans a skill body once and returns the shell commands
// written in executable position plus the span each one occupied.
//
// The only executable form is an inline code span whose first character is
// '!': `!git status` yields "git status". Inert regions are skipped entirely:
//
//   - fenced code blocks (``` ... ```), including their opening and closing
//     markers;
//   - ordinary inline code (`echo hi`) that does not start with '!';
//   - backslashed backticks (\`), which are escaped and never delimit a span.
//
// Parsing is deliberately conservative: a stray fence or an unterminated
// inline span only ever suppresses a command, never invents one. Scanning is
// rune-aware (so CJK content cannot corrupt offsets) and this function never
// executes anything. Duplicate commands are dropped, keeping the first
// occurrence, and at most MaxShellCommands unique commands are returned.
func ParseShellCommands(body string) (commands []string, spans []Span) {
	seen := make(map[string]bool)
	for i := 0; i < len(body); {
		r, size := utf8.DecodeRuneInString(body[i:])
		switch {
		case r == '\\':
			// An escaped rune is literal: skip the backslash and its target so
			// an escaped backtick cannot open an inline span.
			i += size
			if i < len(body) {
				_, next := utf8.DecodeRuneInString(body[i:])
				i += next
			}
		case r == '`':
			if strings.HasPrefix(body[i:], "```") {
				// Fenced block: inert until the matching closing fence (or EOF).
				i += 3
				for i < len(body) {
					if strings.HasPrefix(body[i:], "```") {
						i += 3
						break
					}
					_, s := utf8.DecodeRuneInString(body[i:])
					i += s
				}
				continue
			}

			open := i
			contentStart := i + 1
			executable := contentStart < len(body) && body[contentStart] == '!'
			if executable {
				contentStart++ // the '!' is a marker, not part of the command
			}
			close, ok := findClosingBacktick(body, i+1)
			if !ok {
				// No closing backtick: treat the opening one as literal text.
				i += size
				continue
			}
			if executable {
				cmd := strings.TrimSpace(body[contentStart:close])
				if cmd != "" && !seen[cmd] && len(commands) < MaxShellCommands {
					seen[cmd] = true
					commands = append(commands, cmd)
					spans = append(spans, Span{Start: open, End: close + 1})
				}
			}
			i = close + 1
		default:
			i += size
		}
	}
	return commands, spans
}

// findClosingBacktick returns the byte index of the first unescaped backtick
// at or after from. Escaped backticks (\`) are not delimiters.
func findClosingBacktick(body string, from int) (int, bool) {
	for j := from; j < len(body); {
		switch body[j] {
		case '\\':
			j++
			if j < len(body) {
				_, size := utf8.DecodeRuneInString(body[j:])
				j += size
			}
		case '`':
			return j, true
		default:
			_, size := utf8.DecodeRuneInString(body[j:])
			j += size
		}
	}
	return 0, false
}

// ShellInjectAllowed is the gate for acting on parsed commands. Commands may
// only be surfaced for execution when the skill is trusted and the kill-switch
// has not been set. It is pure: reading KILO_DISABLE_SKILL_SHELL and deciding
// trust are the caller's responsibilities.
func ShellInjectAllowed(skillTrusted, killSwitch bool) bool {
	return skillTrusted && !killSwitch
}
