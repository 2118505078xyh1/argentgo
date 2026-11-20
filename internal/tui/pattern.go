package tui

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// TermKind identifies how one term of a pattern is matched against a line.
// The kinds mirror fzf's query operators plus two structural values: TermInverse
// (a negated fuzzy term, written "!text") and TermOr (a group separator, written
// "|"). Positive terms inside one OR group are combined with AND.
type TermKind uint8

const (
	// TermFuzzy is an unprefixed term; the caller-supplied fuzzy callback decides.
	TermFuzzy TermKind = iota
	// TermExact is a substring match, written "'text".
	TermExact
	// TermPrefix is an anchored-at-start match, written "^text".
	TermPrefix
	// TermSuffix is an anchored-at-end match, written "text$".
	TermSuffix
	// TermEqual is a match with word boundaries on both sides, written "'text'".
	TermEqual
	// TermInverse is a negated fuzzy match, written "!text".
	TermInverse
	// TermOr separates AND groups; a line matches when any group matches.
	TermOr
)

// String makes test failures and debug output readable.
func (k TermKind) String() string {
	switch k {
	case TermFuzzy:
		return "fuzzy"
	case TermExact:
		return "exact"
	case TermPrefix:
		return "prefix"
	case TermSuffix:
		return "suffix"
	case TermEqual:
		return "equal"
	case TermInverse:
		return "inverse"
	case TermOr:
		return "or"
	default:
		return "unknown"
	}
}

// SearchTerm is one parsed token of a pattern query. CaseSensitive records the
// smart-case decision: true when Text contains an uppercase rune, false
// otherwise. Text is the unescaped pattern body without its operator prefix or
// suffix. A TermOr carries an empty Text.
type SearchTerm struct {
	Kind          TermKind
	Text          string
	CaseSensitive bool
}

// ParsePattern turns a query into a flat list of search terms. Whitespace
// separates AND terms, "|" separates OR groups, and a leading backslash escapes
// the next byte so operators and spaces can be searched literally. An empty (or
// whitespace-only) query returns an empty list; callers treat that as "match
// everything" via MatchPattern.
func ParsePattern(query string) []SearchTerm {
	tokens := tokenizePattern(query)
	terms := make([]SearchTerm, 0, len(tokens))
	for _, tok := range tokens {
		if tok == "|" {
			terms = append(terms, SearchTerm{Kind: TermOr})
			continue
		}
		terms = append(terms, parsePatternToken(tok))
	}
	return terms
}

// tokenizePattern splits on unescaped whitespace and on "|", while keeping
// backslashes so parsePatternToken can distinguish a real operator from an
// escaped literal. An escaped space ("\ ") therefore stays inside one token.
func tokenizePattern(query string) []string {
	var tokens []string
	var cur []byte
	flush := func() {
		if len(cur) > 0 {
			tokens = append(tokens, string(cur))
			cur = cur[:0]
		}
	}
	for i := 0; i < len(query); i++ {
		switch c := query[i]; {
		case c == '\\' && i+1 < len(query):
			cur = append(cur, c, query[i+1])
			i++
		case c == '|':
			flush()
			tokens = append(tokens, "|")
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			flush()
		default:
			cur = append(cur, c)
		}
	}
	flush()
	return tokens
}

// parsePatternToken interprets the leading and trailing operator bytes of a
// single token and applies smart-case to the resulting body.
func parsePatternToken(tok string) SearchTerm {
	term := SearchTerm{Kind: TermFuzzy}
	body := tok

	if len(body) > 0 && body[0] == '!' {
		term.Kind = TermInverse
		body = body[1:]
	} else if len(body) > 0 {
		switch body[0] {
		case '\'':
			term.Kind = TermExact
			body = body[1:]
			// A closing quote turns a substring match into a bounded one.
			if len(body) > 0 && body[len(body)-1] == '\'' && !escapedAt(body, len(body)-1) {
				term.Kind = TermEqual
				body = body[:len(body)-1]
			}
		case '^':
			term.Kind = TermPrefix
			body = body[1:]
		}
	}

	// "$" only acts as an anchor on an otherwise plain term, so "^x$" keeps the
	// "$" literal rather than silently changing its meaning.
	if term.Kind == TermFuzzy && len(body) > 0 && body[len(body)-1] == '$' && !escapedAt(body, len(body)-1) {
		term.Kind = TermSuffix
		body = body[:len(body)-1]
	}

	term.Text = unescapePattern(body)
	term.CaseSensitive = hasUpperRune(term.Text)
	return term
}

// escapedAt reports whether s[i] is preceded by an odd number of backslashes.
func escapedAt(s string, i int) bool {
	n := 0
	for j := i - 1; j >= 0 && s[j] == '\\'; j-- {
		n++
	}
	return n%2 == 1
}

// unescapePattern collapses each "\x" pair into a literal "x".
func unescapePattern(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func hasUpperRune(s string) bool {
	for _, r := range s {
		if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

// MatchPattern reports whether text satisfies the parsed terms. AND binds
// tighter than OR: the terms are split into groups at every TermOr, a line
// matches a group when every one of its terms matches (inverting TermInverse
// terms), and the group results are ORed. An empty term list matches
// everything, which is the natural result of an empty query.
//
// fuzzy is injected so this package never depends on the matcher implementation;
// it receives the (already case-normalised) pattern and text and returns the
// usual score, positions and match flag. A nil fuzzy falls back to a substring
// check, which keeps pure-pattern callers simple.
func MatchPattern(terms []SearchTerm, text string, fuzzy func(p, t string) (int, []int, bool)) bool {
	if len(terms) == 0 {
		return true
	}

	sawGroup := false
	start := 0
	for i := 0; i <= len(terms); i++ {
		if i != len(terms) && terms[i].Kind != TermOr {
			continue
		}
		if i > start {
			sawGroup = true
			if groupMatches(terms[start:i], text, fuzzy) {
				return true
			}
		}
		start = i + 1
	}
	// Only separators (e.g. a query of "|") — no real constraint to apply.
	if !sawGroup {
		return true
	}
	return false
}

func groupMatches(group []SearchTerm, text string, fuzzy func(p, t string) (int, []int, bool)) bool {
	for _, term := range group {
		matched := matchOne(term, text, fuzzy)
		if term.Kind == TermInverse {
			if matched {
				return false
			}
			continue
		}
		if !matched {
			return false
		}
	}
	return true
}

// matchOne returns the raw (non-inverted) result for a single positive term.
func matchOne(term SearchTerm, text string, fuzzy func(p, t string) (int, []int, bool)) bool {
	pattern, candidate := term.Text, text
	if !term.CaseSensitive {
		pattern = strings.ToLower(pattern)
		candidate = strings.ToLower(candidate)
	}

	switch term.Kind {
	case TermExact:
		return strings.Contains(candidate, pattern)
	case TermPrefix:
		return strings.HasPrefix(candidate, pattern)
	case TermSuffix:
		return strings.HasSuffix(candidate, pattern)
	case TermEqual:
		return boundedMatch(candidate, pattern)
	case TermFuzzy, TermInverse:
		if fuzzy != nil {
			_, _, ok := fuzzy(pattern, candidate)
			return ok
		}
		return strings.Contains(candidate, pattern)
	default: // TermOr and unknown kinds are neutral inside a group.
		return true
	}
}

// boundedMatch implements the "'word'" operator: pattern must occur as a whole
// word, i.e. the runes on both sides are not letters, digits or underscores.
func boundedMatch(text, pattern string) bool {
	if pattern == "" {
		return true
	}
	for i := 0; i+len(pattern) <= len(text); i++ {
		if text[i:i+len(pattern)] != pattern {
			continue
		}
		if i > 0 {
			before, _ := utf8.DecodeLastRuneInString(text[:i])
			if isWordRune(before) {
				continue
			}
		}
		after := i + len(pattern)
		if after < len(text) {
			r, _ := utf8.DecodeRuneInString(text[after:])
			if isWordRune(r) {
				continue
			}
		}
		return true
	}
	return false
}

func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}
