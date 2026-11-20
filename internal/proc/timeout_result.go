package proc

import (
	"context"
	"errors"
	"strconv"
	"time"
)

// timeoutResult 把一次运行的耗时、时间上限和错误汇总成结构化字典，
// 让工具层在超时时返回机器可读的字段，而不是只有一条裸错误字符串。
//
// timed_out 由 err 是否包装 context.DeadlineExceeded 决定，与
// ClassifyCancel 的超时判定保持一致；elapsed 与 limit 的负值一律钳到零，
// 因此毫秒字段不会出现负号，也不会因下溢而变成很大的正数。message
// 原样保留 err.Error()，其中包含中文等多字节字符时不会被截断或破坏。
func timeoutResult(elapsed, limit time.Duration, err error) map[string]string {
	if elapsed < 0 {
		elapsed = 0
	}
	if limit < 0 {
		limit = 0
	}

	message := ""
	if err != nil {
		message = err.Error()
	}

	return map[string]string{
		"timed_out":  strconv.FormatBool(errors.Is(err, context.DeadlineExceeded)),
		"elapsed_ms": strconv.FormatInt(elapsed.Milliseconds(), 10),
		"limit_ms":   strconv.FormatInt(limit.Milliseconds(), 10),
		"message":    message,
	}
}

// remainingBudget 计算多阶段共享截止时间下留给下一阶段的预算。
// 调用方应把上一阶段已经耗掉的 elapsed 从总预算 total 中扣除后再透传，
// 而不是为每个阶段重新计时。结果为非正数时返回 0，确保预算已经耗尽的
// 阶段不会拿到一个正数而重启时钟；total 或 elapsed 为负也按此规则处理。
func remainingBudget(elapsed, total time.Duration) time.Duration {
	if elapsed < 0 {
		elapsed = 0
	}
	if total < 0 {
		total = 0
	}
	remaining := total - elapsed
	if remaining <= 0 {
		return 0
	}
	return remaining
}
