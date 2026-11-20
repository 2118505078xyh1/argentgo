package security

import (
	"encoding/json"
	"os"
	"strings"
	"unicode"
)

// FindFileEditingCommand returns the mutating shell token in command, or "" if
// none was found. It is a token-aware classifier in the spirit of Cline's
// command guard: quoted strings, backtick substitutions, heredoc bodies and
// shell comments are masked before tokenizing, so a literal "rm" inside a
// string does not trigger a false positive.
//
// The function is pure: it has no side effects and keeps no global state. It is
// deliberately conservative — a mutating command may slip through, but reading
// commands (ls, cat, echo, git status, go test, rg, find, type, Get-Content)
// must not be blocked. The returned value is the offending token as it appeared
// in the command (for example "rm" or ">/etc/passwd").
func FindFileEditingCommand(command string) string {
	if strings.TrimSpace(command) == "" {
		return ""
	}

	// POSIX file/shell mutators plus cmd.exe and PowerShell equivalents. A token
	// is compared case-insensitively because PowerShell cmdlets and aliases are.
	mutating := map[string]bool{
		// POSIX.
		"rm": true, "rmdir": true, "mv": true, "cp": true, "sed": true,
		"tee": true, "dd": true, "chmod": true, "chown": true, "chgrp": true,
		"truncate": true, "touch": true, "mkdir": true, "install": true,
		"ln": true, "unlink": true, "shred": true, "split": true, "patch": true,
		// cmd.exe.
		"del": true, "erase": true, "move": true, "copy": true, "xcopy": true,
		"robocopy": true, "ren": true, "rename": true, "md": true, "rd": true,
		// PowerShell cmdlets and common aliases.
		"remove-item": true, "set-content": true, "out-file": true,
		"add-content": true, "clear-content": true, "move-item": true,
		"copy-item": true, "new-item": true, "set-item": true,
		"rename-item": true, "remove-itemproperty": true, "set-itemproperty": true,
		"new-itemproperty": true, "compress-archive": true, "expand-archive": true,
		"ri": true, "ni": true, "si": true, "rni": true, "clc": true,
	}

	// Tools whose second token decides whether the call mutates. Read-only
	// subcommands (git status, go test, go vet, go list, cargo check, npm view,
	// pip freeze, ...) are intentionally absent.
	subcommands := map[string]map[string]bool{
		"git": {
			"push": true, "commit": true, "checkout": true, "reset": true,
			"clean": true, "rebase": true, "merge": true, "stash": true,
			"add": true, "rm": true, "mv": true, "restore": true, "revert": true,
			"cherry-pick": true, "apply": true, "am": true, "fetch": true, "pull": true,
		},
		"go": {
			"build": true, "install": true, "get": true, "mod": true,
			"fmt": true, "generate": true, "clean": true,
		},
		"npm": {
			"install": true, "i": true, "ci": true, "add": true, "remove": true,
			"rm": true, "uninstall": true, "update": true, "publish": true,
			"pack": true,
		},
		"pip":  {"install": true, "uninstall": true},
		"pip3": {"install": true, "uninstall": true},
		"cargo": {
			"build": true, "install": true, "add": true, "remove": true,
			"update": true, "publish": true, "fmt": true, "clean": true,
		},
	}

	cleaned := sanitizeCommand(command)
	for _, segment := range splitOnSeparators(cleaned) {
		fields := strings.Fields(segment)
		if len(fields) == 0 {
			continue
		}
		cmd := strings.ToLower(fields[0])
		if mutating[cmd] {
			return fields[0]
		}
		if subs, ok := subcommands[cmd]; ok && len(fields) > 1 {
			sub := strings.ToLower(fields[1])
			if subs[sub] {
				return fields[0] + " " + fields[1]
			}
		}
		for i, field := range fields {
			idx := strings.LastIndex(field, ">")
			if idx < 0 {
				continue
			}
			target := strings.TrimLeft(field[idx+1:], ">")
			if target == "" && i+1 < len(fields) {
				target = fields[i+1]
			}
			if target == "" || strings.HasPrefix(target, "&") {
				continue // 2>&1 and friends are not file writes
			}
			lower := strings.ToLower(strings.Trim(target, "\"'"))
			if lower == "null" || lower == "/dev/null" || lower == "nul" || lower == "nul:" {
				continue
			}
			if !isTempPath(target) {
				return ">" + target
			}
		}
	}
	return ""
}

// guardShellCommand extracts the command string from a shell tool's JSON
// arguments. Only bash and powershell are shell tools; everything else is
// ignored. A malformed payload yields no match (false negatives are safe).
func guardShellCommand(toolName string, args json.RawMessage) string {
	name := strings.ToLower(strings.TrimSpace(toolName))
	if name != "bash" && name != "powershell" {
		return ""
	}
	var payload struct {
		Command string `json:"command"`
		Script  string `json:"script"`
	}
	if err := json.Unmarshal(args, &payload); err != nil {
		return ""
	}
	command := payload.Command
	if strings.TrimSpace(command) == "" {
		command = payload.Script
	}
	return FindFileEditingCommand(command)
}

// sanitizeCommand masks heredoc bodies, then calls sanitizeLine on each line.
func sanitizeCommand(command string) string {
	lines := strings.Split(command, "\n")
	heredoc := ""
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if heredoc != "" {
			// A line containing only the delimiter closes the heredoc. Both the
			// body and the delimiter line are replaced with blanks so no token
			// inside them can be mistaken for a command.
			if strings.TrimSpace(line) == heredoc {
				heredoc = ""
			}
			out = append(out, "")
			continue
		}
		clean, delim := sanitizeLine(line)
		heredoc = delim
		out = append(out, clean)
	}
	return strings.Join(out, "\n")
}

// sanitizeLine blanks quoted strings, backtick substitutions, shell comments
// and the marker of a heredoc opener. Non-ignored characters (including shell
// separators) are preserved so the caller can still split commands apart.
func sanitizeLine(line string) (string, string) {
	var b strings.Builder
	var heredoc string
	inSingle, inDouble, inBacktick := false, false, false
	escaped := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if escaped {
			b.WriteByte(' ')
			escaped = false
			continue
		}
		if c == '\\' && !inSingle {
			escaped = true
			b.WriteByte(' ')
			continue
		}
		if inSingle {
			if c == '\'' {
				inSingle = false
			}
			b.WriteByte(' ')
			continue
		}
		if inDouble {
			if c == '"' {
				inDouble = false
			}
			b.WriteByte(' ')
			continue
		}
		if inBacktick {
			if c == '`' {
				inBacktick = false
			}
			b.WriteByte(' ')
			continue
		}
		switch c {
		case '\'':
			inSingle = true
			b.WriteByte(' ')
			continue
		case '"':
			inDouble = true
			b.WriteByte(' ')
			continue
		case '`':
			inBacktick = true
			b.WriteByte(' ')
			continue
		case '#':
			if i == 0 || isSeparatorByte(line[i-1]) {
				for ; i < len(line); i++ {
					b.WriteByte(' ')
				}
				return b.String(), heredoc
			}
		}
		if c == '<' && i+1 < len(line) && line[i+1] == '<' {
			j := i + 2
			if j < len(line) && line[j] == '-' {
				j++
			}
			for j < len(line) && (line[j] == '\'' || line[j] == '"') {
				j++
			}
			start := j
			for j < len(line) && isDelimiterByte(line[j]) {
				j++
			}
			if j > start {
				heredoc = line[start:j]
			}
			for k := i; k < j; k++ {
				b.WriteByte(' ')
			}
			i = j - 1
			continue
		}
		b.WriteByte(c)
	}
	return b.String(), heredoc
}

// splitOnSeparators splits a shell program into individual commands. The
// separators themselves are discarded; `;` && `||` `|` `&` parentheses and
// newlines all end a command. Redirection `>` is NOT a separator.
func splitOnSeparators(s string) []string {
	out := []string{}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ';', '&', '|', '(', ')', '\n', '\r':
			out = append(out, b.String())
			b.Reset()
		default:
			b.WriteByte(s[i])
		}
	}
	return append(out, b.String())
}

func isDelimiterByte(b byte) bool {
	return b == '_' || b == '-' || b == '.' ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9')
}

func isSeparatorByte(b byte) bool {
	switch b {
	case ';', '&', '|', '(', ')', '<', '>', '\t', '\r':
		return true
	}
	return unicode.IsSpace(rune(b))
}

// isTempPath reports whether path points into a scratch directory whose writes
// are harmless for the purpose of this guard. os.TempDir is an environment
// read, not a side effect, so the classifier stays free of global state.
func isTempPath(path string) bool {
	p := strings.ReplaceAll(strings.Trim(strings.TrimSpace(path), "\"'"), "\\", "/")
	lower := strings.ToLower(p)
	roots := []string{
		strings.ToLower(strings.ReplaceAll(os.TempDir(), "\\", "/")),
		"/tmp", "/var/tmp", "%temp%", "c:/windows/temp",
	}
	for _, root := range roots {
		root = strings.TrimSuffix(strings.ToLower(root), "/")
		if root == "" {
			continue
		}
		if lower == root || strings.HasPrefix(lower, root+"/") {
			return true
		}
	}
	return false
}
