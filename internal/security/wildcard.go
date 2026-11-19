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
	matched, err := path.Match(pattern, subject)
	if err == nil && matched {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		prefix := strings.TrimRight(pattern, "*")
		if prefix == "" {
			return true // an all-star pattern permits any subject
		}
		return strings.HasPrefix(subject, prefix)
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
