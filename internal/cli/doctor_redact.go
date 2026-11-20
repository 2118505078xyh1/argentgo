package cli

import (
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// Doctor output is routinely copied into public issues. A detail may embed a
// config path, a base URL with inline `user:pass@` credentials, or an error
// string quoting either of those, so every detail is scrubbed before printing.
const (
	// doctorDetailMaxRunes bounds a single detail line. It is deliberately
	// generous: ordinary doctor output (paths, short errors, MCP problem lists)
	// stays byte-for-byte identical and only pathological payloads are cut.
	doctorDetailMaxRunes = 4096
	// doctorDetailTruncated is appended instead of the discarded tail.
	doctorDetailTruncated = " ...[truncated]"
)

// redactDoctorDetail makes one doctor detail safe to paste into a bug report.
// It is a pure function with no I/O or process state. The steps are ordered so
// that a redaction placeholder can never itself be cut in half: strip URL
// userinfo, redact remaining credential shapes, then truncate by rune.
func redactDoctorDetail(s string) string {
	if s == "" {
		return s
	}
	s = stripDoctorURLUserInfo(s)
	s = security.Redact(s)
	return truncateDoctorRunes(s, doctorDetailMaxRunes)
}

// stripDoctorURLUserInfo drops the `user:pass@` component of every URL in s but
// keeps the scheme, host, port, path and query, so a redacted base URL still
// tells the user which endpoint failed. A userinfo is only stripped when it
// contains a password (a `:` before the final `@`), which keeps bare
// `user@host` addresses and an `@` inside a URL path untouched.
func stripDoctorURLUserInfo(s string) string {
	var out strings.Builder
	out.Grow(len(s))
	rest := s
	for {
		sep := strings.Index(rest, "://")
		if sep < 0 {
			out.WriteString(rest)
			return out.String()
		}

		// Copy the scheme and "://" verbatim. A scheme must start with an
		// ASCII letter and contain only RFC 3986 scheme bytes; otherwise this
		// is not a URL and is copied unchanged before we keep scanning.
		schemeStart := sep
		for schemeStart > 0 && isDoctorURLSchemeByte(rest[schemeStart-1]) {
			schemeStart--
		}
		validScheme := schemeStart < sep && isDoctorASCIIAlpha(rest[schemeStart])
		out.WriteString(rest[:sep+3])
		rest = rest[sep+3:]
		if !validScheme {
			continue
		}

		// The authority ends at the first path/query/fragment or quoting byte.
		// Indexes are ASCII byte offsets, so slicing is rune-safe.
		end := 0
		for end < len(rest) && !isDoctorAuthorityDelimiter(rest[end]) {
			end++
		}
		authority := rest[:end]
		// The last @ separates userinfo from host: a password may itself
		// contain an unescaped @.
		if at := strings.LastIndexByte(authority, '@'); at > 0 && strings.Contains(authority[:at], ":") {
			out.WriteString(authority[at+1:])
		} else {
			out.WriteString(authority)
		}
		rest = rest[end:]
	}
}

// isDoctorURLSchemeByte reports whether ch may appear after the first letter of
// a URL scheme (RFC 3986: ALPHA / DIGIT / "+" / "-" / ".").
func isDoctorURLSchemeByte(ch byte) bool {
	return isDoctorASCIIAlpha(ch) || ch >= '0' && ch <= '9' || ch == '+' || ch == '-' || ch == '.'
}

func isDoctorASCIIAlpha(ch byte) bool {
	return ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z'
}

// isDoctorAuthorityDelimiter marks the end of a URL authority. Error strings
// often quote URLs, so the quote characters are delimiters too.
func isDoctorAuthorityDelimiter(ch byte) bool {
	switch ch {
	case '/', '?', '#', ' ', '\t', '\n', '\r', '"', '\'', '`':
		return true
	}
	return ch < 0x20 || ch == 0x7f
}

// truncateDoctorRunes keeps at most max runes and appends a marker when it had
// to discard anything. Iterating over the string yields byte offsets at rune
// boundaries, so a multi-byte CJK rune is never split in half.
func truncateDoctorRunes(s string, max int) string {
	count := 0
	for i := range s {
		if count == max {
			return s[:i] + doctorDetailTruncated
		}
		count++
	}
	return s
}
