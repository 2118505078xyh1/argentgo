package mcp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	// defaultDiscoveryTimeoutStdio 限制对本地 stdio 服务器的发现，
	// 它们在回答 initialize / tools-list 之前
	// 可能需要时间启动运行时。
	defaultDiscoveryTimeoutStdio = 30 * time.Second
	// defaultDiscoveryTimeoutRemote 限制对 HTTP/SSE 服务器的发现；
	// 在此窗口内无法回答的远程服务器被视为不可用。
	defaultDiscoveryTimeoutRemote = 5 * time.Second
	// minDiscoveryTimeout 和 maxDiscoveryTimeout 限制任何每服务器的
	// 发现超时覆盖，使错误的配置无法禁用或拖慢
	// 发现。
	minDiscoveryTimeout = time.Second
	maxDiscoveryTimeout = 2 * time.Minute
)

// DiscoveryTimeouts 是分级的发现超时策略。零值字段
// 回退到其包级默认值，因此调用方可以
// 只覆盖它们关心的部分。
type DiscoveryTimeouts struct {
	Stdio  time.Duration
	Remote time.Duration
	Min    time.Duration
	Max    time.Duration
}

// DefaultDiscoveryTimeouts 返回发布时策略：stdio 30 秒、远程 5 秒，
// 每服务器覆盖钳制到 [1s, 2m]。
func DefaultDiscoveryTimeouts() DiscoveryTimeouts {
	return DiscoveryTimeouts{
		Stdio:  defaultDiscoveryTimeoutStdio,
		Remote: defaultDiscoveryTimeoutRemote,
		Min:    minDiscoveryTimeout,
		Max:    maxDiscoveryTimeout,
	}
}

// clamp 限制显式的每服务器覆盖。非正的覆盖表示
// "无覆盖"，原样返回，使分级默认值生效。
func (d DiscoveryTimeouts) clamp(override time.Duration) time.Duration {
	if override <= 0 {
		return 0
	}
	min, max := d.Min, d.Max
	if min <= 0 {
		min = minDiscoveryTimeout
	}
	if max <= 0 {
		max = maxDiscoveryTimeout
	}
	if max < min {
		max = min
	}
	if override < min {
		return min
	}
	if override > max {
		return max
	}
	return override
}

// Effective 返回 transport（Spec.transportKind 接受的一个值）的发现超时，
// 给定可选的每服务器覆盖，该覆盖被钳制
// 到配置的界限内。
func (d DiscoveryTimeouts) Effective(transport string, override time.Duration) time.Duration {
	if clamped := d.clamp(override); clamped > 0 {
		return clamped
	}
	stdio, remote := d.Stdio, d.Remote
	if stdio <= 0 {
		stdio = defaultDiscoveryTimeoutStdio
	}
	if remote <= 0 {
		remote = defaultDiscoveryTimeoutRemote
	}
	if strings.EqualFold(strings.TrimSpace(transport), "stdio") {
		return stdio
	}
	return remote
}

// transportKind 模仿 Connect 使用的推断：显式 transport
// 优先，否则有 command 表示 stdio，有 URL 表示远程。
func (s Spec) transportKind() string {
	kind := strings.ToLower(strings.TrimSpace(s.Transport))
	if kind != "" {
		return kind
	}
	if strings.TrimSpace(s.Command) != "" {
		return "stdio"
	}
	return "http"
}

// EffectiveDiscoveryTimeout 返回本服务器的发现超时：
// 设置时取钳制后的每服务器 DiscoveryTimeout 覆盖，否则取
// 按传输类型选择的分级默认值（stdio 30 秒 / 远程 5 秒）。
//
// 它有意与 Spec.Timeout 无关，后者保持其作为普通
// 请求回退界限的原始含义。
func (s Spec) EffectiveDiscoveryTimeout() time.Duration {
	return DefaultDiscoveryTimeouts().Effective(s.transportKind(), s.DiscoveryTimeout)
}

// HTTPStatusError 携带 HTTP 响应状态码，使重试策略
// 无需解析错误字符串即可分类失败。传输在采用重试策略时，应把
// 非 2xx 响应包装为此类型。
type HTTPStatusError struct {
	StatusCode int
	Message    string
}

func (e *HTTPStatusError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Message)
}

// IsRetryable 报告 err 是否是值得退避并重试的短暂失败。
// 只有狭窄的允许列表可重试：
//
//   - 网络重置和超时（ECONNRESET、ETIMEDOUT、net 超时错误）；
//   - HTTP 502、503 和 504。
//
// 其他一切都是永久性的，必须立即失败。特别是 401、
// 403 和 JSON-RPC method-not-found (-32601) 绝不重试。
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	// 永久性的 JSON-RPC 错误胜过链中任何其他信号。
	var rpcErr *rpcErrorBody
	if errors.As(err, &rpcErr) && rpcErr.Code == -32601 {
		return false
	}
	// HTTP 状态：先是有类型错误，然后是当前 HTTP 传输发出的 "HTTP %d"
	// 形式，使策略在传输采用有类型错误之前就可用。
	// adopt the typed error.
	if status := httpStatusFrom(err); status > 0 {
		return status == 502 || status == 503 || status == 504
	}
	// 网络层的短暂状况。
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ETIMEDOUT) ||
		errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// httpStatusFrom 从 err 中提取 HTTP 状态码，来源可以是有类型的
// HTTPStatusError，也可以是 HTTP 传输发出的 "HTTP <code>" 文本。
// 没有状态码时返回 0。
func httpStatusFrom(err error) int {
	var statusErr *HTTPStatusError
	if errors.As(err, &statusErr) {
		return statusErr.StatusCode
	}
	for cur := err; cur != nil; cur = errors.Unwrap(cur) {
		const prefix = "HTTP "
		text := cur.Error()
		if !strings.HasPrefix(text, prefix) {
			continue
		}
		rest := text[len(prefix):]
		if i := strings.IndexAny(rest, " :"); i >= 0 {
			rest = rest[:i]
		}
		if code, convErr := strconv.Atoi(rest); convErr == nil {
			return code
		}
	}
	return 0
}
