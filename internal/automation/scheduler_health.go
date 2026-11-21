package automation

import "fmt"

// 调度健康判定的稳定取值。它们是稳定的字符串，调用方可直接比较而无需依赖
// 数字枚举；取值本身不随入参、时区或环境变化。
const (
	VerdictHealthy = "healthy" // 运行正常，既未过期也无错过与连续失败
	VerdictLate    = "late"    // 出现过期、错过或失败中的一种，但尚未彻底卡死
	VerdictStuck   = "stuck"   // 已停止推进：反复失败，或过期且持续错过
)

const (
	// defaultStaleAfterMS 是 staleAfterMS <= 0 时采用的默认过期阈值（15 分钟）。
	defaultStaleAfterMS int64 = 15 * 60 * 1000
	// stuckFailThreshold 是连续失败达到该数量即判定为彻底卡死的阈值。
	stuckFailThreshold = 3
)

// ScheduleHealth 是单个调度的健康诊断快照。所有字段均为标量，便于按值传递
// 与比较；Verdict 由 AssessSchedule 填充，可直接取值比较。
type ScheduleHealth struct {
	ID              string
	Expression      string
	LastRunMS       int64 // 最近一次运行的 Unix 毫秒时间戳
	Missed          int   // 累计错过执行次数
	ConsecutiveFail int   // 连续失败次数
	Verdict         string
}

// AssessSchedule 根据 nowMS 与 LastRunMS 的差值判定调度是否过期（stale），
// 再结合 Missed 与 ConsecutiveFail 给出 Verdict，返回填充后的副本。
//
// 过期判定：当 nowMS-LastRunMS 严格大于 staleAfterMS 时视为过期；时钟回拨
// （nowMS <= LastRunMS）不视为过期。staleAfterMS <= 0 时采用默认值
// defaultStaleAfterMS。差值溢出时视为过期（真实的超大间隔必然超过阈值）。
//
// Verdict 判定优先级固定为（严重者胜出）：
//  1. stuck：连续失败次数达到 stuckFailThreshold，或过期且存在错过执行；
//  2. late：过期、有错过或有失败中的任意一种；
//  3. healthy：其余情况（运行及时且无错过、无失败）。
//
// 本函数是纯函数：h 与 nowMS 均按值传入，只读且不修改任何入参，不产生副作用；
// Expression（含 CJK 文本）原样保留；相同输入必然得到完全相同的输出。
func AssessSchedule(h ScheduleHealth, nowMS int64, staleAfterMS int64) ScheduleHealth {
	if staleAfterMS <= 0 {
		staleAfterMS = defaultStaleAfterMS
	}
	stale := isScheduleStale(nowMS, h.LastRunMS, staleAfterMS)

	switch {
	case h.ConsecutiveFail >= stuckFailThreshold || (stale && h.Missed > 0):
		h.Verdict = VerdictStuck
	case stale || h.Missed > 0 || h.ConsecutiveFail > 0:
		h.Verdict = VerdictLate
	default:
		h.Verdict = VerdictHealthy
	}
	return h
}

// isScheduleStale 报告 nowMS 相对 lastRunMS 是否已超过阈值。用比较而非直接相减
// 来规避 int64 溢出：nowMS > lastRunMS 却相减为负，说明真实差值超出可表示范围，
// 必然超过阈值，故判为过期。
func isScheduleStale(nowMS, lastRunMS, staleAfterMS int64) bool {
	if nowMS <= lastRunMS {
		return false
	}
	delta := nowMS - lastRunMS
	if delta < 0 { // 相减溢出
		return true
	}
	return delta > staleAfterMS
}

// Summary 生成稳定的中文摘要："任务 <ID>（<表达式>）：判定 <中文>；最近运行
// <LastRunMS> 毫秒；错过 <Missed> 次；连续失败 <ConsecutiveFail> 次"。空 ID 与
// 空表达式分别以固定占位符呈现，未知 Verdict 原样保留以免丢失信息；相同 h 在
// 任意次调用、任意环境下结果完全一致。
func (h ScheduleHealth) Summary() string {
	id := h.ID
	if id == "" {
		id = "未命名任务"
	}
	expr := h.Expression
	if expr == "" {
		expr = "无表达式"
	}
	return fmt.Sprintf("任务 %s（%s）：判定 %s；最近运行 %d 毫秒；错过 %d 次；连续失败 %d 次",
		id, expr, verdictLabel(h.Verdict), h.LastRunMS, h.Missed, h.ConsecutiveFail)
}

// verdictLabel 把 Verdict 映射为稳定的中文标签；未知取值原样返回。
func verdictLabel(verdict string) string {
	switch verdict {
	case VerdictHealthy:
		return "健康"
	case VerdictLate:
		return "滞后"
	case VerdictStuck:
		return "卡死"
	default:
		return verdict
	}
}
