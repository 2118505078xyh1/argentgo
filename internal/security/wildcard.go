package security

import (
	"path"
	"strings"
)

// WildcardMatch reports whether subject matches a permission-rule pattern.
//
// Matching uses path.Match semantics: '*' matches any run of non-separator
// characters, '?' matches one non-separator, and '[abc]' is a character class.
// As a convenience, a pattern ending in '*' also matches across separators by
// prefix comparison, so "read*" permits both "read" and "read/file.txt".
// Malformed patterns never match.
func WildcardMatch(pattern, subject string) bool {
	// path.Match always uses '/' as the separator and treats '\' as an escape,
	// even on Windows where '\' *is* the separator. Normalise both sides to '/'
	// (the same cross-platform normalisation GlobMatch applies) so a rule and a
	// path written with Windows separators are compared structurally instead of
	// having their '\' misread as escapes.
	p := strings.ReplaceAll(pattern, `\`, "/")
	s := strings.ReplaceAll(subject, `\`, "/")
	matched, err := path.Match(p, s)
	if err != nil {
		// A malformed pattern never matches - not even through the prefix
		// fallback below. Otherwise a typo like "[*" would still match
		// "[anything]" and silently turn a rule into an unintended allow.
		return false
	}
	if matched {
		return true
	}
	if strings.HasSuffix(p, "*") {
		prefix := strings.TrimRight(p, "*")
		if prefix == "" {
			return true // an all-star pattern permits any subject
		}
		return strings.HasPrefix(s, prefix)
	}
	return false
}

// WildcardRule is one entry of an ordered permission list. Effect is normally
// Allow or Deny; any other value (including Ask) is treated as Ask.
type WildcardRule struct {
	Pattern string
	Effect  Decision
}

// LastMatchDecision evaluates rules in order and returns the effect of the
// last matching rule. When no rule matches, Ask is returned so an unlisted
// subject never silently gains access.
func LastMatchDecision(rules []WildcardRule, subject string) Decision {
	result := Ask
	for _, rule := range rules {
		if WildcardMatch(rule.Pattern, subject) {
			result = rule.Effect
		}
	}
	return result
}

// DenyWinsDecision behaves like LastMatchDecision except that a matching deny
// rule wins over matching allow rules no matter what order they appear in.
// This is the safe default for permission lists where deny is absolute.
func DenyWinsDecision(rules []WildcardRule, subject string) Decision {
	result := Ask
	denied := false
	for _, rule := range rules {
		if !WildcardMatch(rule.Pattern, subject) {
			continue
		}
		if rule.Effect == Deny {
			denied = true
		}
		result = rule.Effect
	}
	if denied {
		return Deny
	}
	return result
}
