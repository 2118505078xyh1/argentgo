package commands

// Completion coordinator: aider-style three-level fallback over a flat pool of
// available strings.
//
// CompleteCommands and CompleteArgument are deliberately single-purpose pure
// matchers (command names on one side, enumerated argument values on the
// other). Complete ties them together with the fallback order aider uses in
// its input widget, so a frontend needs one call instead of reimplementing the
// normalization and the "which list applies right now" decision in every
// language binding. Nothing here reads files, spawns processes or mutates the
// caller's slices.

import (
	"sort"
	"strings"
)

// Complete returns the completion candidates for input drawn from available.
//
// The three levels are tried in order; the first level that produces a match
// wins, so an ambiguous prefix yields the whole list of matches (the
// "ambiguity list" a dropdown needs to render). Levels:
//
//  1. Command names — when input starts with '/', the available strings are
//     treated as slash-command names and matched with CompleteCommands. That
//     reuses its leading-slash handling, case/width folding, CJK-safe rune
//     matching and alias rules; as in CompleteCommands, a partly typed
//     argument ("/rev --all") does not suppress the name matches.
//  2. Command-specific candidates — the trailing token (or the token after a
//     command, as in "/deploy st") is matched against available through
//     CompleteArgument's enumerated-value rules. A trailing space after a
//     complete slash command ("/deploy ") yields an empty prefix, i.e. every
//     candidate.
//  3. Identifiers — anything CompleteArgument cannot represent (values holding
//     whitespace or '|', or a single candidate) is matched as a plain, CJK-safe
//     prefix. This is the aider fallback for file names and chat identifiers.
//
// Matching is case-insensitive and width-folding (full-width ASCII and the
// ideographic space behave like their half-width forms), so Chinese IME input
// works at every level. Results are deduplicated and stably sorted by their
// folded, lower-cased value with the original string as tie-breaker; a nil or
// empty input lists everything in available. The returned slice is fresh and
// never aliases available.
func Complete(input string, available []string) []string {
	folded := foldWidth(input)
	trimmed := strings.TrimSpace(folded)
	trailingSpace := strings.TrimRight(folded, " \t\r\n") != folded

	if trimmed == "" {
		return sortedUnique(available)
	}

	current := lastToken(trimmed)
	if trailingSpace {
		current = ""
	}

	if strings.HasPrefix(trimmed, "/") {
		// Level 1: CompleteCommands ignores anything after the first token, so
		// a partly typed argument ("/rev --all") still completes the name.
		if matched := completeCommandNames(available, trimmed); len(matched) > 0 {
			return matched
		}
	}

	if matched := completeArgumentNames(current, available); len(matched) > 0 {
		return matched // Level 2: enumerated command-specific candidates.
	}
	return completeIdentifiers(current, available) // Level 3: identifiers.
}

// completeCommandNames adapts the flat candidate pool to CompleteCommands by
// wrapping each string as a command name, then maps the matches back to names.
func completeCommandNames(available []string, prefix string) []string {
	cmds := make([]Command, 0, len(available))
	for _, value := range available {
		if value != "" {
			cmds = append(cmds, Command{Name: value})
		}
	}
	matched := CompleteCommands(cmds, prefix)
	out := make([]string, 0, len(matched))
	for _, cmd := range matched {
		out = append(out, cmd.Name)
	}
	return sortedUnique(out)
}

// completeArgumentNames adapts the flat pool to CompleteArgument by encoding
// it as one enumerated argument-hint group. A value containing whitespace or
// '|' cannot round-trip through a hint token, so such values are left to the
// identifier level. A group needs at least two distinct values to count as an
// enumeration; a single candidate is likewise handled by the identifier level.
func completeArgumentNames(prefix string, available []string) []string {
	simple := make([]string, 0, len(available))
	for _, value := range available {
		if value == "" || strings.ContainsAny(value, " \t\r\n|") {
			continue
		}
		simple = append(simple, value)
	}
	if len(simple) < 2 {
		return nil
	}
	hint := "[" + strings.Join(simple, "|") + "]"
	return sortedUnique(CompleteArgument(Command{ArgumentHint: hint}, prefix))
}

// completeIdentifiers is level 3: a plain prefix match using the same folded,
// case-insensitive key as argument completion. It keeps '/' and embedded
// spaces, so paths and multi-word identifiers complete sensibly.
func completeIdentifiers(prefix string, available []string) []string {
	key := argumentKey(prefix)
	out := make([]string, 0, len(available))
	for _, value := range available {
		if value != "" && strings.HasPrefix(argumentKey(value), key) {
			out = append(out, value)
		}
	}
	return sortedUnique(out)
}

// lastToken returns the portion of s after the last whitespace rune.
func lastToken(s string) string {
	if i := strings.LastIndexAny(s, " \t\r\n"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// sortedUnique returns a fresh slice sorted by folded key (original string as
// tie-breaker) with exact duplicates removed. It never touches its input.
func sortedUnique(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := append([]string(nil), values...)
	sort.SliceStable(out, func(i, j int) bool {
		ki, kj := argumentKey(out[i]), argumentKey(out[j])
		if ki != kj {
			return ki < kj
		}
		return out[i] < out[j]
	})
	uniq := out[:0]
	for _, value := range out {
		if len(uniq) == 0 || uniq[len(uniq)-1] != value {
			uniq = append(uniq, value)
		}
	}
	return uniq
}
