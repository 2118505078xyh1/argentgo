package app

import "strings"

// failureSignalsEnv 是「失败信号接入压缩证据」的独立门控变量。
const failureSignalsEnv = "ARGENT_FAILURE_SIGNALS"

// failureSignalsEnabled 报告是否启用失败信号压缩证据。默认关闭：
// 只有 ARGENT_FAILURE_SIGNALS=1 才启用，其他值压缩路径逐字节不变。
func failureSignalsEnabled(env func(string) string) bool {
	return env != nil && strings.TrimSpace(env(failureSignalsEnv)) == "1"
}
