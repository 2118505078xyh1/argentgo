package proc

import (
	"fmt"
	"time"
)

// ProcessHealth 是一次进程运行的健康快照。它只由传入的事实值构造，
// 绝不查询真实进程、也不读取宿主时钟，因此可作为纯诊断原语在任意时刻安全生成。
//
// 字段含义：
//   - ID：调用方给出的进程标识；HealthOfResult 中因 Result 不带标识而使用固定标签。
//   - Alive：!TimedOut && !Signaled，表示进程未被超时或信号这两种异常方式终止。
//   - AgeMS：已运行时长（毫秒），负值或零一律钳到 0。
//   - TimedOut：是否因超时被终止。
//   - Signaled：是否因信号被终止。
//   - Summary：可读的中文摘要，格式固定、不含 map 遍历，因而稳定且可复现。
type ProcessHealth struct {
	ID       string
	Alive    bool
	AgeMS    int64
	TimedOut bool
	Signaled bool
	Summary  string
}

// 健康摘要中使用的稳定中文状态词。它们是固定字面值，不随语言环境变化。
const (
	healthAlive          = "存活"
	healthTimedOut       = "已超时"
	healthSignaled       = "已被信号终止"
	healthTimedAndSignal = "已超时且被信号终止"
)

// resultHealthID 是 HealthOfResult 使用的固定标识：proc.Result 本身不携带
// 调用方标识，因此用一个稳定标签占位，避免凭空编造与真实进程相关的身份。
const resultHealthID = "result"

// DescribeProcess 仅依据传入事实构造健康快照，不查进程、不读宿主时钟。
//
// AgeMS 由 now.Sub(started) 得到（<=0 归 0）；Alive = !timedOut && !signaled；
// Summary 为固定格式的中文摘要。函数不修改任何入参，也不对 id 做按字节切分，
// 因此中文等 CJK 标识会原样保留在 ID 与 Summary 中，与编码无关。
func DescribeProcess(id string, started, now time.Time, timedOut, signaled bool) ProcessHealth {
	return newProcessHealth(id, now.Sub(started).Milliseconds(), timedOut, signaled, "")
}

// HealthOfResult 从一次已结束运行的 Result 派生健康快照，纯粹且确定。
//
// 映射规则（均沿用 proc 既有约定，并在注释中写明，便于调用方判断）：
//   - AgeMS 取 d 的毫秒数（<=0 归 0）；调用方通常传入观测到的 r.Duration。
//   - TimedOut：ExitCode == 124，即 GNU timeout 的超时码，与 classifyExitCode 一致。
//   - Signaled：ExitCode < 0，对齐 os.ProcessState.ExitCode 对信号终止返回 -1 的约定。
//   - Truncated 不改变 TimedOut/Signaled，而是作为附注写进 Summary：
//     输出被截断是独立的诊断信号，不应伪装成超时或信号终止。
//   - ID 因 Result 不携带标识而使用固定标签 resultHealthID。
//
// 函数不修改 r，也不读取全局状态；相同的 r 与 d 永远得到相同快照，
// 可被并发调用而无需同步。
func HealthOfResult(r Result, d time.Duration) ProcessHealth {
	timedOut := r.ExitCode == 124
	signaled := r.ExitCode < 0

	extra := fmt.Sprintf("退出码=%d", r.ExitCode)
	if r.Truncated {
		extra += "，输出已截断"
	}
	return newProcessHealth(resultHealthID, d.Milliseconds(), timedOut, signaled, extra)
}

// newProcessHealth 是两个构造函数的公共核心：全部由值构造，不查真实进程。
// ageMS <= 0 一律钳到 0；extra 非空时作为附注追加到摘要末尾。
func newProcessHealth(id string, ageMS int64, timedOut, signaled bool, extra string) ProcessHealth {
	if ageMS <= 0 {
		ageMS = 0
	}
	return ProcessHealth{
		ID:       id,
		Alive:    !timedOut && !signaled,
		AgeMS:    ageMS,
		TimedOut: timedOut,
		Signaled: signaled,
		Summary:  healthSummary(id, ageMS, timedOut, signaled, extra),
	}
}

// healthStatusWord 把 timedOut/signaled 组合映射为稳定的中文状态词。
// 两者同时出现时以"且"连接，保证四种组合各有唯一且互异的描述。
func healthStatusWord(timedOut, signaled bool) string {
	switch {
	case timedOut && signaled:
		return healthTimedAndSignal
	case timedOut:
		return healthTimedOut
	case signaled:
		return healthSignaled
	default:
		return healthAlive
	}
}

// healthSummary 生成固定格式的中文摘要。%q 让 CJK 等可打印 Unicode 在引号中
// 保持可读（不做按字节切分或转义），且相同输入永远得到相同字符串。
func healthSummary(id string, ageMS int64, timedOut, signaled bool, extra string) string {
	summary := fmt.Sprintf("进程%q：%s，已运行%d毫秒", id, healthStatusWord(timedOut, signaled), ageMS)
	if extra != "" {
		summary += "，" + extra
	}
	return summary
}
