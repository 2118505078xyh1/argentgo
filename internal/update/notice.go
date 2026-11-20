package update

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxNoticeSummaryRunes bounds the release-note excerpt appended to an update
// notice so the whole message stays a compact, single line.
const maxNoticeSummaryRunes = 80

// placeholderVersion replaces a version string that fails basic validation, so
// untrusted release metadata cannot inject control characters or markup into a
// notice shown to the user.
const placeholderVersion = "?"

// FormatUpdateNotice returns a sanitised, single-line, Chinese-language notice
// telling the user that a newer build is available and how to upgrade. It is a
// pure function: no I/O, no global state, and identical inputs always produce
// identical output.
//
// Both version strings are checked against a conservative ASCII whitelist;
// anything else (control characters, spaces, ANSI escapes, markup) becomes a
// "?" placeholder. When notes is non-empty a short single-line summary is
// appended and capped at maxNoticeSummaryRunes runes (rune-safe for CJK text).
// If latest is not newer than current, a short "already up to date" notice is
// returned instead.
func FormatUpdateNotice(current, latest, notes string) string {
	safeCurrent := safeVersionToken(current)
	safeLatest := safeVersionToken(latest)
	if CompareVersions(latest, current) <= 0 {
		return "已是最新版本"
	}
	notice := "有新版本 " + safeCurrent + "→" + safeLatest + "，运行 argent update 升级"
	if summary := noticeSummary(notes); summary != "" {
		notice += "：" + summary
	}
	return notice
}

// safeVersionToken returns text unchanged when it is a plausible version tag
// made only of ASCII letters, digits, ".", "-", "+" or "_". Anything else is
// replaced by placeholderVersion so release metadata cannot inject content.
func safeVersionToken(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || len(trimmed) > 64 {
		return placeholderVersion
	}
	for index := 0; index < len(trimmed); index++ {
		char := trimmed[index]
		switch {
		case char >= '0' && char <= '9',
			char >= 'a' && char <= 'z',
			char >= 'A' && char <= 'Z',
			char == '.', char == '-', char == '+', char == '_':
			// allowed version character
		default:
			return placeholderVersion
		}
	}
	return trimmed
}

// noticeSummary extracts a short, single-line excerpt from raw release notes.
// It reuses ParseReleaseNotes to pick the first prose line (with inline markdown
// stripped), falling back to the first change item, then normalises whitespace
// and truncates by rune count so CJK characters are never split.
func noticeSummary(notes string) string {
	if strings.TrimSpace(notes) == "" {
		return ""
	}
	parsed := ParseReleaseNotes(notes)
	text := parsed.Summary
	if strings.TrimSpace(text) == "" && len(parsed.Changes) > 0 {
		text = parsed.Changes[0]
	}
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return truncateRunes(singleLine(text), maxNoticeSummaryRunes)
}

// singleLine collapses every run of whitespace or control characters (including
// newlines and ANSI escapes) into one ASCII space, so the result contains no
// line breaks or terminal control sequences.
func singleLine(text string) string {
	var builder strings.Builder
	builder.Grow(len(text))
	spacePending := false
	for _, char := range text {
		if unicode.IsControl(char) || unicode.IsSpace(char) {
			spacePending = builder.Len() > 0
			continue
		}
		if spacePending {
			builder.WriteByte(' ')
			spacePending = false
		}
		builder.WriteRune(char)
	}
	return builder.String()
}

// truncateRunes shortens text to at most max runes, trimming a trailing space
// and appending an ellipsis when it was cut. It is safe for multi-byte runes.
func truncateRunes(text string, max int) string {
	if utf8.RuneCountInString(text) <= max {
		return text
	}
	runes := []rune(text)
	return strings.TrimRight(string(runes[:max]), " ") + "…"
}
