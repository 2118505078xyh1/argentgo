package skills

import (
	"path"
	"strings"
)

// Triggers holds optional automatic-activation rules for a skill.
//
// Each list is an independent filter and an empty list means "no restriction":
// a skill with zero triggers is eligible everywhere. Callers combine the
// filters that fit their event (a file path, a prompt, a model family) instead
// of relying on the model to call use_skill by name. Matching here is pure: no
// filesystem access and no dependencies beyond the standard library.
type Triggers struct {
	// Paths are gitignore-style globs matched against the path of a file the
	// agent is about to read or edit.
	Paths []string
	// Keywords are case-insensitive substrings matched against prompt text.
	Keywords []string
	// Models restricts the skill to these model families (e.g. "claude",
	// "gemini"). An empty list, "all" or "*" means every family.
	Models []string
}

// MatchPath reports whether p matches any of the gitignore-style globs in
// patterns.
//
// Syntax: `**` spans zero or more path segments, `*` stays inside one segment,
// and a pattern without a slash is tested against the basename at any depth.
// Patterns and p are normalised the same way: backslashes become slashes, a
// leading `./` or `/` is dropped and comparison is case-insensitive. One rule
// therefore works for both Windows and POSIX paths.
func MatchPath(patterns []string, p string) bool {
	segments := strings.Split(normalizeGlobPath(p), "/")
	for _, pattern := range patterns {
		glob := normalizeGlobPath(pattern)
		if glob == "" {
			continue
		}
		if !strings.Contains(glob, "/") {
			glob = "**/" + glob
		}
		if matchSegments(strings.Split(glob, "/"), segments, 0, 0) {
			return true
		}
	}
	return false
}

// matchSegments walks pattern and segments together. A `**` pattern segment
// consumes zero or more path segments; every other segment is handed to
// path.Match, which owns the `*` / `?` / `[...]` wildcard syntax.
func matchSegments(pattern, segments []string, pi, si int) bool {
	for pi < len(pattern) {
		if pattern[pi] == "**" {
			for next := si; next <= len(segments); next++ {
				if matchSegments(pattern, segments, pi+1, next) {
					return true
				}
			}
			return false
		}
		if si >= len(segments) {
			return false
		}
		ok, err := path.Match(pattern[pi], segments[si])
		if err != nil || !ok {
			return false
		}
		pi++
		si++
	}
	return si == len(segments)
}

// normalizeGlobPath makes a pattern or a concrete path comparable: one
// separator, no leading marker and one case. Lower-casing keeps Windows-style
// case-insensitive paths working without a build tag or a runtime check.
func normalizeGlobPath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.ReplaceAll(p, `\`, "/")
	p = strings.TrimPrefix(p, "./")
	p = strings.Trim(p, "/")
	return strings.ToLower(p)
}

// SkillsForPath returns the skills eligible for a file path: those with no
// path triggers plus those whose path triggers match p. Registry order is kept.
func SkillsForPath(skills []Skill, p string) []Skill {
	var out []Skill
	for _, skill := range skills {
		if len(skill.Triggers.Paths) == 0 || MatchPath(skill.Triggers.Paths, p) {
			out = append(out, skill)
		}
	}
	return out
}

// MatchKeywords reports whether text contains any of the case-insensitive
// keywords. Substring matching keeps CJK keywords working, where word-based
// tokenisation would drop them.
func MatchKeywords(keywords []string, text string) bool {
	if len(keywords) == 0 {
		return false
	}
	lower := strings.ToLower(text)
	for _, keyword := range keywords {
		keyword = strings.ToLower(strings.TrimSpace(keyword))
		if keyword != "" && strings.Contains(lower, keyword) {
			return true
		}
	}
	return false
}

// SkillsForKeywords returns the skills eligible for prompt text: those with no
// keyword triggers plus those with a keyword hit. Registry order is kept.
func SkillsForKeywords(skills []Skill, text string) []Skill {
	var out []Skill
	for _, skill := range skills {
		if len(skill.Triggers.Keywords) == 0 || MatchKeywords(skill.Triggers.Keywords, text) {
			out = append(out, skill)
		}
	}
	return out
}

// SkillsForModel returns the skills available to a model family. family is
// compared case-insensitively; skills with no model restriction (or with
// "all" / "*") are available to every family. Registry order is kept.
func SkillsForModel(skills []Skill, family string) []Skill {
	family = strings.ToLower(strings.TrimSpace(family))
	var out []Skill
	for _, skill := range skills {
		if len(skill.Triggers.Models) == 0 {
			out = append(out, skill)
			continue
		}
		for _, model := range skill.Triggers.Models {
			model = strings.ToLower(strings.TrimSpace(model))
			if model == family || model == "*" || model == "all" {
				out = append(out, skill)
				break
			}
		}
	}
	return out
}
