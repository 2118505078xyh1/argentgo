package automation

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// cronFieldNames 是五个 cron 字段的规范名称，按顺序。
var cronFieldNames = [5]string{"minute", "hour", "day-of-month", "month", "day-of-week"}

// cronWeekdayAliases 把常见星期拼写映射到语法期望的数字值。该语法只接受数字，因此别名被诊断而非
// 语法期望的数字值。该语法只接受数字，因此别名被诊断而非
// 解析；映射值仅用于建议替换。
var cronWeekdayAliases = map[string]int{
	"sun": 0, "sunday": 0,
	"mon": 1, "monday": 1,
	"tue": 2, "tuesday": 2,
	"wed": 3, "wednesday": 3,
	"thu": 4, "thursday": 4,
	"fri": 5, "friday": 5,
	"sat": 6, "saturday": 6,
}

const (
	cronSeverityError   = "error"
	cronSeverityWarning = "warning"
)

// CronIssue 是 ValidateCron 发现的一个问题。Start 和 End 是原始表达式中从零开始的字节偏移，因此即使在违规文本之前有
// 原始表达式中从零开始的字节偏移，因此即使在违规文本之前有
// 多字节（例如 CJK）字符，它们也保持正确。
type CronIssue struct {
	Field    int    // 0-based field index (0=minute ... 4=day-of-week); -1 when not field-specific
	Start    int    // byte offset of the offending text; -1 when unknown
	End      int    // byte offset just past the offending text; -1 when unknown
	Text     string // the offending substring
	Message  string // human-readable, English (locale-independent) explanation
	Severity string // cronSeverityError or cronSeverityWarning
}

// String 将问题渲染为带位置的单行可读文本。
func (i CronIssue) String() string {
	where := "expression"
	if i.Field >= 0 && i.Field < len(cronFieldNames) {
		where = fmt.Sprintf("field %d (%s)", i.Field+1, cronFieldNames[i.Field])
	}
	if i.Start >= 0 {
		where += fmt.Sprintf(" at byte %d", i.Start)
	}
	var b strings.Builder
	b.WriteString(where)
	b.WriteString(": ")
	if i.Text != "" {
		fmt.Fprintf(&b, "%q: ", i.Text)
	}
	b.WriteString(i.Message)
	b.WriteString(" [")
	b.WriteString(i.Severity)
	b.WriteString("]")
	return b.String()
}

// ValidateCron 检查五字段 cron 表达式并返回它找到的所有
// ValidateCron 检查五字段 cron 表达式并返回它找到的所有问题。与遇到第一个错误就停止的 ParseCron 不同，ValidateCron
// 报告所有问题以及字段名和字节位置，使 CLI 或编辑器能指向确切的错误。
// 或编辑器能指向确切的错误。
//
// 它是纯函数：结果只依赖 expr，且每条诊断
// 它是纯函数：结果只依赖 expr，且每条诊断都是英文且与任何 CJK 或其他语言环境无关。检查字段数量、
// 范围、步长、星期别名和非法组合（反向范围、
// 重复步长分隔符）。语法有效但永不触发的表达式
// 范围、步长、星期别名和非法组合（反向范围、重复步长分隔符）。语法有效但永不触发的表达式（例如限定在二月的 31 日）
// 被报告为警告。有效表达式返回空但非 nil 的切片。
func ValidateCron(expr string) []CronIssue {
	issues := make([]CronIssue, 0)
	fields := splitCronFields(expr)
	if len(fields) != 5 {
		issues = append(issues, CronIssue{
			Field:    -1,
			Start:    -1,
			End:      -1,
			Text:     strings.Join(fieldTexts(fields), " "),
			Message:  fmt.Sprintf("expected 5 fields (minute hour day-of-month month day-of-week), got %d", len(fields)),
			Severity: cronSeverityError,
		})
	}
	bounds := [5][2]int{
		{minuteMin, minuteMax},
		{hourMin, hourMax},
		{domMin, domMax},
		{monthMin, monthMax},
		{dowMin, dowMax},
	}
	for i, field := range fields {
		if i >= len(bounds) {
			break
		}
		issues = append(issues, validateCronField(field.text, field.start, i, bounds[i][0], bounds[i][1])...)
	}
	// 跨字段检查只有在所有五个字段都存在且
	// 结构有效时才有意义，因此解析不会失败。
	if len(fields) == 5 && !hasCronError(issues) {
		issues = append(issues, cronNeverFiresIssues(expr)...)
	}
	return issues
}

// 跨字段检查只有在所有五个字段都存在且结构有效时才有意义，因此解析不会失败。
// cronNeverFiresIssues 在 expr 语法有效但五年内没有匹配时间时发出警告。它复用 ParseCron/Next，使诊断
// 不会偏离调度器自己对"触发"的定义。
func cronNeverFiresIssues(expr string) []CronIssue {
	c, err := ParseCron(expr)
	if err != nil {
		return nil
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if c.Next(base).IsZero() {
		return []CronIssue{{
			Field:    -1,
			Start:    -1,
			End:      -1,
			Message:  "expression is valid but can never fire (no matching time within five years)",
			Severity: cronSeverityWarning,
		}}
	}
	return nil
}

// cronFieldToken 是一个以空白分隔的字段及其字节偏移。
type cronFieldToken struct {
	text  string
	start int // byte offset of the first byte of the field
	end   int // byte offset just past the last byte of the field
}

// splitCronFields 在 Unicode 空白上拆分 expr 并保留字节
// splitCronFields 在 Unicode 空白上拆分 expr 并保留字节偏移。它匹配 strings.Fields 语义但保持 CJK 无关：
// 即使多字节字符出现在坏字段之前，偏移也保持正确。
func splitCronFields(expr string) []cronFieldToken {
	var out []cronFieldToken
	pos := 0
	for pos < len(expr) {
		r, size := utf8.DecodeRuneInString(expr[pos:])
		if unicode.IsSpace(r) {
			pos += size
			continue
		}
		start := pos
		for pos < len(expr) {
			r, size = utf8.DecodeRuneInString(expr[pos:])
			if unicode.IsSpace(r) {
				break
			}
			pos += size
		}
		out = append(out, cronFieldToken{text: expr[start:pos], start: start, end: pos})
	}
	return out
}

func fieldTexts(fields []cronFieldToken) []string {
	out := make([]string, len(fields))
	for i, field := range fields {
		out[i] = field.text
	}
	return out
}

func hasCronError(issues []CronIssue) bool {
	for _, issue := range issues {
		if issue.Severity == cronSeverityError {
			return true
		}
	}
	return false
}

// validateCronField 报告单个字段中的所有问题。base 是该字段在原始表达式中的字节偏移。
// 该字段在原始表达式中的字节偏移。
func validateCronField(text string, base, idx, min, max int) []CronIssue {
	var issues []CronIssue
	add := func(start, end int, fragment, message string) {
		issues = append(issues, CronIssue{
			Field:    idx,
			Start:    start,
			End:      end,
			Text:     fragment,
			Message:  message,
			Severity: cronSeverityError,
		})
	}
	if strings.TrimSpace(text) == "" {
		add(base, base+len(text), text, "empty field")
		return issues
	}
	partBase := base
	for _, part := range strings.Split(text, ",") {
		lead := len(part) - len(strings.TrimLeftFunc(part, unicode.IsSpace))
		trimmed := strings.TrimSpace(part)
		partStart := partBase + lead
		partBase += len(part) + 1 // +1 for the comma
		if trimmed == "" {
			add(partStart, partStart, "", "empty list element")
			continue
		}
		if slashes := strings.Count(trimmed, "/"); slashes > 1 {
			add(partStart, partStart+len(trimmed), trimmed,
				fmt.Sprintf("at most one '/' step separator is allowed, got %d", slashes))
			continue
		}

		body, stepText, hasStep := strings.Cut(trimmed, "/")
		if hasStep {
			validateCronStep(stepText, partStart+len(body)+1, add)
		}
		switch {
		case body == "*":
			// 无限制：始终有效。
		case strings.Contains(body, "-"):
			first, last, found := strings.Cut(body, "-")
			if !found || strings.Contains(last, "-") {
				add(partStart, partStart+len(body), body, "malformed range; expected a-b")
				break
			}
			lo, okLo := validateCronValue(first, partStart, min, max, idx, add)
			hi, okHi := validateCronValue(last, partStart+len(first)+1, min, max, idx, add)
			if okLo && okHi && lo > hi {
				add(partStart, partStart+len(body), body,
					fmt.Sprintf("reversed range: %d is greater than %d", lo, hi))
			}
		default:
			validateCronValue(body, partStart, min, max, idx, add)
		}
	}
	return issues
}

// validateCronValue 验证一个数字值（或星期别名），返回解析出的数字以及它是否可用于范围检查。
// 解析出的数字以及它是否可用于范围检查。
func validateCronValue(token string, start int, min, max, idx int, add func(int, int, string, string)) (int, bool) {
	lead := len(token) - len(strings.TrimLeftFunc(token, unicode.IsSpace))
	trimmed := strings.TrimSpace(token)
	at := start + lead
	if trimmed == "" {
		add(at, at, "", "missing value")
		return 0, false
	}
	if idx == 4 { // day-of-week is the only field with named values
		if n, ok := cronWeekdayAliases[strings.ToLower(trimmed)]; ok {
			add(at, at+len(trimmed), trimmed,
				fmt.Sprintf("weekday alias %q is not supported; use %d", trimmed, n))
			return 0, false
		}
	}
	v, err := strconv.Atoi(trimmed)
	if err != nil {
		add(at, at+len(trimmed), trimmed, "not a number")
		return 0, false
	}
	if v < min || v > max {
		add(at, at+len(trimmed), trimmed,
			fmt.Sprintf("value %d is outside %d-%d", v, min, max))
		return 0, false
	}
	return v, true
}

// validateCronStep 报告非正或非数字的步长值。
func validateCronStep(stepText string, start int, add func(int, int, string, string)) {
	lead := len(stepText) - len(strings.TrimLeftFunc(stepText, unicode.IsSpace))
	trimmed := strings.TrimSpace(stepText)
	at := start + lead
	if trimmed == "" {
		add(at, at, "", "empty step after '/'")
		return
	}
	n, err := strconv.Atoi(trimmed)
	if err != nil || n <= 0 {
		add(at, at+len(trimmed), trimmed, "step must be a positive integer")
	}
}
