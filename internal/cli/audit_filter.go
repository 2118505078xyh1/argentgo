package cli

import (
	"sort"
	"strings"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// AuditFilter holds the optional predicates used by FilterAuditEntries.
// An empty field is ignored, so a zero-value filter keeps every entry.
type AuditFilter struct {
	// Decision matches security.AuditEntry.Decision case-insensitively
	// (allow, ask, deny, refused, unavailable, error).
	Decision string
	// Session matches security.AuditEntry.Session exactly.
	Session string
	// Tool matches security.AuditEntry.Tool exactly.
	Tool string
	// Since is an inclusive lower bound on the entry timestamp.
	Since string
	// Until is an inclusive upper bound on the entry timestamp.
	Until string
	// Text is a case-insensitive substring matched against every text field
	// of an entry (timestamp, tool, decision, reason, args, session, privacy).
	Text string
}

// cjkTimeReplacer maps Chinese date/time punctuation onto ASCII separators so
// values such as "2026年01月02日 15时04分05秒" parse like "2026-01-02 15:04:05".
var cjkTimeReplacer = strings.NewReplacer(
	"年", "-",
	"月", "-",
	"日", "",
	"时", ":",
	"分", ":",
	"秒", "",
)

// auditTimeLayouts are tried in order by parseAuditTime. Layouts without a
// zone are interpreted in UTC, matching the UTC timestamps written by audit.
var auditTimeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02",
	"2006-1-2 15:4:5",
	"2006-1-2",
}

// parseAuditTime parses an audit timestamp. It accepts RFC 3339, plain date
// and date-time forms, and their CJK equivalents (年月日 / 时分秒). The
// boolean reports whether the value could be parsed at all.
func parseAuditTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	normalized := cjkTimeReplacer.Replace(s)
	for _, layout := range auditTimeLayouts {
		if t, err := time.ParseInLocation(layout, normalized, time.UTC); err == nil {
			return t, true
		}
	}
	if normalized != s {
		for _, layout := range auditTimeLayouts {
			if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
				return t, true
			}
		}
	}
	return time.Time{}, false
}

// matchesAuditText reports whether any text field of entry contains needle.
// needle is expected to be lower-cased by the caller.
func matchesAuditText(entry security.AuditEntry, needle string) bool {
	for _, field := range []string{
		entry.TS, entry.Tool, entry.Decision, entry.Reason,
		entry.Args, entry.Session, entry.PrivacyMode,
	} {
		if strings.Contains(strings.ToLower(field), needle) {
			return true
		}
	}
	return false
}

// FilterAuditEntries returns the entries satisfying every set field of filter,
// sorted by timestamp ascending (oldest first). The input slice is never
// modified. Entries with an unparseable timestamp sort after dated ones while
// keeping their original relative order.
func FilterAuditEntries(entries []security.AuditEntry, filter AuditFilter) []security.AuditEntry {
	since, hasSince := parseAuditTime(filter.Since)
	until, hasUntil := parseAuditTime(filter.Until)

	out := make([]security.AuditEntry, 0, len(entries))
	for _, entry := range entries {
		if filter.Decision != "" && !strings.EqualFold(entry.Decision, filter.Decision) {
			continue
		}
		if filter.Session != "" && entry.Session != filter.Session {
			continue
		}
		if filter.Tool != "" && entry.Tool != filter.Tool {
			continue
		}
		if filter.Text != "" && !matchesAuditText(entry, strings.ToLower(filter.Text)) {
			continue
		}
		ts, ok := parseAuditTime(entry.TS)
		if hasSince {
			if !ok || ts.Before(since) {
				continue
			}
		}
		if hasUntil {
			if !ok || ts.After(until) {
				continue
			}
		}
		out = append(out, entry)
	}

	sort.SliceStable(out, func(i, j int) bool {
		ti, oki := parseAuditTime(out[i].TS)
		tj, okj := parseAuditTime(out[j].TS)
		if !oki || !okj {
			return oki && !okj
		}
		return ti.Before(tj)
	})
	return out
}
