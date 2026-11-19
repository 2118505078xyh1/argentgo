package security

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Redact replaces credential-shaped substrings with a placeholder. It is
// deliberately conservative: a false positive costs a redacted string, a
// false negative leaks a key.
func Redact(text string) string {
	var out strings.Builder
	out.Grow(len(text))
	rest := text
	for rest != "" {
		match := findSecret(rest)
		if match == nil {
			out.WriteString(rest)
			break
		}
		if match.end <= match.start || match.end > len(rest) {
			out.WriteString(rest)
			break
		}
		out.WriteString(rest[:match.start])
		out.WriteString(match.placeholder)
		rest = rest[match.end:]
	}
	return out.String()
}

type secretMatch struct {
	start       int
	end         int
	placeholder string
}

var tokenPrefixes = []struct {
	prefix string
	kind   string
}{
	{"sk-proj-", "api key"},
	{"sk-ant-", "api key"},
	{"sk-or-", "api key"},
	{"sk-", "api key"},
	{"ghp_", "token"},
	{"gho_", "token"},
	{"ghu_", "token"},
	{"ghs_", "token"},
	{"github_pat_", "token"},
	{"AKIA", "aws key"},
	{"ASIA", "aws key"},
	{"AIza", "google key"},
	{"xoxa-", "slack token"},
	{"xoxb-", "slack token"},
	{"xoxp-", "slack token"},
	{"xoxs-", "slack token"},
	{"r8_", "token"},
	{"hf_", "token"},
	{"glpat-", "token"},
	{"npm_", "token"},
	{"pypi-", "token"},
	{"eyJ", "jwt"},
}

var keyValueNames = []struct {
	needle string
	kind   string
}{
	{"api_key", "api key"},
	{"apikey", "api key"},
	{"api-key", "api key"},
	{"secret", "secret"},
	{"password", "password"},
	{"passwd", "password"},
	{"token", "token"},
	{"access_key", "access key"},
	{"private_key", "private key"},
}

var headerPrefixes = []struct {
	needle string
	kind   string
}{
	{"Bearer ", "bearer token"},
	{"bearer ", "bearer token"},
	{"Authorization: ", "auth header"},
	{"authorization: ", "auth header"},
}

// findSecret returns the earliest credential-shaped substring, if any.
func findSecret(text string) *secretMatch {
	lowered := asciiLower(text)
	best := (*secretMatch)(nil)
	consider := func(start, end int, kind string) {
		if end <= start || end > len(text) {
			return
		}
		if best == nil || start < best.start || (start == best.start && end > best.end) {
			best = &secretMatch{start: start, end: end, placeholder: placeholder(kind)}
		}
	}
	for _, entry := range tokenPrefixes {
		for from := 0; from < len(lowered); {
			offset := strings.Index(lowered[from:], strings.ToLower(entry.prefix))
			if offset < 0 {
				break
			}
			start := from + offset
			consider(start, tokenEnd(text, start), entry.kind)
			from = start + len(entry.prefix)
		}
	}
	for _, entry := range headerPrefixes {
		offset := strings.Index(text, entry.needle)
		if offset < 0 {
			continue
		}
		start := offset + len(entry.needle)
		// An `Authorization: Bearer <token>` header carries the credential
		// after the scheme, not the scheme itself.
		if strings.HasPrefix(entry.needle, "Authorization") || strings.HasPrefix(entry.needle, "authorization") {
			trimmed := strings.TrimLeft(text[start:], " \t")
			start += len(text[start:]) - len(trimmed)
			if len(trimmed) >= 7 && strings.EqualFold(trimmed[:7], "Bearer ") {
				start += 7
			}
		}
		consider(start, tokenEnd(text, start), entry.kind)
	}
	for _, entry := range keyValueNames {
		for from := 0; from < len(lowered); {
			offset := strings.Index(lowered[from:], entry.needle)
			if offset < 0 {
				break
			}
			start := from + offset
			after := text[start+len(entry.needle):]
			trimmed := strings.TrimLeft(after, " \t")
			gap := len(after) - len(trimmed)
			if eq := strings.IndexAny(trimmed, "=:"); eq >= 0 && eq <= 3 {
				valueStart := start + len(entry.needle) + gap + eq + 1
				value := text[valueStart:]
				valueTrimmed := strings.TrimLeft(value, " \t\"'")
				valueStart += len(value) - len(valueTrimmed)
				consider(valueStart, tokenEnd(text, valueStart), entry.kind)
			}
			from = start + len(entry.needle)
		}
	}
	return best
}

// tokenEnd finds the end of a credential-shaped run.
func tokenEnd(text string, start int) int {
	end := start
	for end < len(text) {
		ch := text[end]
		if isAlnum(ch) || ch == '.' || ch == '-' || ch == '_' || ch == '/' || ch == '+' || ch == '=' {
			end++
			continue
		}
		break
	}
	return end
}

func isAlnum(ch byte) bool {
	return ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z'
}

// asciiLower lowercases ASCII letters only, so byte offsets stay valid.
func asciiLower(text string) string {
	out := make([]byte, len(text))
	for i := 0; i < len(text); i++ {
		ch := text[i]
		if ch >= 'A' && ch <= 'Z' {
			ch += 'a' - 'A'
		}
		out[i] = ch
	}
	return string(out)
}

func placeholder(kind string) string {
	return "[redacted:" + kind + "]"
}

var secretKeyPattern = regexp.MustCompile(`(?i)(key|token|secret|password|authorization)`)

// secretKeyKind reports whether an object key names a credential.
func secretKeyKind(key string) string {
	if !secretKeyPattern.MatchString(key) {
		return ""
	}
	normalized := asciiLower(strings.TrimSpace(key))
	switch {
	case strings.Contains(normalized, "authorization"):
		return "auth header"
	case strings.Contains(normalized, "password") || strings.Contains(normalized, "passwd"):
		return "password"
	case strings.Contains(normalized, "token"):
		return "token"
	case strings.Contains(normalized, "secret"):
		return "secret"
	case strings.Contains(normalized, "key"):
		return "api key"
	}
	return "secret"
}

// RedactJSON redacts a JSON document without flattening it: a value held under
// a credential-named key becomes a placeholder, every other string is scrubbed
// with Redact. Invalid JSON falls back to text redaction.
func RedactJSON(raw []byte) string {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return Redact(string(raw))
	}
	redacted, err := json.Marshal(redactValue(value, ""))
	if err != nil {
		return Redact(string(raw))
	}
	return string(redacted)
}

func redactValue(value any, secretKind string) any {
	switch typed := value.(type) {
	case string:
		if secretKind != "" {
			return placeholder(secretKind)
		}
		return Redact(typed)
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, inner := range typed {
			out[key] = redactValue(inner, secretKeyKind(key))
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, inner := range typed {
			out[i] = redactValue(inner, "")
		}
		return out
	default:
		return value
	}
}
