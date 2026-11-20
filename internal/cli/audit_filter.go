package cli

import (
	"sort"
	"strings"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// AuditFilter 保存 FilterAuditEntries 使用的可选谓词。
// 字段为空时会被忽略，因此零值过滤器会保留所有条目。
type AuditFilter struct {
	// Decision 不区分大小写地匹配 security.AuditEntry.Decision
	// （allow、ask、deny、refused、unavailable、error）。
	Decision string
	// Session 精确匹配 security.AuditEntry.Session。
	Session string
	// Tool 精确匹配 security.AuditEntry.Tool。
	Tool string
	// Since 是条目时间戳的包含性下界。
	Since string
	// Until 是条目时间戳的包含性上界。
	Until string
	// Text 是对条目的每个文本字段进行匹配的不区分大小写的子串
	// （timestamp、tool、decision、reason、args、session、privacy）。
	Text string
}

// cjkTimeReplacer 把中文日期/时间标点映射为 ASCII 分隔符，
// 使诸如 "2026年01月02日 15时04分05秒" 的值能像 "2026-01-02 15:04:05" 一样解析。
var cjkTimeReplacer = strings.NewReplacer(
	"年", "-",
	"月", "-",
	"日", "",
	"时", ":",
	"分", ":",
	"秒", "",
)

// auditTimeLayouts 由 parseAuditTime 按顺序尝试。不带时区的布局
// 会按 UTC 解释，与 audit 写入的 UTC 时间戳一致。
var auditTimeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02",
	"2006-1-2 15:4:5",
	"2006-1-2",
}

// parseAuditTime 解析审计时间戳。它接受 RFC 3339、纯日期
// 和日期时间形式，以及它们的中文等价形式（年月日 / 时分秒）。
// 布尔值报告该值是否根本可被解析。
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

// matchesAuditText 报告 entry 的任何文本字段是否包含 needle。
// needle 应由调用方预先转为小写。
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

// FilterAuditEntries 返回满足 filter 中每个已设置字段的条目，
// 按时间戳升序排列（最早的在前）。输入切片永远不会被
// 修改。时间戳无法解析的条目排在有时戳的条目之后，同时
// 保持它们原有的相对顺序。
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
