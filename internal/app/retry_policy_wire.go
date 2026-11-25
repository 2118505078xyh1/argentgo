package app

import (
	"os"
	"strconv"
	"strings"
	"time"

	"example.com/argentgo/internal/llm"
)

// 本文件把重试策略的三个旋钮接到环境变量，使高延迟/严格限流 provider
// 无需改代码即可调大尝试次数与退避。全部未设置时返回零值 RetryPolicy，
// llm 客户端会回退到既有默认策略（线字节与接线前逐字节一致）。

const (
	retryAttemptsEnv  = "ARGENT_RETRY_ATTEMPTS"
	retryBaseDelayEnv = "ARGENT_RETRY_BASE_DELAY_MS"
	retryMaxDelayEnv  = "ARGENT_RETRY_MAX_DELAY_MS"
)

// parseRetryPolicy 是纯函数：从注入的 env 读取三个旋钮，非法/非正值被忽略
// （保持默认），便于表驱动测试与诊断复用。
func parseRetryPolicy(env func(string) string) llm.RetryPolicy {
	var p llm.RetryPolicy
	positive := func(key string) int {
		n, err := strconv.Atoi(strings.TrimSpace(env(key)))
		if err != nil || n <= 0 {
			return 0
		}
		return n
	}
	if n := positive(retryAttemptsEnv); n > 0 {
		p.MaxAttempts = n
	}
	if ms := positive(retryBaseDelayEnv); ms > 0 {
		p.BaseDelay = time.Duration(ms) * time.Millisecond
	}
	if ms := positive(retryMaxDelayEnv); ms > 0 {
		p.MaxDelay = time.Duration(ms) * time.Millisecond
	}
	return p
}

func retryPolicyFromEnv() llm.RetryPolicy {
	return parseRetryPolicy(os.Getenv)
}
