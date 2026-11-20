package tui

import (
	"os/exec"
	"runtime"
	"unicode"
)

// editorLookPath is the PATH resolver used on Windows to turn a bare command
// name into a full path, so .cmd/.bat shims (e.g. code -> code.cmd) launch
// correctly. It is a package variable, mirroring clipboardWrite in keys.go, so
// tests can swap in a deterministic resolver; production uses exec.LookPath.
var editorLookPath = exec.LookPath

// ResolveExternalEditor resolves the external editor command from the VISUAL
// and EDITOR environment variables. VISUAL takes precedence over EDITOR, and a
// raw value such as "code --wait" is split into a command plus its arguments.
//
// The env lookup is injected (rather than calling os.Getenv) so this is a pure
// query: callers provide the environment and tests never touch the real one.
// On Windows the command is additionally resolved through PATH; a bare name is
// kept unchanged when resolution fails, matching the shim behaviour of the
// original implementation.
//
// It returns ok=false when neither variable is set or the chosen value has no
// command token.
func ResolveExternalEditor(lookup func(string) (string, bool)) (cmd string, args []string, ok bool) {
	if lookup == nil {
		return "", nil, false
	}
	raw, found := lookup("VISUAL")
	if !found {
		raw, found = lookup("EDITOR")
	}
	if !found {
		return "", nil, false
	}
	parts := splitEditorCommand(raw)
	if len(parts) == 0 {
		return "", nil, false
	}
	cmd = parts[0]
	if len(parts) > 1 {
		args = parts[1:]
	}
	if runtime.GOOS == "windows" {
		if resolved, err := editorLookPath(cmd); err == nil && resolved != "" {
			cmd = resolved
		}
	}
	return cmd, args, true
}

// splitEditorCommand splits an editor command string into fields, honoring
// single and double quotes so paths containing spaces stay in one field. A
// backslash escapes only a following quote or backslash, so ordinary Windows
// path separators survive untouched.
func splitEditorCommand(raw string) []string {
	rs := []rune(raw)
	var fields []string
	var cur []rune
	var quote rune
	flush := func() {
		if len(cur) > 0 {
			fields = append(fields, string(cur))
			cur = nil
		}
	}
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if r == '\\' && i+1 < len(rs) {
			next := rs[i+1]
			if next == quote || next == '\\' || (quote == 0 && (next == '"' || next == '\'')) {
				i++
				cur = append(cur, next)
				continue
			}
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				cur = append(cur, r)
			}
			continue
		}
		switch {
		case r == '\'' || r == '"':
			quote = r
		case unicode.IsSpace(r):
			flush()
		default:
			cur = append(cur, r)
		}
	}
	flush()
	return fields
}
