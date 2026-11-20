package cli

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/session"
)

// SessionHit 是会话搜索命中的类型，直接复用 session 包的 Hit，
// 以便搜索得到的命中可以原样交给过滤器处理。
type SessionHit = session.Hit

// SessionFilter 保存会话搜索命中的可选过滤条件。每个字段为空时表示不启用
// 该维度，零值过滤器保留全部命中。
type SessionFilter struct {
	// Title 按会话标题子串匹配，不区分大小写。
	Title string
	// Since 是时间戳下界（含），兼容 RFC 3339、普通日期时间与 CJK 写法。
	Since string
	// Until 是时间戳上界（含），时间写法与 Since 相同。
	Until string
	// In 按命中所在目录的子串匹配，不区分大小写；"/" 与 "\\" 视为等价分隔符。
	In string
	// Text 在命中的 ID、标题、路径与摘要中做子串匹配，不区分大小写。
	Text string
}

// active 报告过滤器是否设置了任一条件。值先做去空白处理，
// 因此 "--since=" 这类空值不会被当成有效约束。
func (f SessionFilter) active() bool {
	return strings.TrimSpace(f.Title) != "" ||
		strings.TrimSpace(f.Since) != "" ||
		strings.TrimSpace(f.Until) != "" ||
		strings.TrimSpace(f.In) != "" ||
		strings.TrimSpace(f.Text) != ""
}

// normalizeSessionPath 统一路径分隔符，使 Windows 与 POSIX 写法可以互相匹配。
func normalizeSessionPath(s string) string {
	return strings.ReplaceAll(s, "\\", "/")
}

// sessionHitMatchesText 报告命中的任一文本字段是否包含 needle。
// needle 应由调用方预先转为小写。
func sessionHitMatchesText(hit SessionHit, needle string) bool {
	for _, field := range []string{hit.ID, hit.Title, hit.Path, hit.Snippet} {
		if strings.Contains(strings.ToLower(field), needle) {
			return true
		}
	}
	return false
}

// filterSessionHits 返回同时满足所有已设置条件的命中。时间上界与下界均为闭区间，
// 时间值由 parseAuditTime 解析，因此 RFC 3339、普通日期时间以及 CJK 形式
// （年月日 / 时分秒）均可使用；无法解析命中的时间戳时，只要启用了时间维度就丢弃该命中。
//
// 过滤器为空（零值）时原样返回 hits，不做复制也不排序，保证无过滤时行为不变。
// 有过滤时结果按时间戳从新到旧稳定排序，时间相同则按 ID、行号排序以保证确定性；
// 输入切片从不被修改。
func filterSessionHits(hits []SessionHit, filter SessionFilter) []SessionHit {
	if !filter.active() {
		return hits
	}

	since, hasSince := parseAuditTime(filter.Since)
	until, hasUntil := parseAuditTime(filter.Until)
	title := strings.ToLower(strings.TrimSpace(filter.Title))
	text := strings.ToLower(strings.TrimSpace(filter.Text))
	in := normalizeSessionPath(strings.ToLower(strings.TrimSpace(filter.In)))

	out := make([]SessionHit, 0, len(hits))
	for _, hit := range hits {
		if title != "" && !strings.Contains(strings.ToLower(hit.Title), title) {
			continue
		}
		if in != "" && !strings.Contains(normalizeSessionPath(strings.ToLower(filepath.Dir(hit.Path))), in) {
			continue
		}
		if text != "" && !sessionHitMatchesText(hit, text) {
			continue
		}
		if hasSince && hit.TS.Before(since) {
			continue
		}
		if hasUntil && hit.TS.After(until) {
			continue
		}
		out = append(out, hit)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].TS.Equal(out[j].TS) {
			return out[i].TS.After(out[j].TS)
		}
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return out[i].Line < out[j].Line
	})
	return out
}
