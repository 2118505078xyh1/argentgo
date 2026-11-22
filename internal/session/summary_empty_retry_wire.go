package session

import (
	"os"
	"strings"
)

// summaryEmptyRetryEnv 是「空摘要重试」的独立门控变量。
const summaryEmptyRetryEnv = "ARGENT_SUMMARY_EMPTY_RETRY"

// summaryEmptyRetryEnabled 报告是否启用空摘要重试。默认关闭：
// 只有 ARGENT_SUMMARY_EMPTY_RETRY=1 才启用，其他值压缩逐字节不变。
func summaryEmptyRetryEnabled() bool {
	return strings.TrimSpace(os.Getenv(summaryEmptyRetryEnv)) == "1"
}
