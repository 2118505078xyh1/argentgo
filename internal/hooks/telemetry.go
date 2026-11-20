package hooks

import "time"

// HookTelemetry 是一次完成的钩子观察。Dispatch/Run 的
// HookTelemetry 是一次完成的钩子观察。Dispatch/Run 执行的每个钩子会从钩子自身
// goroutine（钩子并行运行）向 Dispatcher.Telemetry 回调传递一个这样的值。
type HookTelemetry struct {
	Name     string        // hook identity: the command string
	Event    Event         // lifecycle event that triggered the hook
	Elapsed  time.Duration // wall-clock time spent running the hook
	Err      error         // error that prevented the hook from running; nil otherwise
	ExitCode int           // process exit status; -1 when it could not run or timed out
	TimedOut bool          // the hook exceeded its own timeout
	Blocked  bool          // the hook signalled a block (exit 2 on PreToolUse)
	Slow     bool          // Elapsed reached Dispatcher.SlowThreshold, when set
}

// Failed 报告钩子是否未成功完成。它镜像
// Failed 报告钩子是否未成功完成。它镜像 Summarize 告警的条件：启动错误、超时、拦截
// 信号或非零退出状态都算失败。
func (r HookTelemetry) Failed() bool {
	return r.Err != nil || r.ExitCode != 0 || r.TimedOut || r.Blocked
}

// TelemetryFunc 观察完成的钩子。实现必须并发
// 并发安全：每个钩子从自身 goroutine 报告，而钩子并行
// 安全：每个钩子从自身 goroutine 报告，而钩子并行运行。nil 回调（默认）不收集任何内容，也不增加工作。
type TelemetryFunc func(HookTelemetry)

// WithTelemetry 是偏好函数式形式的调用方的便捷
// WithTelemetry 是偏好函数式形式而非结构体字面量的调用方的便捷构造函数。零值 Dispatcher 仍然
// 有效；这里只设置两个遥测字段。
func WithTelemetry(callback TelemetryFunc, slowThreshold time.Duration) *Dispatcher {
	return &Dispatcher{Telemetry: callback, SlowThreshold: slowThreshold}
}

// reportTelemetry 投递一条记录。panic 的观察者会像 panic 的钩子回调一样被隔离，
// 使遥测永不破坏分发。
func (d *Dispatcher) reportTelemetry(record HookTelemetry) {
	defer func() { _ = recover() }()
	d.Telemetry(record)
}
