package security

import (
	"regexp"
	"strings"
)

// maxShellLiteralDepth bounds recursive classification of nested shell -c
// payloads. Each recursion consumed at least the wrapper text, so termination
// is already guaranteed; the cap additionally keeps hostile deeply-nested
// input cheap to classify.
const maxShellLiteralDepth = 8

// shellExecRe matches a shell executable (optionally with a relative or
// absolute path) at a *command position*. It is run against the mask produced
// by maskCommandPositions, where quoted strings, backtick substitutions,
// comments and heredoc bodies have already been blanked. That is what keeps a
// shell name mentioned only inside a harmless string (e.g.
// `echo "try bash -c 'rm'"`) from becoming a candidate.
var shellExecRe = regexp.MustCompile(`(?i)(?:^|[\s;|&(])((?:/|[A-Za-z0-9_.-]+/)*(?:bash|dash|zsh|ksh|ash|sh))\b`)

// maskCommandPositions is sanitizeCommand with one deliberate difference:
// heredoc body and delimiter lines become runs of spaces of equal length
// instead of empty strings. Byte offsets in the mask therefore always line up
// with the original command, which lets shellLiteralPayloads copy the quoted
// payload verbatim out of the original command.
func maskCommandPositions(command string) string {
	lines := strings.Split(command, "\n")
	heredoc := ""
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if heredoc != "" {
			if strings.TrimSpace(line) == heredoc {
				heredoc = ""
			}
			out = append(out, strings.Repeat(" ", len(line)))
			continue
		}
		clean, delim := sanitizeLine(line)
		heredoc = delim
		out = append(out, clean)
	}
	return strings.Join(out, "\n")
}

// shellLiteralPayloads returns the literal, quoted command payload of every
// `bash -c`, `sh -c`, `bash -lc`, ... invocation found at a command position.
// It is a pure *extractor*: it never decides what is dangerous, it only hands
// the recovered literal back to the caller, which re-runs the existing
// classifiers on it. This closes the gap where sanitizeCommand blanks the
// quoted payload before FindFileEditingCommand/DetectThreat ever see it.
//
// Only a real quoted payload is returned. An unquoted -c argument is not a
// command string in shell grammar (it becomes $0 and the following words are
// positional parameters), so skipping it avoids false positives on commands
// such as `git status bash -c rm` while an unterminated quote is recovered on a
// best-effort basis (fail-safe: still inspect whatever text is present).
func shellLiteralPayloads(command string) []string {
	mask := maskCommandPositions(command)
	var out []string
	for _, loc := range shellExecRe.FindAllStringSubmatchIndex(mask, -1) {
		// loc[2]:loc[3] is the shell executable token; scanning for the -c flag
		// starts immediately after it.
		flagEnd, ok := findShellCFlagEnd(mask, loc[3])
		if !ok {
			continue
		}
		payload, ok := extractQuotedPayload(command, flagEnd)
		if ok && strings.TrimSpace(payload) != "" {
			out = append(out, payload)
		}
	}
	return out
}

// findShellCFlagEnd scans option tokens after a shell executable and returns
// the byte offset just past a short option cluster containing 'c' (-c, -lc,
// -cl, -xc, ...). Long options (--noprofile) are skipped as well, but the first
// non-option token ends the scan: only option text can carry -c.
func findShellCFlagEnd(mask string, p int) (int, bool) {
	for p < len(mask) {
		for p < len(mask) && isShellBlank(mask[p]) {
			p++
		}
		if p >= len(mask) || mask[p] != '-' {
			return 0, false
		}
		start := p
		for p < len(mask) && !isShellBlank(mask[p]) && !isShellSeparator(mask[p]) {
			p++
		}
		token := mask[start:p]
		if len(token) >= 2 && token[1] != '-' && strings.Contains(strings.ToLower(token), "c") {
			return p, true
		}
	}
	return 0, false
}

// extractQuotedPayload reads the single shell word starting at or after p,
// which must open with a quote. An unterminated quote is recovered up to a
// newline or end of input rather than discarded, so a truncated hostile payload
// is still classified (fail-safe).
func extractQuotedPayload(command string, p int) (string, bool) {
	for p < len(command) && isShellBlank(command[p]) {
		p++
	}
	if p >= len(command) || (command[p] != '\'' && command[p] != '"') {
		return "", false
	}
	return readShellWord(command, p)
}

// readShellWord follows POSIX quote concatenation and backslash escapes
// so a hostile payload cannot hide a second quoted
// fragment from the classifier by splitting it across adjacent segments. The
// word ends at the first unquoted whitespace or shell separator.
func readShellWord(s string, start int) (string, bool) {
	var b strings.Builder
	i := start
	for i < len(s) {
		switch s[i] {
		case '\'', '"':
			quote := s[i]
			i++
			for i < len(s) {
				c := s[i]
				if c == '\\' && quote == '"' && i+1 < len(s) {
					b.WriteByte(c)
					b.WriteByte(s[i+1])
					i += 2
					continue
				}
				if c == quote {
					i++
					break
				}
				if c == '\n' || c == '\r' {
					return b.String(), true // unterminated: fail-safe
				}
				b.WriteByte(c)
				i++
			}
			if i >= len(s) {
				return b.String(), true
			}
			// Continue the same word only for directly adjacent fragments;
			// whitespace and separators end the -c argument.
			switch {
			case s[i] == '\'' || s[i] == '"':
				continue
			case s[i] == '\\' && i+1 < len(s) && s[i+1] == '\'':
				b.WriteByte('\'')
				i += 2
				continue
			default:
				return b.String(), true
			}
		case '\\':
			if i+1 < len(s) {
				b.WriteByte(s[i+1])
				i += 2
			} else {
				i++
			}
		default:
			return b.String(), true
		}
	}
	return b.String(), true
}

func isShellBlank(c byte) bool { return c == ' ' || c == '\t' || c == '\r' }

func isShellSeparator(c byte) bool {
	switch c {
	case ';', '&', '|', '(', ')', '\n':
		return true
	}
	return false
}
