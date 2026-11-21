package automation

import "time"

// nextInWindowMaxSteps 限制 NextInWindow 的推进步数：注入的 next 若停滞、
// 循环或窗口永不匹配，循环必须在有界步数内退出，不能挂住调用方。
const nextInWindowMaxSteps = 10_000

// TimeWindow 是触发时间必须落入的挂钟时间窗，按 t 自身的时区解释。
//
// StartHour == EndHour 表示全天（即不对小时设限），因此零值 TimeWindow
// 不施加任何约束；StartHour > EndHour 表示跨午夜窗口，例如 {22, 6, nil}
// 覆盖 22:00 到次日 06:00 之前。窗口上界为开区间。
// Days 为空表示每天都允许，否则按 time.Weekday（0 为星期日）过滤。
// StartHour 与 EndHour 取值应在 0..23 之间。
type TimeWindow struct {
	StartHour int
	EndHour   int
	Days      []time.Weekday
}

// Contains 报告 t 是否落在窗口内。它只读取 t 的挂钟小时与星期，
// 不做任何文本或区域设置相关的处理，因此与 CJK 等非拉丁环境无关。
func (w TimeWindow) Contains(t time.Time) bool {
	if t.IsZero() {
		return false
	}
	return w.allowsWeekday(t.Weekday()) && w.allowsHour(t.Hour())
}

// allowsWeekday 应用星期过滤；Days 为空表示每天。
func (w TimeWindow) allowsWeekday(day time.Weekday) bool {
	if len(w.Days) == 0 {
		return true
	}
	for _, allowed := range w.Days {
		if allowed == day {
			return true
		}
	}
	return false
}

// allowsHour 应用小时过滤。起点等于终点视为全天，使零值窗口匹配所有时间。
func (w TimeWindow) allowsHour(hour int) bool {
	switch {
	case w.StartHour == w.EndHour:
		return true
	case w.StartHour < w.EndHour:
		return hour >= w.StartHour && hour < w.EndHour
	default:
		// 跨午夜：[StartHour, 24) 与 [0, EndHour) 的并集。
		return hour >= w.StartHour || hour < w.EndHour
	}
}

// NextInWindow 从 now 出发用 next 反复推进，返回第一个落入 w 的时间。
// 即使 now 已在窗口内也会先推进一次，与 Schedule.Next 的“严格晚于”语义一致。
//
// next 为 nil 时退化为用 expr 构造 Schedule 并调用其 Next；此时 expr 无效
// 会立刻返回 (零值, false)。返回 false 的情形包括：next 返回零时间、停滞或
// 倒退（无法保证前进），以及推进超过 nextInWindowMaxSteps 步仍未落入窗口。
// expr 在 next 非空时不参与计算，因此不受其中是否含 CJK 字符影响。
func NextInWindow(expr string, w TimeWindow, now time.Time, next func(time.Time) time.Time) (time.Time, bool) {
	if now.IsZero() {
		return time.Time{}, false
	}
	advance := next
	if advance == nil {
		schedule := Schedule{Expr: expr}
		advance = schedule.Next
	}
	prev := now
	for i := 0; i < nextInWindowMaxSteps; i++ {
		cur := advance(prev)
		if cur.IsZero() || !cur.After(prev) {
			return time.Time{}, false
		}
		if w.Contains(cur) {
			return cur, true
		}
		prev = cur
	}
	return time.Time{}, false
}
