package app

import (
	"os"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/agent"
)

// 本文件实现 TODO #104 的 app 层 opt-in 接线：把提供方实测 token 锚点与
// 文本估算的比率作为有上限的校准系数，避免本地模型/大工具输出下估算误差
// 导致过早或过晚压缩。默认开启，ARGENT_CONTEXT_CALIBRATION=0（允许首尾空白）
// 时紧急关闭，回落到未校准的阈值与估算比较。

const contextCalibrationEnv = "ARGENT_CONTEXT_CALIBRATION"

func contextCalibrationEnabled() bool {
	return contextCalibrationEnabledFrom(nil)
}

// contextCalibrationEnabledFrom 是 per-run env 变体：env 非 nil 时以它为准，
// 否则回退到进程环境。Execute 装配循环时传入 stdio.Env，使多表面各自的
// ARGENT_CONTEXT_CALIBRATION 设置真正生效（此前只读进程 env 会被忽略）。
func contextCalibrationEnabledFrom(env func(string) string) bool {
	if env == nil {
		env = os.Getenv
	}
	return strings.TrimSpace(env(contextCalibrationEnv)) != "0"
}

// compactionDecisionCalibrated 是 compactionDecision 的校准变体：
// 用实测/估算比率（钳到 [1,4]）放大估算值后再做 1.3 倍膨胀与阈值比较。
// 实测或估算非正时退化为原始决策，保证无锚点会话行为不变。
func compactionDecisionCalibrated(estimate, window, configured, percent, measured int) (threshold int, compact bool) {
	threshold = agent.CompactionThreshold(window, configured, percent)
	if threshold <= 0 {
		return threshold, false
	}
	if measured <= 0 || estimate <= 0 {
		return threshold, agent.InflateEstimate(estimate) >= threshold
	}
	factor := agent.CalibrationFactor(measured, estimate)
	adjusted := agent.CalibratedEstimate(estimate, factor)
	return threshold, agent.InflateEstimate(adjusted) >= threshold
}

// calibratedThreshold 返回预留余量后的有效阈值（供观测/测试使用）：
// 把固定阈值与 provider 上下文窗口的 ReservedCeiling 取较小者。
func calibratedThreshold(threshold, window int) int {
	ceiling := agent.ReservedCeiling(window)
	if ceiling <= 0 || threshold <= 0 {
		return threshold
	}
	if threshold < ceiling {
		return threshold
	}
	return ceiling
}
