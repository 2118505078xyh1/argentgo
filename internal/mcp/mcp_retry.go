package mcp

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"time"
)

// 本文件给 MCP 远程调用增加一个选择加入的有界重试层。
//
// 默认 maxAttempts=1：只调用一次、不等待、不改动任何错误文本或请求序列，
// 与历史行为逐字节一致。只有显式把 ARGENT_MCP_RETRY_MAX_ATTEMPTS 设为
// 2 或 3 时，Initialize / tools-list / resources-list 这类幂等发现调用
// 才会对短暂故障（502/503/504、网络超时）做指数退避重试。
// tools/call 可能改变服务器状态，绝不自动重试。

const (
	// mcpRetryAttemptsEnv 是重试次数的选择加入开关。
	mcpRetryAttemptsEnv = "ARGENT_MCP_RETRY_MAX_ATTEMPTS"
	// maxMCPRetryAttempts 限制退避预算，避免错误配置造成长尾延迟。
	maxMCPRetryAttempts = 3

	// mcpRetryBaseDelay 是第一次重试前的基准等待；每次尝试翻倍，
	// 但不超过 mcpRetryMaxDelay。
	mcpRetryBaseDelay = 25 * time.Millisecond
	mcpRetryMaxDelay  = 200 * time.Millisecond
)

// retryAttempts 返回本次调用允许的最大尝试次数。
// 未设置、无效值或非正数都回退到 1（即关闭重试）。
func retryAttempts() int {
	raw := strings.TrimSpace(os.Getenv(mcpRetryAttemptsEnv))
	if raw == "" {
		return 1
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 1 {
		return 1
	}
	if n > maxMCPRetryAttempts {
		return maxMCPRetryAttempts
	}
	return n
}

// CallWithRetry 调用 call，并仅对 IsRetryable 判定的短暂失败做退避重试。
//
// maxAttempts 被钳制到 [1, 3]；为 1 时不等待、只调用一次。每次重试前
// 都检查 ctx：调用方取消或超时后立即带着 ctx.Err() 返回，绝不再发请求。
// 返回的原始值与错误始终来自最后一次实际调用（ctx 提前结束除外）。
func CallWithRetry(ctx context.Context, maxAttempts int, call func(context.Context) (json.RawMessage, error)) (json.RawMessage, error) {
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	if maxAttempts > maxMCPRetryAttempts {
		maxAttempts = maxMCPRetryAttempts
	}
	if ctx == nil {
		ctx = context.Background()
	}

	var (
		raw json.RawMessage
		err error
	)
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			// 等待本身也感知取消；ctx 已结束时连一次额外调用都不发。
			if waitErr := sleepRetry(ctx, retryDelay(attempt-1)); waitErr != nil {
				return nil, waitErr
			}
		}
		raw, err = call(ctx)
		if err == nil {
			return raw, nil
		}
		// 永久错误不重试；最后一次尝试耗尽也不再等待。
		if attempt == maxAttempts-1 || !IsRetryable(err) {
			return nil, err
		}
	}
	return raw, err
}

// callWithRetry 是客户端传输调用的包装：默认只走一次 c.tr.call，
// 选择加入后才会对可重试错误重复同一方法调用。
func (c *client) callWithRetry(ctx context.Context, method string, params any) (json.RawMessage, error) {
	return CallWithRetry(ctx, retryAttempts(), func(ctx context.Context) (json.RawMessage, error) {
		return c.tr.call(ctx, method, params)
	})
}

// retryDelay 返回第 attempt 次重试（从 0 开始）前的退避时长。
// 抖动落在 [0.5, 1) 倍区间：只缩短、不放大等待，因此不会突破上限，
// 同时打散多个调用方同时重试的洪峰。
func retryDelay(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	delay := mcpRetryBaseDelay
	for i := 0; i < attempt; i++ {
		delay *= 2
		if delay >= mcpRetryMaxDelay {
			return mcpRetryMaxDelay
		}
	}
	jitter := 0.5 + 0.5*rand.Float64()
	d := time.Duration(float64(delay) * jitter)
	if d <= 0 {
		return 0
	}
	if d > mcpRetryMaxDelay {
		return mcpRetryMaxDelay
	}
	return d
}

// sleepRetry 等待 d，但 ctx 提前结束会立即返回 ctx.Err()。
func sleepRetry(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
