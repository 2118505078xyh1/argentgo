package automation

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// previewDefaultCount 是 n <= 0 时展示的默认运行次数。
const previewDefaultCount = 3

// SchedulePreview 是自研 shell 调度命令的可展示预览原语。
//
// Expression 为原始调度表达式（按传入值原样保留）；NextRuns 为接下来若干次运行时间
// 按 UTC 归一后的稳定字符串（升序，与 next 的调用顺序一致）；Human 为稳定的中文描述。
// 它零 I/O、无调用方，全部字段均为值语义副本，可被任意前端直接读取或序列化。
type SchedulePreview struct {
	Expression string
	NextRuns   []string
	Human      string
}

// PreviewSchedule 用注入的 next 从 now 出发计算接下来 n 次运行时间，返回可稳定展示的 SchedulePreview。
//
// 语义与约定：
//
//   - n <= 0 时按 previewDefaultCount 处理；因此 NextRuns 最多含该次数的元素。
//   - 每一次迭代都把上一次得到的时刻传给 next，第一次传入 now；调用方通过 next
//     自行决定步长、时区与是否严格晚于入参，本函数不做任何调度解析。
//   - next 返回零值时刻表示「没有更多运行」，循环立即停止；next 为 nil 时同样得到空的 NextRuns。
//   - 运行时间统一以 UTC、time.RFC3339 格式化，因此结果不依赖宿主时区，稳定可比较。
//   - now 与 expr 均为按值传入，next 只是一段逻辑，本函数不改任何入参；CJK 文本
//     全程用 fmt/strings 拼接，不按字节切分，故对中文安全。
//
// 这是纯函数：相同入参与相同 next 行为在任意环境下产生完全一致的结果。
func PreviewSchedule(expr string, now time.Time, next func(time.Time) time.Time, n int) SchedulePreview {
	count := n
	if count <= 0 {
		count = previewDefaultCount
	}

	runs := make([]string, 0, count)
	t := now
	for i := 0; i < count; i++ {
		if next == nil {
			break
		}
		t = next(t)
		if t.IsZero() {
			break
		}
		runs = append(runs, t.UTC().Format(time.RFC3339))
	}

	return SchedulePreview{
		Expression: expr,
		NextRuns:   runs,
		Human:      previewHuman(expr, count),
	}
}

// previewHuman 构造稳定的中文描述。对常见的「每天 HH:MM」五字段 cron，给出更自然的
// 「每天 09:00 起，接下来 3 次」；其余表达式回退到「调度 <表达式>，接下来 N 次」。
// 表达式文本统一折叠空白，避免换行或多余空格破坏单行稳定性；全程使用字符串拼接，
// 不按字节切分，因此对 CJK 安全。
func previewHuman(expr string, count int) string {
	head := "调度 " + strings.Join(strings.Fields(expr), " ")
	if daily, ok := previewDailyTime(expr); ok {
		head = daily
	}
	return fmt.Sprintf("%s，接下来 %d 次", head, count)
}

// previewDailyTime 识别严格的「分 时 * * *」单值形态，返回「每天 HH:MM 起」。
// 只接受两个时间字段为单值且日/月/星期均为 * 的情况，避免把复杂调度误报为每天。
func previewDailyTime(expr string) (string, bool) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return "", false
	}
	if fields[2] != "*" || fields[3] != "*" || fields[4] != "*" {
		return "", false
	}
	minute, err := strconv.Atoi(fields[0])
	if err != nil || minute < 0 || minute > 59 {
		return "", false
	}
	hour, err := strconv.Atoi(fields[1])
	if err != nil || hour < 0 || hour > 23 {
		return "", false
	}
	return fmt.Sprintf("每天 %02d:%02d 起", hour, minute), true
}

// Summary 返回稳定的单行摘要：「<表达式> | <Human> | 下一次: <时间或“无”>」。
// 表达式与描述中的空白都被折叠为单个空格，确保即使含换行也仍为单行；只展示首个
// 运行时间，使摘要长度恒定且不随次数增长。零值预览同样得到稳定结果。
func (p SchedulePreview) Summary() string {
	next := "无"
	if len(p.NextRuns) > 0 {
		next = p.NextRuns[0]
	}
	expr := strings.Join(strings.Fields(p.Expression), " ")
	human := strings.Join(strings.Fields(p.Human), " ")
	return fmt.Sprintf("%s | %s | 下一次: %s", expr, human, next)
}
