package app

import "strings"

// failureSignalsEnv 是「失败信号接入压缩证据」的独立门控变量。
const failureSignalsEnv = "ARGENT_FAILURE_SIGNALS"

// failureSignalsEnabled 报告是否启用失败信号压缩证据。默认开启，仅当
// ARGENT_FAILURE_SIGNALS=0（允许首尾空白）时紧急关闭。
func failureSignalsEnabled(env func(string) string) bool {
	return env == nil || strings.TrimSpace(env(failureSignalsEnv)) != "0"
}
