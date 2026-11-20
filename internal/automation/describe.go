package automation

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// cronMonthNames and cronWeekdayNames are the English names used by the
// human-readable description. Keeping them here (rather than in the CLI)
// makes the description locale-independent and reusable by any frontend.
var (
	cronMonthNames   = [12]string{"January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"}
	cronWeekdayNames = [7]string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}
)

// CronDescription is a display-ready explanation of a schedule. Summary is a
// short English sentence suitable for a CLI/TUI list, and Next holds the next
// n firing times (in ascending order) strictly after the requested instant.
// A schedule that cannot fire has an empty Next.
type CronDescription struct {
	Expression string
	Summary    string
	Next       []time.Time
}

// String renders the short human-readable summary. Use Next directly when a
// frontend wants to show the upcoming firing times.
func (d CronDescription) String() string { return d.Summary }

// DescribeCron explains a five-field cron expression and computes its next n
// firing times after after. It returns an error for an invalid expression.
// Passing n <= 0 or a zero after skips the time computation, which is useful
// when only the summary is needed (for example when rendering a task list).
//
// The summary is built from the parsed field sets and the next times come from
// the very same scheduler (ParseCron/Next) the daemon uses, so the description
// cannot drift from what will actually run.
func DescribeCron(expr string, after time.Time, n int) (CronDescription, error) {
	c, err := ParseCron(expr)
	if err != nil {
		return CronDescription{}, err
	}
	return CronDescription{
		Expression: c.Expression(),
		Summary:    describeCronSummary(c),
		Next:       (Schedule{Kind: KindCron, Expr: expr}).NextTimes(after, n),
	}, nil
}

// Describe explains a schedule (cron or interval) and computes its next n
// firing times after after. It is the general form of DescribeCron.
func (s Schedule) Describe(after time.Time, n int) (CronDescription, error) {
	kind := s.kind()
	if kind == KindCron {
		return DescribeCron(s.Expr, after, n)
	}
	if kind != KindInterval {
		return CronDescription{}, fmt.Errorf("automation: unknown schedule kind %q (use cron or interval)", s.Kind)
	}
	if _, err := ParseInterval(s.Expr); err != nil {
		return CronDescription{}, err
	}
	return CronDescription{
		Expression: kind + " " + strings.TrimSpace(s.Expr),
		Summary:    "every " + strings.TrimSpace(s.Expr),
		Next:       s.NextTimes(after, n),
	}, nil
}

// NextTimes returns the next n firing times strictly after after, in ascending
// order. It returns nil when n <= 0, after is zero, or the schedule cannot fire.
// Both cron and interval schedules are supported through Schedule.Next.
func (s Schedule) NextTimes(after time.Time, n int) []time.Time {
	if n <= 0 || after.IsZero() {
		return nil
	}
	var out []time.Time
	t := after
	for i := 0; i < n; i++ {
		next := s.Next(t)
		if next.IsZero() || !next.After(t) {
			break
		}
		out = append(out, next)
		t = next
	}
	return out
}

// describeCronSummary renders c as a short English sentence. Common shapes
// ("every minute", "every N minutes", "every day at HH:MM") get a dedicated
// phrase; anything else falls back to a generic but still readable form built
// from the allowed values of each field.
func describeCronSummary(c *Cron) string {
	minuteAll := cronFieldAll(c.minute, minuteMin, minuteMax)
	hourAll := cronFieldAll(c.hour, hourMin, hourMax)
	domAll := cronFieldAll(c.dom, domMin, domMax)
	monthAll := cronFieldAll(c.month, monthMin, monthMax)
	dowAll := cronFieldAll(c.dow, 0, 6)
	minutes := cronFieldValues(c.minute, minuteMin, minuteMax)
	hours := cronFieldValues(c.hour, hourMin, hourMax)

	if minuteAll && hourAll && domAll && monthAll && dowAll {
		return "every minute"
	}

	// "*/N" over a whole hour, with every other field unrestricted.
	if hourAll && domAll && monthAll && dowAll && len(minutes) > 1 {
		if step := minutes[1] - minutes[0]; step > 1 && minutes[0] == 0 &&
			minutes[len(minutes)-1]+step > minuteMax {
			uniform := true
			for i, v := range minutes {
				if v != i*step {
					uniform = false
					break
				}
			}
			if uniform {
				return fmt.Sprintf("every %d minutes", step)
			}
		}
	}

	// "every hour at :MM" on every day is more naturally just "hourly at :MM".
	if hourAll && domAll && dowAll && monthAll && len(minutes) == 1 {
		return fmt.Sprintf("hourly at :%02d", minutes[0])
	}

	// The time-of-day phrase. It always starts with either "at " or "every ".
	var timePart string
	switch {
	case minuteAll && hourAll:
		timePart = "every minute"
	case minuteAll:
		timePart = "every minute during hours " + formatIntList(hours)
	case hourAll && len(minutes) == 1:
		timePart = fmt.Sprintf("every hour at :%02d", minutes[0])
	case hourAll:
		timePart = "at minutes " + formatIntList(minutes) + " of every hour"
	case len(minutes) == 1 && len(hours) == 1:
		timePart = fmt.Sprintf("at %02d:%02d", hours[0], minutes[0])
	case len(minutes) == 1:
		timePart = fmt.Sprintf("at :%02d during hours %s", minutes[0], formatIntList(hours))
	case len(hours) == 1:
		timePart = fmt.Sprintf("at minutes %s during hour %d", formatIntList(minutes), hours[0])
	default:
		timePart = "at minutes " + formatIntList(minutes) + " during hours " + formatIntList(hours)
	}

	// The day phrase, including the Vixie dom/dow OR rule.
	var dayPart string
	switch {
	case domAll && dowAll:
		dayPart = "every day"
	case domAll:
		dayPart = "every " + formatWeekdayList(cronFieldValues(c.dow, 0, 6))
	case dowAll:
		dayPart = "on day " + formatIntList(cronFieldValues(c.dom, domMin, domMax)) + " of the month"
	default:
		dayPart = "on day " + formatIntList(cronFieldValues(c.dom, domMin, domMax)) +
			" or " + formatWeekdayList(cronFieldValues(c.dow, 0, 6))
	}

	parts := []string{dayPart}
	if !monthAll {
		parts = append(parts, "in "+formatMonthList(cronFieldValues(c.month, monthMin, monthMax)))
	}
	sep := " "
	if strings.HasPrefix(timePart, "every ") {
		sep = ", "
	}
	return strings.Join(parts, sep) + sep + timePart
}

// cronFieldAll reports whether f contains every value in [min, max].
func cronFieldAll(f fieldSet, min, max int) bool {
	for v := min; v <= max; v++ {
		if !f.has(v) {
			return false
		}
	}
	return true
}

// cronFieldValues returns the sorted allowed values of f within [min, max].
func cronFieldValues(f fieldSet, min, max int) []int {
	var out []int
	for v := min; v <= max; v++ {
		if f.has(v) {
			out = append(out, v)
		}
	}
	return out
}

// formatIntList renders sorted integers, collapsing consecutive runs into
// ranges ("1-5,8,10-12").
func formatIntList(values []int) string {
	var parts []string
	for i := 0; i < len(values); {
		j := i
		for j+1 < len(values) && values[j+1] == values[j]+1 {
			j++
		}
		if j > i {
			parts = append(parts, fmt.Sprintf("%d-%d", values[i], values[j]))
		} else {
			parts = append(parts, strconv.Itoa(values[i]))
		}
		i = j + 1
	}
	return strings.Join(parts, ",")
}

// formatWeekdayList renders Sunday=0 .. Saturday=6 values as named days.
func formatWeekdayList(values []int) string {
	return formatNamedList(values, 0, cronWeekdayNames[:])
}

// formatMonthList renders 1-based month values as named months.
func formatMonthList(values []int) string {
	return formatNamedList(values, 1, cronMonthNames[:])
}

// formatNamedList renders sorted values using names[value-offset], collapsing
// consecutive runs into named ranges ("Monday-Friday", "January-March").
func formatNamedList(values []int, offset int, names []string) string {
	var parts []string
	for i := 0; i < len(values); {
		j := i
		for j+1 < len(values) && values[j+1] == values[j]+1 {
			j++
		}
		if j > i {
			parts = append(parts, names[values[i]-offset]+"-"+names[values[j]-offset])
		} else {
			parts = append(parts, names[values[i]-offset])
		}
		i = j + 1
	}
	return strings.Join(parts, ",")
}
