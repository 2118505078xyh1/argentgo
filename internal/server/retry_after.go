package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// 本文件把 HTTP Retry-After 头部收敛成一个可独立验证的纯函数。
//
// 背景：审计指出服务端缺少限流退避能力——上游返回 429 时，客户端
// 需要读取 Retry-After 决定退避时长。这里先把解析能力落地：它不读取
// *http.Response、不修改 handler、不触碰全局状态（时钟仅通过参数注入，
// 对外入口使用 time.Now），因此可在不启动服务器的情况下验证，也能被
// HTTP/SSE、JSON-RPC 与 ACP 复用。接线是可选的，本文件只提供能力。

const (
	// maxRetryAfterSeconds 是退避时长的上限（15 分钟）。与 internal/llm
	// 的 MaxRetryAfter 对齐：恶意或有 bug 的上游不得通过 Retry-After
	// 让一次退避停滞数小时。超过上限的正值会被钳制而不是回退为无提示。
	maxRetryAfterSeconds = 15 * 60
)

// parseRetryAfter 解析 HTTP Retry-After 头部值，返回整秒退避时长与
// 是否解析成功。头部可以是「增量秒数」或「HTTP-date」两种形态：
//
//   - 增量秒数：只接受 ASCII 十进制整数（可带正负号）。CJK、全角数字、
//     小数、单位后缀等非法形态一律回退 (0, false)，且绝不会在 UTF-8
//     字符中间截断；
//   - HTTP-date：用 http.ParseTime 解析，取相对当前时刻的正向间隔并
//     向上取整到整秒；已过期或无法解析的日期回退 (0, false)；
//   - 正值（含正整数溢出）一律钳制到 maxRetryAfterSeconds，避免超大
//     值回绕为负数或把退避推向遥远未来；
//   - 非正数（零、负值、负整数溢出）与空值（含仅空白）回退 (0, false)。
//
// 该函数除了读取时钟（用于 HTTP-date）之外是纯函数：不读全局状态、
// 不执行 IO，对任意字符串安全。需要确定性时钟的调用方可直接使用
// parseRetryAfterAt。
func parseRetryAfter(header string) (seconds int, ok bool) {
	return parseRetryAfterAt(header, time.Now())
}

// parseRetryAfterAt 与 parseRetryAfter 规则一致，但把「当前时刻」作为
// 参数注入，使 HTTP-date 分支可用固定时钟做确定性测试。增量秒数分支与 now 无关。
func parseRetryAfterAt(header string, now time.Time) (seconds int, ok bool) {
	value := strings.TrimSpace(header)
	if value == "" {
		return 0, false
	}

	// 优先按增量秒数解析。ParseInt 只接受 ASCII 十进制与可选符号，
	// 多字节字符（如 CJK、全角数字）会被当作语法错误直接拒绝，
	// 不会拆开 UTF-8 序列。
	if n, err := strconv.ParseInt(value, 10, 64); err == nil {
		if n <= 0 {
			return 0, false
		}
		return clampRetryAfterSeconds(n), true
	} else if errors.Is(err, strconv.ErrRange) && !strings.HasPrefix(value, "-") {
		// 正整数超出 int64 范围，语义上仍是「极大正数」，
		// 钳制到上限；带负号（含溢出）则按负值回退。
		return maxRetryAfterSeconds, true
	}

	// 再按 HTTP-date 解析。http.ParseTime 覆盖 RFC1123/RFC850/ANSIC
	// 等 HTTP 日期格式；CJK 与非日期文本都会在这里失败。
	when, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	remaining := when.Sub(now)
	if remaining <= 0 {
		return 0, false
	}
	// 向上取整到整秒并保证至少 1 秒，避免把仍在未来、但不足一秒的
	// 日期压成 0 而被误判为「无提示」。Sub 对极远日期会饱和，
	// 这里再做一次上限钳制即可。
	secs := (remaining + time.Second - 1) / time.Second
	if secs < 1 {
		secs = 1
	}
	return clampRetryAfterSeconds(int64(secs)), true
}

// clampRetryAfterSeconds 把 int64 秒数钳制到 [1, maxRetryAfterSeconds]。
// 调用方保证传入值大于 0。
func clampRetryAfterSeconds(n int64) int {
	if n > int64(maxRetryAfterSeconds) {
		return maxRetryAfterSeconds
	}
	return int(n)
}
