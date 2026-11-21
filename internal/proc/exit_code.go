package proc

// 退出码的语义分类标签。上层拿到这些稳定的字符串即可决定如何向用户呈现
// 一次子进程运行的结局，而不必各自重复 130/124 等 shell 约定。
const (
	exitSuccess     = "success"
	exitInterrupted = "interrupted"
	exitTimeout     = "timeout"
	exitError       = "error"
	exitSignal      = "signal"
)

// classifyExitCode 把子进程的退出码与终止信号映射为一个稳定的语义分类。
//
// 判定优先级从高到低，明确且互不重叠：
//
//  1. signal 非空 → "signal"。显式的信号终止是对进程结局最强的描述，
//     它先于任何数字退出码生效；对信号串只判断"是否为空"，不做按字节的
//     切分、取首字节或长度比较，因此中文等多字节（CJK）信号同样安全。
//  2. code == 0 → "success"，进程正常结束。
//  3. code == 130 → "interrupted"，即 128+SIGINT，用户主动中断。
//  4. code == 124 → "timeout"，GNU timeout 在超时时使用的退出码。
//  5. 其余任何非零 code → "error"，通用错误。
//
// 这是纯函数：不读取任何全局状态，相同输入永远得到相同输出，
// 因此可被并发调用且无需同步。
func classifyExitCode(code int, signal string) string {
	switch {
	case signal != "":
		return exitSignal
	case code == 0:
		return exitSuccess
	case code == 130:
		return exitInterrupted
	case code == 124:
		return exitTimeout
	default:
		return exitError
	}
}
