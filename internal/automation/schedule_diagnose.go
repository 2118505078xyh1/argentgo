package automation

import "time"

// 时间窗诊断结果里 Reason 的取值。它们是稳定的中文字符串，调用方可以直接
// 比较而无需依赖数字枚举；取值本身不随入参、时区或环境变化。
const (
	reasonWindowAllowed     = "在允许时段"
	reasonHourOutOfRange    = "小时不在范围"
	reasonWeekdayNotAllowed = "星期不允许"
	reasonZeroTime          = "零时刻"
)

// WindowCheck 是单个时刻相对于一个 TimeWindow 的诊断结果。
// At 是原始时刻（按调用方传入的时区原样保留），InWindow 与 TimeWindow.Contains
// 的判定一致，Reason 给出稳定、可读的中文原因。
type WindowCheck struct {
	At       time.Time
	InWindow bool
	Reason   string
}

// DiagnoseWindow 逐时刻判断 times 是否落入窗口 w，并为每个时刻给出稳定 Reason。
// 输出与 times 一一对应，保持调用方传入的顺序（不排序），因此第 i 个结果对应
// 第 i 个输入时刻；空的 times 返回非 nil 的空切片。
//
// Reason 的判定优先级固定为：零时刻 → 小时越界 → 星期不允许 → 在允许时段。
// 先判小时再判星期，与 TimeWindow 以小时区间为主、Days 为附加过滤的结构一致；
// 当小时与星期同时不满足时稳定地报告"小时不在范围"。
//
// 本函数是纯函数：只读取 w 与 times，不修改入参，也不产生任何副作用。
func DiagnoseWindow(w TimeWindow, times []time.Time) []WindowCheck {
	checks := make([]WindowCheck, 0, len(times))
	for _, at := range times {
		check := WindowCheck{At: at}
		switch {
		case at.IsZero():
			check.Reason = reasonZeroTime
		case !w.allowsHour(at.Hour()):
			check.Reason = reasonHourOutOfRange
		case !w.allowsWeekday(at.Weekday()):
			check.Reason = reasonWeekdayNotAllowed
		default:
			check.InWindow = true
			check.Reason = reasonWindowAllowed
		}
		checks = append(checks, check)
	}
	return checks
}

// NextAllowedInWindow 从 from 出发，按 expr 推进到下一次严格晚于 from、
// 且落入窗口 w 的时刻。它复用 NextInWindow 的确定性推进（传入 nil next 让
// NextInWindow 用 expr 构造 Schedule.Next），因此 expr 无效、from 为零时刻、
// 推进停滞或在步数上限内始终落不进窗口时返回 (零时间, false)。
//
// 本函数是纯函数：from 按值传递，w 与 expr 只读，不修改任何入参。
func NextAllowedInWindow(w TimeWindow, from time.Time, expr string) (time.Time, bool) {
	return NextInWindow(expr, w, from, nil)
}
