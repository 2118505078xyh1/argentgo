package proc

import "fmt"

// ExitClass 是一次子进程运行结局的"首因"分类标签。它借鉴 dsh 的做法：
// 当多个异常信号同时出现时（例如超时后杀树，进程最终被信号终止），
// 只取最根本的那一个原因，而不是把表征（信号、退出码）当成结局。
//
// 它是 string 的具名类型，取值来自下面六个稳定常量；上层据此决定如何
// 向用户呈现一次运行，无需各自重复实现那条判断链。
type ExitClass string

// 六种稳定的分类标签，字面值与 dsh 的首因词汇表对齐，不随语言环境变化。
const (
	// ExitSuccess 表示进程正常结束（退出码为 0）。
	ExitSuccess ExitClass = "success"
	// ExitFailed 表示进程自行以非零退出码结束。
	ExitFailed ExitClass = "failed"
	// ExitTimeout 表示运行被时间上限终止。
	ExitTimeout ExitClass = "timeout"
	// ExitSignaled 表示进程被信号终止。
	ExitSignaled ExitClass = "signaled"
	// ExitSpawnErr 表示进程根本没能启动（如可执行文件不存在）。
	ExitSpawnErr ExitClass = "spawn-error"
	// ExitAborted 表示运行被显式中止（用户取消）。
	ExitAborted ExitClass = "aborted"
)

// ClassifyExit 按"首因"优先级把一次运行的原始事实归为一个 ExitClass。
//
// 输入分别对应 proc 现有执行路径能观察到的几类事实：
//   - exitCode 对齐 Result.ExitCode；
//   - signal 非空对齐 CancelSignal / 信号终止；
//   - timedOut 对齐 context.DeadlineExceeded（ClassifyCancel 的 CancelTimeout）；
//   - spawnErr 对齐 exec 启动失败（返回 error 但还没有 Result）；
//   - aborted 对齐 context.Canceled（ClassifyCancel 的 CancelUser）。
//
// 判定优先级从高到低，明确且互不重叠：
//
//  1. spawnErr → spawn-error。进程没启动就没有后续结局可谈，是最根本的原因。
//  2. aborted → aborted。用户显式中止先于任何被动的超时或信号。
//  3. timedOut → timeout。超时是杀树的表因，优先于随后观测到的信号。
//  4. signal 非空 → signaled。显式信号终止先于数字退出码；对信号串只判空，
//     不做按字节切分或长度比较，因此中文等多字节信号同样安全。
//  5. exitCode == 0 → success，进程干净退出。
//  6. 其余任何退出码（含负数）→ failed。
//
// 这是纯函数：所有参数都是值或不可变的字符串，不读取全局状态、不修改入参，
// 相同输入永远得到相同输出，可被并发调用而无需同步。
func ClassifyExit(exitCode int, signal string, timedOut, spawnErr, aborted bool) ExitClass {
	switch {
	case spawnErr:
		return ExitSpawnErr
	case aborted:
		return ExitAborted
	case timedOut:
		return ExitTimeout
	case signal != "":
		return ExitSignaled
	case exitCode == 0:
		return ExitSuccess
	default:
		return ExitFailed
	}
}

// IsFailure 报告该分类是否代表一次失败的运行。
// 只有 ExitSuccess 算作成功；其余五种已定义分类（以及任何未知取值）都视为失败，
// 这样即使上游传入了未来新增的标签也不会被误当成成功。
func (c ExitClass) IsFailure() bool {
	return c != ExitSuccess
}

// ExitMarkdown 生成可附加到 shell 输出末尾的稳定标记文本。
// 标记使用 ASCII 方括号，便于在日志中检索，且不会因语言环境变化。
//
// 各分类的返回值：
//   - ExitSuccess → ""（干净退出无需标记）；
//   - ExitFailed → "[failed: N]"，N 为退出码 code；
//   - ExitTimeout → "[timed out]"；
//   - ExitSignaled → "[killed by signal: N]"，N 为信号标识 code；
//   - ExitSpawnErr → "[spawn error]"；
//   - ExitAborted → "[aborted]"；
//   - 任何未知分类 → ""（不产生标记）。
//
// 这是纯函数，不读取全局状态；code 仅以十进制写入标记，不做文本解码，
// 因此对 CJK / 多字节内容无影响。
func ExitMarkdown(class ExitClass, code int) string {
	switch class {
	case ExitSuccess:
		return ""
	case ExitFailed:
		return fmt.Sprintf("[failed: %d]", code)
	case ExitTimeout:
		return "[timed out]"
	case ExitSignaled:
		return fmt.Sprintf("[killed by signal: %d]", code)
	case ExitSpawnErr:
		return "[spawn error]"
	case ExitAborted:
		return "[aborted]"
	default:
		return ""
	}
}
