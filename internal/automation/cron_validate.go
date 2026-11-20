package automation

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// cronFieldNames are the canonical names of the five cron fields, in order.
var cronFieldNames = [5]string{"minute", "hour", "day-of-month", "month", "day-of-week"}

// cronWeekdayAliases maps common weekday spellings to the numeric value the
// grammar expects. The grammar is numeric-only, so aliases are diagnosed rather
// than parsed; the mapped value is used only to suggest a replacement.
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

// CronIssue is one problem found by ValidateCron. Start and End are zero-based
// byte offsets into the original expression, so they stay correct even when a
// multibyte (for example CJK) character precedes the offending text.
type CronIssue struct {
	Field    int    // 0-based field index (0=minute ... 4=day-of-week); -1 when not field-specific
	Start    int    // byte offset of the offending text; -1 when unknown
	End      int    // byte offset just past the offending text; -1 when unknown
	Text     string // the offending substring
	Message  string // human-readable, English (locale-independent) explanation
	Severity string // cronSeverityError or cronSeverityWarning
}

// String renders the issue as a single readable line with its position.
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

// ValidateCron checks a five-field cron expression and returns every problem
// it finds. Unlike ParseCron, which stops at the first error, ValidateCron
// reports all issues together with the field name and byte position, so a CLI
// or editor can point at the exact mistake.
//
// It is a pure function: the result depends only on expr, and every diagnostic
// is in English and independent of any CJK or other locale. Field counts,
// ranges, steps, weekday aliases and illegal combinations (reversed ranges,
// repeated step separators) are checked. A syntactically valid expression that
// can never fire (for example day-of-month 31 restricted to February) is
// reported as a warning. A valid expression returns an empty, non-nil slice.
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
	// Cross-field checks only make sense when all five fields are present and
	// structurally valid, so parsing cannot fail.
	if len(fields) == 5 && !hasCronError(issues) {
		issues = append(issues, cronNeverFiresIssues(expr)...)
	}
	return issues
}

// cronNeverFiresIssues warns when expr is syntactically valid but has no
// matching time within five years. It reuses ParseCron/Next so the diagnostic
// cannot drift from the scheduler's own definition of "fires".
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

// cronFieldToken is one whitespace-separated field plus its byte offsets.
type cronFieldToken struct {
	text  string
	start int // byte offset of the first byte of the field
	end   int // byte offset just past the last byte of the field
}

// splitCronFields splits expr on Unicode whitespace while keeping byte
// offsets. It matches strings.Fields semantics but remains CJK-independent:
// offsets stay correct even when multibyte characters precede a bad field.
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

// validateCronField reports every issue in a single field. base is the byte
// offset of the field within the original expression.
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
			// Unrestricted: always valid.
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

// validateCronValue validates one numeric value (or a weekday alias), returning
// the parsed number and whether it is usable for range checks.
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

// validateCronStep reports a non-positive or non-numeric step value.
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
