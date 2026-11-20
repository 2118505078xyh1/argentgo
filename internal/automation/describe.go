package automation

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// cronMonthNames 和 cronWeekdayNames 是易读描述中使用的英文名称。把它们保留在这里（而非 CLI）
// 易读描述中使用的英文名称。把它们保留在这里（而非 CLI）
// 使描述与语言环境无关且可被任何前端复用。
var (
	cronMonthNames   = [12]string{"January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"}
	cronWeekdayNames = [7]string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}
)

// 使描述与语言环境无关且可被任何前端复用。
// CronDescription 是调度的可直接展示的说明。Summary 是适合 CLI/TUI 列表的简短英文句子，Next 保存严格晚于请求时刻的
// 接下来 n 个触发时间（升序）。
// 无法触发的调度其 Next 为空。
type CronDescription struct {
	Expression string
	Summary    string
	Next       []time.Time
}

// String 渲染简短易读的摘要。当前端想显示接下来的触发时间时直接使用 Next。
// 当前端想显示接下来的触发时间时直接使用 Next。
func (d CronDescription) String() string { return d.Summary }

// DescribeCron 解释五字段 cron 表达式并计算其在此之后的接下来 n 个触发时间。表达式无效时返回错误。
// 触发时间。表达式无效时返回错误。
// 传入 n <= 0 或零 after 会跳过时间计算，这在只需要摘要时很有用（例如渲染任务列表时）。
// 只需要摘要时很有用（例如渲染任务列表时）。
//
// 摘要由解析后的字段集合构建，而接下来的时间来自
// daemon 所使用的同一个调度器（ParseCron/Next），因此描述不会偏离实际会运行的内容。
// 描述不会偏离实际会运行的内容。
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

// Describe 解释一个调度（cron 或间隔）并计算其在此之后的接下来 n 个触发时间。它是 DescribeCron 的通用形式。
// 触发时间。它是 DescribeCron 的通用形式。
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

// NextTimes 严格返回 after 之后接下来 n 个触发时间，按升序
// NextTimes 严格返回 after 之后接下来 n 个触发时间，按升序排列。当 n <= 0、after 为零或调度无法触发时返回 nil。
// cron 和间隔调度都通过 Schedule.Next 支持。
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

// describeCronSummary 把 c 渲染为简短英文句子。常见形态
// describeCronSummary 把 c 渲染为简短英文句子。常见形态（"every minute"、"every N minutes"、"every day at HH:MM"）有专用
// 短语；其他情况回退到由允许值构建的通用但仍可读形式。
// 短语；其他情况回退到由每个字段允许值构建的通用但仍可读的形式。
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

	// 整小时上的 "*/N"，且其他字段均不受限。
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

	// 每天 "every hour at :MM" 更自然地写作 "hourly at :MM"。
	if hourAll && domAll && dowAll && monthAll && len(minutes) == 1 {
		return fmt.Sprintf("hourly at :%02d", minutes[0])
	}

	// 时刻短语。它总是以 "at " 或 "every " 开头。
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

	// 日期短语，包括 Vixie 的 dom/dow OR 规则。
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

// cronFieldAll 报告 f 是否包含 [min, max] 中的每个值。
func cronFieldAll(f fieldSet, min, max int) bool {
	for v := min; v <= max; v++ {
		if !f.has(v) {
			return false
		}
	}
	return true
}

// cronFieldValues 返回 f 在 [min, max] 内的已排序允许值。
func cronFieldValues(f fieldSet, min, max int) []int {
	var out []int
	for v := min; v <= max; v++ {
		if f.has(v) {
			out = append(out, v)
		}
	}
	return out
}

// formatIntList 渲染已排序整数，把连续段折叠为
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

// formatWeekdayList 把 Sunday=0 .. Saturday=6 的值渲染为具名星期。
func formatWeekdayList(values []int) string {
	return formatNamedList(values, 0, cronWeekdayNames[:])
}

// formatMonthList 把 1 基月份值渲染为具名月份。
func formatMonthList(values []int) string {
	return formatNamedList(values, 1, cronMonthNames[:])
}

// formatNamedList 用 names[value-offset] 渲染已排序值，把连续段折叠为
// 具名范围（"Monday-Friday"、"January-March"）。
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
