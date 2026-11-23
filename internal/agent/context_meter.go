package agent

// 本文件实现 TODO #104 的纯计算原语：用提供方实测 token 锚点校准文本估算、
// 预留安全余量、以及溢出后逐级缩减窗口。全部是 stdlib-only 纯函数，默认
// 不接线时零调用；opt-in 由 app 层决定（agent 不依赖会话或配置）。

const (
	// calibrationMaxFactor 是 underestimateFactor 的上限（学自 cline）。
	calibrationMaxFactor = 4
	// reserveSharePercent 与 reserveMinTokens 是预留余量（学自 opencode 的
	// RESERVE_SHARE=0.15 / RESERVE_MIN=16000）。
	reserveSharePercent = 15
	reserveMinTokens    = 16000
)

// shrinkSteps 是溢出后逐级重发的窗口比例（学自 opencode 的
// SHRINK_STEPS=[0.85,0.7,0.5,0.35]）。以整数百分比表达，避免浮点漂移。
var shrinkSteps = [...]int{85, 70, 50, 35}

// CalibrationFactor 返回"实测 tokens / 文本估算 tokens"的低估系数，钳到 [1,4]。
// 实测或估算非正时返回 1（表示无需校准）。例如实测 120k / 估算 60k → 2；
// 实测 300k / 估算 60k → 钳到 4；实测小于估算 → 1。
func CalibrationFactor(measured, estimated int) int {
	if measured <= 0 || estimated <= 0 {
		return 1
	}
	f := measured / estimated
	if f < 1 {
		return 1
	}
	if f > calibrationMaxFactor {
		return calibrationMaxFactor
	}
	return f
}

// CalibratedEstimate 按系数放大估算值（整数运算，无溢出时精确）。
// factor<=1 时原样返回。
func CalibratedEstimate(estimate, factor int) int {
	if factor <= 1 || estimate <= 0 {
		return estimate
	}
	return estimate * factor
}

// ReservedCeiling 返回扣除安全余量后的可用上下文上限：
// ceiling = window - max(window*0.15, 16000)，并保证结果至少为 window 的一半。
// 非正窗口返回 0。
func ReservedCeiling(window int) int {
	if window <= 0 {
		return 0
	}
	reserve := window * reserveSharePercent / 100
	if reserve < reserveMinTokens {
		reserve = reserveMinTokens
	}
	if reserve >= window {
		return window / 2
	}
	return window - reserve
}

// ShrinkWindow 按第 step 级比例缩小窗口（step 从 0 开始）；越界时原样返回。
func ShrinkWindow(window, step int) int {
	if step < 0 || step >= len(shrinkSteps) {
		return window
	}
	return window * shrinkSteps[step] / 100
}

// ShrinkStepCount 返回逐级缩减的级数。
func ShrinkStepCount() int { return len(shrinkSteps) }
