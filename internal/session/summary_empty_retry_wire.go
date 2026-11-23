package session

import (
	"os"
	"strings"
)

// summaryEmptyRetryEnv 是「空摘要重试」的独立门控变量。
const summaryEmptyRetryEnv = "ARGENT_SUMMARY_EMPTY_RETRY"

// summaryEmptyRetryEnabled 报告是否启用空摘要重试。默认开启：
// 取值空或不为 "0" 时启用，只有显式设为 "0" 才关闭。
func summaryEmptyRetryEnabled() bool {
	return strings.TrimSpace(os.Getenv(summaryEmptyRetryEnv)) != "0"
}
