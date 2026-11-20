package commands

// Command-name completion.
//
// CompleteCommands is a pure prefix matcher for slash commands: it never reads
// files, spawns processes or mutates its arguments. It exists so a UI can offer
// a live suggestion list without re-implementing the package's naming rules
// (namespaces, frontmatter aliases, CJK input) in every frontend.

import (
	"sort"
	"strings"
)

// CompleteCommands returns every command whose Name or Aliases begin with
// prefix. Matching rules:
//
//   - A leading '/' and surrounding whitespace are ignored, so a half-typed
//     slash input such as "/rev" works directly.
//   - Only the first whitespace-delimited token of prefix is considered; a
//     partially typed argument after the name does not suppress matches.
//   - Comparison is case-insensitive and rune-safe (multi-byte CJK names such
//     as 审查 match a 审 prefix). Full-width ASCII and the ideographic space
//     fold to their half-width forms, covering input from Chinese IMEs.
//   - Aliases declared in frontmatter match as well. For a plugin command an
//     alias also matches with the namespace, e.g. alias "re" of "acme:review"
//     is reachable as "acme:re".
//   - An empty prefix matches everything, which makes one function usable for
//     both the empty-query listing and incremental completion.
//
// The result is a fresh slice sorted by Name (then Path for stable ties) and
// never contains duplicates, even when a command matches via several names.
func CompleteCommands(cmds []Command, prefix string) []Command {
	key := completionKey(prefix)
	out := make([]Command, 0, len(cmds))
	for _, cmd := range cmds {
		if completionMatches(cmd, key) {
			out = append(out, cmd)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// Complete is the Set convenience wrapper around CompleteCommands.
func (s *Set) Complete(prefix string) []Command {
	return CompleteCommands(s.List(), prefix)
}

func completionMatches(cmd Command, key string) bool {
	if key == "" {
		return true
	}
	if strings.HasPrefix(completionKey(cmd.Name), key) {
		return true
	}
	for _, alias := range cmd.Aliases {
		if strings.HasPrefix(completionKey(alias), key) {
			return true
		}
		if cmd.Plugin != "" && strings.HasPrefix(completionKey(cmd.Plugin+":"+alias), key) {
			return true
		}
	}
	return false
}

// completionKey normalizes a user-typed prefix or a command name/alias into a
// single comparable form. Width folding is done rune by rune before case
// folding so a full-width Latin letter behaves exactly like its ASCII form.
func completionKey(s string) string {
	s = strings.TrimSpace(foldWidth(s))
	s = strings.TrimPrefix(s, "/")
	if i := strings.IndexAny(s, " \t\r\n"); i >= 0 {
		s = s[:i]
	}
	return strings.ToLower(s)
}

// foldWidth folds full-width ASCII and the ideographic space into their
// half-width forms. It is shared with argument completion so both kinds of
// input accept text typed through a Chinese IME.
func foldWidth(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\u3000': // ideographic space
			b.WriteRune(' ')
		case r >= '\uFF01' && r <= '\uFF5E': // full-width ASCII block
			b.WriteRune(r - 0xFEE0)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
