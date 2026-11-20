package automation

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cron is a parsed five-field cron expression:
//
//	minute hour day-of-month month day-of-week
//
// Each field accepts `*`, a single value `n`, a range `a-b`, a step `*/n`,
// a list `a,b,c`, and a stepped range `a-b/n`. Day of week 0 is Sunday and 7 is
// accepted as Sunday too. Times are evaluated in the location of the time value
// passed to Next.
//
// Day-of-month and day-of-week follow the traditional Vixie rule: when both are
// restricted (neither starts with `*`) a day matches if either field matches,
// otherwise the restricted field must match.
type Cron struct {
	expr    string
	minute  fieldSet
	hour    fieldSet
	dom     fieldSet
	month   fieldSet
	dow     fieldSet
	domStar bool
	dowStar bool
}

// fieldSet is a bitmap of allowed values; cron fields are always below 64.
type fieldSet struct {
	bits uint64
}

func (f fieldSet) has(value int) bool {
	if value < 0 || value > 63 {
		return false
	}
	return f.bits&(1<<uint(value)) != 0
}

// Cron field bounds. Day of week is parsed as 0-7 so that 7 (=Sunday) is
// accepted, then folded onto 0.
const (
	minuteMin, minuteMax = 0, 59
	hourMin, hourMax     = 0, 23
	domMin, domMax       = 1, 31
	monthMin, monthMax   = 1, 12
	dowMin, dowMax       = 0, 7
)

// ParseCron parses a five-field cron expression.
func ParseCron(expr string) (*Cron, error) {
	fields := strings.Fields(strings.TrimSpace(expr))
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron: %q must have 5 fields (minute hour day-of-month month day-of-week)", expr)
	}
	c := &Cron{expr: strings.Join(fields, " ")}
	var err error
	if c.minute, err = parseCronField(fields[0], minuteMin, minuteMax, "minute"); err != nil {
		return nil, err
	}
	if c.hour, err = parseCronField(fields[1], hourMin, hourMax, "hour"); err != nil {
		return nil, err
	}
	if c.dom, err = parseCronField(fields[2], domMin, domMax, "day-of-month"); err != nil {
		return nil, err
	}
	if c.month, err = parseCronField(fields[3], monthMin, monthMax, "month"); err != nil {
		return nil, err
	}
	if c.dow, err = parseCronField(fields[4], dowMin, dowMax, "day-of-week"); err != nil {
		return nil, err
	}
	// Fold 7 onto 0 so both spellings of Sunday match.
	if c.dow.has(7) {
		c.dow.bits |= 1
		c.dow.bits &^= 1 << 7
	}
	c.domStar = strings.HasPrefix(fields[2], "*")
	c.dowStar = strings.HasPrefix(fields[4], "*")
	return c, nil
}

// parseCronField expands one field into a bitmap.
func parseCronField(text string, min, max int, name string) (fieldSet, error) {
	var out fieldSet
	if strings.TrimSpace(text) == "" {
		return out, fmt.Errorf("cron: empty %s field", name)
	}
	for _, part := range strings.Split(text, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return out, fmt.Errorf("cron: empty list element in %s field %q", name, text)
		}
		body, stepText, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(strings.TrimSpace(stepText))
			if err != nil || n <= 0 {
				return out, fmt.Errorf("cron: %q is not a valid step in %s field %q", stepText, name, text)
			}
			step = n
		}

		var lo, hi int
		switch {
		case body == "*":
			lo, hi = min, max
		case strings.Contains(body, "-"):
			first, last, _ := strings.Cut(body, "-")
			a, err := strconv.Atoi(strings.TrimSpace(first))
			if err != nil {
				return out, fmt.Errorf("cron: %q is not a number in %s field %q", first, name, text)
			}
			b, err2 := strconv.Atoi(strings.TrimSpace(last))
			if err2 != nil {
				return out, fmt.Errorf("cron: %q is not a number in %s field %q", last, name, text)
			}
			lo, hi = a, b
		default:
			v, err := strconv.Atoi(body)
			if err != nil {
				return out, fmt.Errorf("cron: %q is not a valid %s value", body, name)
			}
			lo = v
			hi = v
			if hasStep {
				// "5/15" means 5,20,35,50 in the traditional grammar.
				hi = max
			}
		}
		if lo < min || hi > max || lo > hi {
			return out, fmt.Errorf("cron: %s value %q is outside %d-%d", name, part, min, max)
		}
		for v := lo; v <= hi; v += step {
			out.bits |= 1 << uint(v)
		}
	}
	return out, nil
}

// Expression returns the normalised expression.
func (c *Cron) Expression() string {
	if c == nil {
		return ""
	}
	return c.expr
}

// Matches reports whether t falls exactly on a cron slot (seconds are ignored).
func (c *Cron) Matches(t time.Time) bool {
	if c == nil {
		return false
	}
	return c.month.has(int(t.Month())) &&
		c.matchesDay(t) &&
		c.hour.has(t.Hour()) &&
		c.minute.has(t.Minute())
}

// matchesDay applies the day-of-month / day-of-week OR rule.
func (c *Cron) matchesDay(t time.Time) bool {
	dom := c.dom.has(t.Day())
	dow := c.dow.has(int(t.Weekday()))
	switch {
	case c.domStar && c.dowStar:
		return true
	case c.domStar:
		return dow
	case c.dowStar:
		return dom
	default:
		return dom || dow
	}
}

// Next returns the first matching time strictly after t, truncated to the
// minute. It returns the zero time when the expression cannot fire within five
// years (for example "0 0 31 2 *").
func (c *Cron) Next(t time.Time) time.Time {
	if c == nil {
		return time.Time{}
	}
	loc := t.Location()
	next := t.Truncate(time.Minute).Add(time.Minute)
	limit := next.AddDate(5, 0, 0)
	for next.Before(limit) {
		if !c.month.has(int(next.Month())) {
			// Jump to the first minute of the next month.
			next = time.Date(next.Year(), next.Month(), 1, 0, 0, 0, 0, loc).AddDate(0, 1, 0)
			continue
		}
		if !c.matchesDay(next) {
			// Jump to midnight of the following day.
			next = time.Date(next.Year(), next.Month(), next.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 1)
			continue
		}
		if !c.hour.has(next.Hour()) {
			next = time.Date(next.Year(), next.Month(), next.Day(), next.Hour(), 0, 0, 0, loc).Add(time.Hour)
			continue
		}
		if !c.minute.has(next.Minute()) {
			next = next.Add(time.Minute)
			continue
		}
		return next
	}
	return time.Time{}
}

// parseDays reports whether expr is a day-based interval ("1d", "2.5d",
// "1 D") and returns the number of days. Day intervals advance the calendar,
// not a fixed number of hours, so they keep the same wall-clock time across a
// DST transition.
func parseDays(expr string) (float64, bool) {
	body, ok := strings.CutSuffix(strings.TrimSpace(strings.ToLower(expr)), "d")
	if !ok {
		return 0, false
	}
	days, err := strconv.ParseFloat(strings.TrimSpace(body), 64)
	if err != nil || days <= 0 {
		return 0, false
	}
	return days, true
}

// addCalendarDays advances t by whole calendar days, preserving its wall-clock
// time and location across DST, then adds any fractional remainder as a plain
// duration.
func addCalendarDays(t time.Time, days float64) time.Time {
	whole := int(days)
	out := t.AddDate(0, 0, whole)
	if frac := days - float64(whole); frac != 0 {
		out = out.Add(time.Duration(frac * float64(24*time.Hour)))
	}
	return out
}

// ParseInterval parses an interval expression: anything time.ParseDuration
// understands ("30m", "1h30m", "90s") plus a day suffix ("1d", "2.5d").
// Days are returned as 24h durations here for validation and logging; the
// scheduler calls addCalendarDays for day expressions so they survive DST.
func ParseInterval(expr string) (time.Duration, error) {
	text := strings.TrimSpace(strings.ToLower(expr))
	if text == "" {
		return 0, fmt.Errorf("interval: the expression is empty")
	}
	if days, ok := parseDays(text); ok {
		d := time.Duration(days * float64(24*time.Hour))
		if d <= 0 {
			return 0, fmt.Errorf("interval: %q must be greater than zero", expr)
		}
		return d, nil
	}
	d, err := time.ParseDuration(text)
	if err != nil {
		return 0, fmt.Errorf("interval: %q: %w", expr, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("interval: %q must be greater than zero", expr)
	}
	return d, nil
}
