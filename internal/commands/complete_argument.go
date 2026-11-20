package commands

// Command-argument completion.
//
// CompleteArgument is a pure matcher for a single slash-command argument: it
// never reads files, spawns processes or mutates its arguments. Candidate
// values are derived from the command's frontmatter `argument-hint`. A hint
// token whose text (after stripping one optional <...> or [...] wrapper)
// contains a '|' is treated as an enumerated choice, e.g. `dev|stage|prod` or
// `[dev|stage|prod]`; each alternative is a candidate. Plain placeholders such
// as `<env>` or `[file]` describe an open-ended value and yield no candidates.

import "strings"

// CompleteArgument returns the candidate values from cmd's argument-hint that
// begin with argPrefix, in hint order. Matching is case-insensitive and
// rune-safe (full-width ASCII folds to half-width, so CJK IME input works) but
// the returned strings are the untouched candidates, ready for insertion.
//
// Rules:
//
//   - An empty argPrefix matches every candidate, so one function serves both
//     the empty-query listing and incremental completion.
//   - Whitespace-delimited hint tokens are inspected independently; a token
//     with a '|' contributes its alternatives, others are ignored.
//   - One optional <...> or [...] wrapper is stripped before splitting, so
//     both `[a|b]` and `a|b` are recognised.
//   - Empty alternatives and duplicates are dropped; a group with fewer than
//     two distinct values is not an enumeration and is ignored.
//   - A command with no hint, or a hint made only of open placeholders, has no
//     candidates and yields nil; a non-empty set of candidates with no match
//     for argPrefix also yields nil.
func CompleteArgument(cmd Command, argPrefix string) []string {
	candidates := argumentCandidates(cmd.ArgumentHint)
	if len(candidates) == 0 {
		return nil
	}
	key := argumentKey(argPrefix)
	var out []string
	for _, candidate := range candidates {
		if key == "" || strings.HasPrefix(argumentKey(candidate), key) {
			out = append(out, candidate)
		}
	}
	return out
}

// argumentKey normalizes a candidate or a user-typed prefix for comparison.
// Unlike completionKey it keeps '/' and embedded spaces: an argument may be a
// path or a multi-word value.
func argumentKey(s string) string {
	return strings.ToLower(strings.TrimSpace(foldWidth(s)))
}

// argumentCandidates extracts the enumerated values from an argument-hint.
func argumentCandidates(hint string) []string {
	var out []string
	for _, token := range strings.Fields(hint) {
		bare := token
		if len(bare) >= 2 {
			first, last := bare[0], bare[len(bare)-1]
			if (first == '<' && last == '>') || (first == '[' && last == ']') {
				bare = bare[1 : len(bare)-1]
			}
		}
		if !strings.Contains(bare, "|") {
			continue // a plain placeholder, not a choice
		}
		parts := strings.Split(bare, "|")
		group := make([]string, 0, len(parts))
		seen := make(map[string]bool, len(parts))
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part == "" || seen[part] {
				continue
			}
			seen[part] = true
			group = append(group, part)
		}
		if len(group) >= 2 {
			out = append(out, group...)
		}
	}
	return out
}
