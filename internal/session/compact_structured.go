package session

import (
	"fmt"
	"strings"
)

// StructuredSummary is the machine-readable projection of a compaction summary.
//
// Compaction.Summary is historically one free-text markdown string (see
// Summarize). This DTO is an additive, opt-in view of that text: it lets a later
// UI or search index consume progress, uncertainties and user actions without
// reimplementing a markdown parser. The compaction write path does not use it —
// Summarize keeps emitting byte-for-byte the same free text as before.
type StructuredSummary struct {
	Progress      []string `json:"progress,omitempty"`
	Uncertainties []string `json:"uncertainties,omitempty"`
	UserActions   []string `json:"userActions,omitempty"`
}

// structuredBucket is the internal target of one markdown section heading.
type structuredBucket int

const (
	bucketNone structuredBucket = iota
	bucketProgress
	bucketUncertainties
	bucketUserActions
)

// summaryHeadingBuckets maps a normalized section title to one of the three
// StructuredSummary fields. It deliberately covers three families:
//
//   - the canonical structured titles emitted by RenderStructuredSummary;
//   - the legacy titles Summarize has always written ("Goal", "Failures",
//     "Next steps", ...), so existing free text can be parsed as-is;
//   - common CJK equivalents, since summaries are often written in zh-CN.
//
// Titles not in this table are treated as structure, not content: their section
// is skipped and does not by itself make ParseStructuredSummary succeed. That
// is what lets a format drift fall back to "not structured" instead of
// producing a misleading partial DTO.
var summaryHeadingBuckets = map[string]structuredBucket{
	// Canonical structured titles.
	"progress":      bucketProgress,
	"uncertainties": bucketUncertainties,
	"uncertainty":   bucketUncertainties,
	"user actions":  bucketUserActions,
	"user action":   bucketUserActions,

	// Legacy Summarize sections.
	"goal":          bucketProgress,
	"goals":         bucketProgress,
	"files changed": bucketProgress,
	"tool activity": bucketProgress,
	"failures":      bucketUncertainties,
	"failure":       bucketUncertainties,
	"next steps":    bucketUserActions,
	"next step":     bucketUserActions,

	// CJK equivalents.
	"进展":    bucketProgress,
	"进度":    bucketProgress,
	"已完成":   bucketProgress,
	"目标":    bucketProgress,
	"文件变更":  bucketProgress,
	"修改文件":  bucketProgress,
	"工具活动":  bucketProgress,
	"不确定性":  bucketUncertainties,
	"不确定事项": bucketUncertainties,
	"不确定":   bucketUncertainties,
	"失败":    bucketUncertainties,
	"错误":    bucketUncertainties,
	"用户操作":  bucketUserActions,
	"用户行动":  bucketUserActions,
	"下一步":   bucketUserActions,
	"后续":    bucketUserActions,
	"待办":    bucketUserActions,
}

// normalizeSummaryHeading recognizes a markdown heading line and returns its
// normalized title. A line is a heading only when its first non-space character
// is '#'; plain labels ("Progress:") and bold text ("**Progress**") are not —
// a format drift must be detectable rather than silently accepted.
func normalizeSummaryHeading(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || trimmed[0] != '#' {
		return "", false
	}
	title := strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
	title = strings.Join(strings.Fields(title), " ") // collapse whitespace, incl. U+3000
	title = strings.TrimRight(title, ":：")
	title = strings.ToLower(title)
	return title, true
}

// summaryBullet extracts the text of a "- ", "* " or "• " bullet. Empty and
// "(none)"-style placeholder bullets are not content and are dropped. Skipping
// placeholders is what makes Render -> Parse a clean round trip, since the
// renderer uses them to keep empty sections visible to a human reader.
func summaryBullet(line string) (string, bool) {
	for _, prefix := range []string{"- ", "* ", "• "} {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		item := strings.TrimSpace(line[len(prefix):])
		if item == "" || isSummaryNoneMarker(item) {
			return "", false
		}
		return item, true
	}
	return "", false
}

func isSummaryNoneMarker(item string) bool {
	switch strings.ToLower(strings.TrimSpace(item)) {
	case "(none)", "none", "n/a", "(无)", "（无）", "无":
		return true
	}
	return false
}

// ParseStructuredSummary is a best-effort, pure parser that projects a
// free-text compaction summary onto StructuredSummary. The bool reports whether
// any *recognized* section heading was found: false means the text carries no
// structured information (empty, prose only, or a drifted format) and the
// caller should keep using the original free text. A true result may contain
// empty fields when a section exists but has no bullets.
//
// Bullets are collected in appearance order and deduplicated. Unrecognized
// sections (including the leading "## Compaction" metadata block) are ignored.
func ParseStructuredSummary(text string) (StructuredSummary, bool) {
	var out StructuredSummary
	seenProgress := make(map[string]bool)
	seenUncertainties := make(map[string]bool)
	seenUserActions := make(map[string]bool)

	current := bucketNone
	found := false
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if title, ok := normalizeSummaryHeading(trimmed); ok {
			current = summaryHeadingBuckets[title] // unknown title -> bucketNone
			if current != bucketNone {
				found = true
			}
			continue
		}
		if current == bucketNone {
			continue
		}
		item, ok := summaryBullet(trimmed)
		if !ok {
			continue
		}
		var dest *[]string
		var seen map[string]bool
		switch current {
		case bucketProgress:
			dest, seen = &out.Progress, seenProgress
		case bucketUncertainties:
			dest, seen = &out.Uncertainties, seenUncertainties
		case bucketUserActions:
			dest, seen = &out.UserActions, seenUserActions
		}
		if seen[item] {
			continue
		}
		seen[item] = true
		*dest = append(*dest, item)
	}
	if !found {
		return StructuredSummary{}, false
	}
	return out, true
}

// RenderStructuredSummary renders a StructuredSummary in the canonical
// markdown shape understood by ParseStructuredSummary. Empty fields are emitted
// as "- (none)" so the human-readable text keeps the familiar Summarize layout
// while ParseStructuredSummary still round-trips them to empty slices.
func RenderStructuredSummary(s StructuredSummary) string {
	var b strings.Builder
	write := func(title string, items []string) {
		fmt.Fprintf(&b, "\n## %s\n", title)
		if len(items) == 0 {
			b.WriteString("- (none)\n")
			return
		}
		for _, item := range items {
			// Keep one bullet per line: an embedded newline would look like a
			// new section body and break the round trip.
			one := strings.TrimSpace(item)
			one = strings.ReplaceAll(one, "\r", " ")
			one = strings.ReplaceAll(one, "\n", " ")
			fmt.Fprintf(&b, "- %s\n", one)
		}
	}
	write("Progress", s.Progress)
	write("Uncertainties", s.Uncertainties)
	write("User actions", s.UserActions)
	return b.String()
}

// Structured is a read-only, opt-in projection of c.Summary. It does not
// mutate the record and is not called by any compaction write path.
func (c Compaction) Structured() (StructuredSummary, bool) {
	return ParseStructuredSummary(c.Summary)
}
