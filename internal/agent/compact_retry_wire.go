package agent

// ShouldCompactRetry 是"上下文溢出后压缩并重放"的纯判定向导。它把
// compactAndReplay 中的三道门禁——配置了压缩器、失败类别为上下文溢出、
// 重试预算未用尽——合并为一个无状态判断，供接线点与测试直接复用。
//
// 函数刻意不接受 EnableCompactRetry 开关：该开关是 Options 的公开字段，
// 由 app 层在构造 Options 字面量时直接赋值即可（见 app_execute.go），
// 不需要全局 setter；开关关闭时调用方根本不应走到这里。
//
// overflow 对应 llm.ClassifyFailure(err).Kind == llm.FailureContextOverflow，
// hasCompactor 对应 Options.Compactor != nil，retriesSoFar 对应
// runState.compactRetries。函数只读入参，不压缩、不发事件、不改状态，
// 因此可重复调用且与语言/CJK 文本无关。
func ShouldCompactRetry(retriesSoFar int, overflow bool, hasCompactor bool) bool {
	if !hasCompactor || !overflow {
		return false
	}
	// 与 agentpolicy.DecideCompactRetry 的 maxRetries <= 0 规则保持一致。
	// MaxCompactRetries 当前恒为 1；保留该判断使上限日后调整为 0/负数时
	// 语义仍与真正的决策层一致。
	if MaxCompactRetries <= 0 {
		return false
	}
	return retriesSoFar < MaxCompactRetries
}
