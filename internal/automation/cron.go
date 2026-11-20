package automation

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cron 是解析后的五字段 cron 表达式：
//
//	minute hour day-of-month month day-of-week
//
// 每个字段接受 `*`、`n` 单值、范围 `a-b`、步长 `*/n`、
// 列表 `a,b,c` 和带步长的范围 `a-b/n`。星期 0 是星期日，7 也
// 被接受为星期日。时间按传给 Next 的 time 值的位置求值。
//
// 日和星期遵循传统的 Vixie 规则：当两者都
// 日和星期遵循传统的 Vixie 规则：当两者都受限（都不以 `*` 开头）时，任一字段匹配即匹配，否则受限字段必须匹配。
// 否则受限字段必须匹配。
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

// fieldSet 是允许值的位图；cron 字段始终小于 64。
type fieldSet struct {
	bits uint64
}

func (f fieldSet) has(value int) bool {
	if value < 0 || value > 63 {
		return false
	}
	return f.bits&(1<<uint(value)) != 0
}

// Cron 字段边界。星期按 0-7 解析以接受 7（=星期日），然后折叠到 0。
// 接受 7（=星期日），然后折叠到 0。
const (
	minuteMin, minuteMax = 0, 59
	hourMin, hourMax     = 0, 23
	domMin, domMax       = 1, 31
	monthMin, monthMax   = 1, 12
	dowMin, dowMax       = 0, 7
)

// ParseCron 解析五字段 cron 表达式。
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
	// 把 7 折叠到 0，使星期日的两种拼写都匹配。
	if c.dow.has(7) {
		c.dow.bits |= 1
		c.dow.bits &^= 1 << 7
	}
	c.domStar = strings.HasPrefix(fields[2], "*")
	c.dowStar = strings.HasPrefix(fields[4], "*")
	return c, nil
}

// parseCronField 把一个字段展开为位图。
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
				// "5/15" 在传统语法中表示 5,20,35,50。
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

// Expression 返回规范化后的表达式。
func (c *Cron) Expression() string {
	if c == nil {
		return ""
	}
	return c.expr
}

// Matches 报告 t 是否恰好落在某个 cron 时隙（忽略秒）。
func (c *Cron) Matches(t time.Time) bool {
	if c == nil {
		return false
	}
	return c.month.has(int(t.Month())) &&
		c.matchesDay(t) &&
		c.hour.has(t.Hour()) &&
		c.minute.has(t.Minute())
}

// matchesDay 应用日/星期的 OR 规则。
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

// Next 返回严格晚于 t 的第一个匹配时间，截断到
// 分钟。当表达式在五年内无法触发时返回零时间
// Next 返回严格晚于 t 的第一个匹配时间，截断到分钟。当表达式在五年内无法触发时返回零时间（例如 "0 0 31 2 *"）。
func (c *Cron) Next(t time.Time) time.Time {
	if c == nil {
		return time.Time{}
	}
	next := t.Truncate(time.Minute).Add(time.Minute)
	limit := next.AddDate(5, 0, 0)
	for next.Before(limit) {
		if !c.month.has(int(next.Month())) {
			// 跳到下个月的第一分钟。
			next = nextMonthStart(next)
			continue
		}
		if !c.matchesDay(next) {
			// 跳到下个月的第一分钟。
			next = nextDayStart(next)
			continue
		}
		if !c.hour.has(next.Hour()) {
			// 跳到第二天的午夜。
			// 用时长运算而非同一挂钟小时的 time.Date 跳到下一个挂钟小时的起点。在 DST
			// 回拨期间本地小时出现两次，time.Date 解析为较早的那次，因此在这里重建会一直落在同一个被排除的小时上。加上剩余分钟
			// 较早的那次，因此在这里重建会一直落在
			// 同一个被排除的小时上。加上剩余分钟
			// 以绝对时间前进，最多两步后终止
			// 以绝对时间前进，最多两步后终止（重复小时的每个出现各一次）。
			next = next.Add(time.Hour - time.Duration(next.Minute())*time.Minute)
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

// nextDayStart 返回 t 之后本地那一天的第一个时刻。
// nextMonthStart 返回 t 之后本地那一个月的第一个时刻。
//
// 两者都用 time.Date 重建挂钟边界，当该时区
// 两者都用 time.Date 重建挂钟边界，当该时区在午夜切换 DST 时这不安全：不存在的 00:00 会被向后规范化到
// 前一天（例如 America/Havana 的 2026-03-08 00:00 变成 2026-03-07 23:00）。前进到这样的候选可能根本无法向前移动，
// 2026-03-07 23:00）。前进到这样的候选可能根本无法向前移动，
// 于是 Next 会永远在同一个被排除的日期上打转。回退到绝对整点步进使每次迭代在真实时间上严格递增，这既终止循环，
// 绝对整点步进使每次迭代在真实时间上严格递增，这
// 既终止循环又保留了 Vixie 风格的规则：
// 又保留了 Vixie 风格的规则：因 DST 失去午夜的那一天没有触发时隙。
func nextDayStart(t time.Time) time.Time {
	y, m, d := t.Date()
	return forwardOrNextHour(t, time.Date(y, m, d+1, 0, 0, 0, 0, t.Location()))
}

func nextMonthStart(t time.Time) time.Time {
	return forwardOrNextHour(t, time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, t.Location()))
}

// forwardOrNextHour 当 candidate 严格晚于 t 时返回它，否则绝对步进到下一小时的整点。后者总是向前移动
// 绝对步进到下一小时的整点。后者总是向前移动
// （至少一分钟），且与 Next 的小时分支用来越过重复或跳过挂钟小时的运算相同。
// （至少一分钟），且与 Next 的小时分支用来越过重复或跳过的挂钟小时的运算相同。
func forwardOrNextHour(t, candidate time.Time) time.Time {
	if candidate.After(t) {
		return candidate
	}
	return t.Add(time.Hour - time.Duration(t.Minute())*time.Minute)
}

// parseDays 报告 expr 是否为基于日的间隔（"1d"、"2.5d"、"1 D"）并返回天数。日间隔推进日历
// "1 D"）并返回天数。日间隔推进日历
// 而非固定的小时数，因此在 DST 过渡中保持相同的挂钟时间。
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

// addCalendarDays 按整个日历日推进 t，在 DST 中保持其挂钟时间和位置，然后把任何小数余量作为普通
// 时间和位置，然后把任何小数余量作为普通的
// duration.
func addCalendarDays(t time.Time, days float64) time.Time {
	whole := int(days)
	out := t.AddDate(0, 0, whole)
	if frac := days - float64(whole); frac != 0 {
		out = out.Add(time.Duration(frac * float64(24*time.Hour)))
	}
	return out
}

// ParseInterval 解析间隔表达式：time.ParseDuration
// ParseInterval 解析间隔表达式：time.ParseDuration 能理解的任何内容（"30m"、"1h30m"、"90s"）加上日后缀（"1d"、"2.5d"）。
// 这里天数作为 24h 时长返回，用于验证和日志；
// 这里天数作为 24h 时长返回，用于验证和日志；调度器对日表达式调用 addCalendarDays，使它们能经受 DST。
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
