package skills

// Skill argument-hint completion metadata.
//
// SkillArgumentHints turns the declarative `argument-hint` frontmatter of a set
// of skills into enumerated candidate values keyed by skill name. A frontend
// (for example a slash command that invokes a skill) can use the map to offer
// live argument suggestions without parsing frontmatter or duplicating the
// package's hint grammar. The helper is pure: it reads only its argument, never
// touches the filesystem and never mutates the skills it is given.
//
// The hint grammar mirrors slash-command argument hints. A whitespace-delimited
// token containing '|' is an enumerated choice, e.g. `dev|stage|prod` or
// `[dev|stage|prod]`; one optional <...> or [...] wrapper is stripped first.
// Plain placeholders such as `<file>` describe an open-ended value and yield
// no candidates. Full-width ASCII (including the full-width '|' and brackets)
// is folded to half-width so hints authored through a Chinese IME are read the
// same way as their ASCII spelling.

import "strings"

// SkillArgumentHints returns the enumerated argument candidates of every skill
// that declares at least one, keyed by skill name.
//
// Rules:
//
//   - Only tokens that, after stripping one optional <...> or [...] wrapper,
//     contain '|' contribute; a group needs two or more distinct values to
//     count as an enumeration.
//   - Empty alternatives and duplicates are dropped; order follows the hint.
//   - Skills with no hint, or with only open placeholders, get no entry, so a
//     lookup for them returns a nil slice.
//   - Duplicate names: the last occurrence wins (ordinary map semantics).
//   - A nil or empty input yields an empty, non-nil map.
func SkillArgumentHints(skills []Skill) map[string][]string {
	out := make(map[string][]string, len(skills))
	for _, skill := range skills {
		candidates := argumentHintCandidates(skill.ArgumentHint)
		if len(candidates) == 0 {
			continue
		}
		out[skill.Name] = candidates
	}
	return out
}

// argumentHintCandidates extracts the enumerated values from one argument-hint.
func argumentHintCandidates(hint string) []string {
	var out []string
	for _, raw := range strings.Fields(hint) {
		token := foldHintWidth(raw)
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

// foldHintWidth folds full-width ASCII and the ideographic space into their
// half-width forms, mirroring slash-command completion so a hint typed through
// a Chinese IME is recognised. CJK characters themselves are left untouched.
func foldHintWidth(s string) string {
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
