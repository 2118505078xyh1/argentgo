package update

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
)

// maxSummaryChanges bounds how many change items SummarizeReleaseNotes prints.
const maxSummaryChanges = 10

// changelogLabels are recognised (case-insensitively, as substrings) as
// changelog section headings in a release body. Chinese labels are included
// because argent's release notes are bilingual.
var changelogLabels = []string{
	"changelog", "change log", "what's changed", "whats changed",
	"release notes", "changes",
	"更新日志", "变更记录", "变更日志", "更新内容", "本次更新",
}

var markdownLink = regexp.MustCompile(`!?\[([^\]]+)\]\([^)]*\)`)

// NoteChecksum is a SHA-256 digest found in release notes together with the
// file name following it. Hash is always lowercase hex; Name is empty for a
// bare digest line.
type NoteChecksum struct {
	Hash string
	Name string
}

// ReleaseNotes is the structured view of a GitHub release body returned by
// ParseReleaseNotes.
type ReleaseNotes struct {
	// Summary is the first prose line of the body with inline markdown
	// stripped. Headings, bullets and checksum lines are skipped.
	Summary string
	// Changes are the bullet items of the changelog section. If the body has
	// no recognisable changelog heading, all bullet items are returned.
	Changes []string
	// Checksums are all SHA-256 lines found anywhere in the body, in order.
	Checksums []NoteChecksum
}

// Notes parses r.Body into structured release notes. It never fails: an empty
// or unusual body simply yields empty fields.
func (r Release) Notes() ReleaseNotes { return ParseReleaseNotes(r.Body) }

// ParseReleaseNotes extracts a short summary, a changelog and SHA-256 lines
// from a GitHub release body. It is a pure function: no I/O, no mutation, and
// identical input always yields identical output.
//
// Headings may use English or Chinese labels (for example "## What's Changed"
// or "## 更新日志"); bullets may use "-", "*", "+" or "1."; and checksum lines
// may be fenced in code blocks, prefixed by a list marker, or bare digests.
// A sub-heading inside a changelog section keeps its bullets in that section;
// a heading of the same or higher level ends it.
func ParseReleaseNotes(body string) ReleaseNotes {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")

	hasChangelog := false
	for _, raw := range lines {
		if _, text, ok := markdownHeading(raw); ok && isChangelogHeading(text) {
			hasChangelog = true
			break
		}
	}

	var out ReleaseNotes
	sectionLevel := 0
	inChangelog := false
	var allBullets, changelogBullets []string

	for _, raw := range lines {
		if level, text, ok := markdownHeading(raw); ok {
			if inChangelog && level <= sectionLevel {
				inChangelog = false
			}
			if isChangelogHeading(text) && !inChangelog {
				inChangelog = true
				sectionLevel = level
			}
			continue
		}
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			continue
		}
		if sum, ok := parseNoteChecksum(line); ok {
			out.Checksums = append(out.Checksums, sum)
			continue
		}
		if bullet, ok := markdownBullet(line); ok {
			allBullets = append(allBullets, bullet)
			if inChangelog {
				changelogBullets = append(changelogBullets, bullet)
			}
			continue
		}
		if out.Summary == "" {
			out.Summary = stripInlineMarkdown(line)
		}
	}

	if hasChangelog {
		out.Changes = changelogBullets
	} else {
		out.Changes = allBullets
	}
	return out
}

// SummarizeReleaseNotes renders notes as a compact, deterministic,
// newline-separated summary suitable for an update prompt or log line. At most
// maxSummaryChanges change items are printed; any remainder is counted.
func SummarizeReleaseNotes(notes ReleaseNotes) string {
	var builder strings.Builder
	if notes.Summary != "" {
		builder.WriteString(notes.Summary)
	}
	shown := len(notes.Changes)
	if shown > maxSummaryChanges {
		shown = maxSummaryChanges
	}
	for _, change := range notes.Changes[:shown] {
		if builder.Len() > 0 {
			builder.WriteByte('\n')
		}
		builder.WriteString("- ")
		builder.WriteString(change)
	}
	if remaining := len(notes.Changes) - shown; remaining > 0 {
		if builder.Len() > 0 {
			builder.WriteByte('\n')
		}
		fmt.Fprintf(&builder, "... and %d more change(s)", remaining)
	}
	if count := len(notes.Checksums); count > 0 {
		if builder.Len() > 0 {
			builder.WriteByte('\n')
		}
		fmt.Fprintf(&builder, "%d SHA-256 checksum(s)", count)
	}
	return builder.String()
}

// markdownHeading recognises an ATX heading ("## Foo"). It returns the level
// (1..6), the heading text, and whether the line was a heading at all.
func markdownHeading(line string) (level int, text string, ok bool) {
	trimmed := strings.TrimSpace(line)
	count := 0
	for count < len(trimmed) && trimmed[count] == '#' {
		count++
	}
	if count == 0 || count > 6 {
		return 0, "", false
	}
	if count < len(trimmed) && trimmed[count] != ' ' && trimmed[count] != '\t' {
		return 0, "", false
	}
	return count, strings.Trim(trimmed[count:], "# \t"), true
}

func isChangelogHeading(text string) bool {
	normalised := strings.ToLower(strings.TrimSpace(text))
	if normalised == "" {
		return false
	}
	for _, label := range changelogLabels {
		if strings.Contains(normalised, label) {
			return true
		}
	}
	return false
}

// markdownBullet strips a bullet or ordered-list marker from a trimmed line and
// removes inline markdown so the item reads as plain text.
func markdownBullet(line string) (string, bool) {
	item, ok := stripListMarker(strings.TrimSpace(line))
	if !ok {
		return "", false
	}
	return stripInlineMarkdown(item), true
}

// stripListMarker removes a leading "-", "*", "+" or "1." / "1)" marker and
// reports whether one was present.
func stripListMarker(line string) (string, bool) {
	if len(line) >= 2 && (line[0] == '-' || line[0] == '*' || line[0] == '+') &&
		(line[1] == ' ' || line[1] == '\t') {
		return strings.TrimSpace(line[2:]), true
	}
	index := 0
	for index < len(line) && line[index] >= '0' && line[index] <= '9' {
		index++
	}
	if index > 0 && index+1 < len(line) &&
		(line[index] == '.' || line[index] == ')') &&
		(line[index+1] == ' ' || line[index+1] == '\t') {
		return strings.TrimSpace(line[index+2:]), true
	}
	return line, false
}

// parseNoteChecksum finds a leading 64-character SHA-256 digest on a line,
// optionally after a list marker and/or inside backticks, and returns the
// lowercased digest plus the following file name (which may be empty).
func parseNoteChecksum(line string) (NoteChecksum, bool) {
	text, _ := stripListMarker(strings.TrimSpace(line))
	text = strings.TrimSpace(strings.Trim(text, "`"))
	if len(text) < sha256.Size*2 {
		return NoteChecksum{}, false
	}
	candidate := text[:sha256.Size*2]
	hash, err := normaliseChecksum(candidate)
	if err != nil {
		return NoteChecksum{}, false
	}
	// A longer hex string is not a checksum line; its first 64 characters are
	// just part of a single (invalid) token.
	if len(text) > sha256.Size*2 && isHexDigit(text[sha256.Size*2]) {
		return NoteChecksum{}, false
	}
	name := strings.TrimSpace(text[sha256.Size*2:])
	name = strings.TrimSpace(strings.TrimLeft(name, "* \t"))
	name = strings.TrimSpace(strings.Trim(name, "`"))
	return NoteChecksum{Hash: hash, Name: name}, true
}

func isHexDigit(char byte) bool {
	return char >= '0' && char <= '9' || char >= 'a' && char <= 'f' || char >= 'A' && char <= 'F'
}

// stripInlineMarkdown removes the common inline markdown decorations so a
// summary line reads as plain text.
func stripInlineMarkdown(line string) string {
	text := markdownLink.ReplaceAllString(line, "$1")
	text = strings.NewReplacer("`", "", "**", "", "__", "").Replace(text)
	return strings.Join(strings.Fields(text), " ")
}
