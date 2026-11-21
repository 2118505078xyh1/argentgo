package automation

import "time"

// 错过执行报告里 Reason 的稳定取值。它们是稳定的中文字符串，调用方可以直接
// 比较而无需依赖数字枚举；取值本身不随入参、时区或环境变化。
const (
	reasonMissedSkipped       = "策略跳过错过时隙"
	reasonMissedOutOfWindow   = "不在允许时段"
	reasonMissedRunOnce       = "补跑最早错过时隙"
	reasonMissedRunOnceIdle   = "已由更早时隙补跑"
	reasonMissedRunLatest     = "补跑最近错过时隙"
	reasonMissedRunLatestIdle = "由更近时隙补跑"
)

// MissedRun 是单个错过时隙在给定策略与时间窗下的执行报告。
//
// Slot 为原始时隙（按调用方传入的时区原样保留）；Policy 为实际应用的策略，
// 经 normalizeMissed 规范化（未知值归一为 MissedRunOnce），因此总可以直接与
// MissedSkip/MissedRunOnce/MissedRunLatest 比较；WillRun 表示该时隙是否会触发
// 一次运行；Reason 为稳定、可读的中文原因。
type MissedRun struct {
	Slot    time.Time
	Policy  string
	WillRun bool
	Reason  string
}

// ReportMissedRuns 报告 slots 中每个早于 now 的时隙在策略 policy 与时间窗 w
// 下是否会运行，并附稳定中文原因。输出只包含真正错过的时隙（严格早于 now 且
// 非零时刻；零时刻是无效占位，与 DiagnoseWindow 一致地予以忽略），且保持调用方
// 传入的相对顺序；不排序、不修改入参、无 I/O，是纯函数。
//
// 判定分三步，优先级固定为：策略 → 窗口 → 选中时隙。
//
//  1. 策略：skip 丢弃所有错过时隙，任何时隙都不会运行；run_once 与 run_latest
//     分别只补跑一个时隙，其余不运行。
//  2. 窗口：不在 w.Contains 内的时隙永不运行，原因为 reasonMissedOutOfWindow。
//  3. 选中时隙：run_once 选中窗口内最早（输入中最靠前）的错过时隙，run_latest
//     选中窗口内最近（输入中最靠后、不晚于 now）的错过时隙；只有它 WillRun=true，
//     其余窗口内时隙说明已由谁补跑。没有窗口内时隙时无人运行。
//
// policy 经 normalizeMissed 规范化，因此空串、拼写错误与大小写/空白差异都按
// run_once 处理，与 daemon 的实际行为一致。
func ReportMissedRuns(slots []time.Time, now time.Time, policy string, w TimeWindow) []MissedRun {
	reports := make([]MissedRun, 0, len(slots))
	effective := normalizeMissed(policy)

	// candidate 记录一个真正错过的时隙及其窗口判定，Slot 按值拷贝以保留原时区。
	type candidate struct {
		at       time.Time
		inWindow bool
	}
	candidates := make([]candidate, 0, len(slots))
	for _, slot := range slots {
		if !slot.IsZero() && slot.Before(now) {
			candidates = append(candidates, candidate{at: slot, inWindow: w.Contains(slot)})
		}
	}

	// runIdx 是唯一会运行的候选下标；skip 或没有窗口内候选时保持 -1。
	runIdx := -1
	switch effective {
	case MissedRunOnce:
		for i, c := range candidates {
			if c.inWindow {
				runIdx = i // 第一个窗口内候选（输入顺序即 slot 顺序）
				break
			}
		}
	case MissedRunLatest:
		for i := len(candidates) - 1; i >= 0; i-- {
			if candidates[i].inWindow {
				runIdx = i // 最后一个窗口内候选
				break
			}
		}
	}

	for i, c := range candidates {
		report := MissedRun{Slot: c.at, Policy: effective}
		switch {
		case effective == MissedSkip:
			report.Reason = reasonMissedSkipped
		case !c.inWindow:
			report.Reason = reasonMissedOutOfWindow
		case i == runIdx && effective == MissedRunOnce:
			report.WillRun = true
			report.Reason = reasonMissedRunOnce
		case i == runIdx && effective == MissedRunLatest:
			report.WillRun = true
			report.Reason = reasonMissedRunLatest
		case effective == MissedRunLatest:
			report.Reason = reasonMissedRunLatestIdle
		default:
			report.Reason = reasonMissedRunOnceIdle
		}
		reports = append(reports, report)
	}
	return reports
}
