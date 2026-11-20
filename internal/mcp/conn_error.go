package mcp

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"syscall"
)

// ErrAuthentication 标记认证或授权失败（凭据错误、密钥过期、权限不足）。
// 这类错误重试同一个连接永远不会成功，调用方必须先更新凭据或配置。
// 传输层应使用 %w 包裹它，使 isRetryableConnectionError 能沿着错误链
// 稳定识别，而不依赖任何语言的错误文本。
var ErrAuthentication = errors.New("mcp: authentication failed")

// ErrConfiguration 标记连接配置错误（URL 无效、传输不被支持、服务器被禁用等）。
// 重试同一份错误配置不会自行恢复，必须先修改配置。
var ErrConfiguration = errors.New("mcp: invalid configuration")

// retryableNetworkErrs 是短暂连接故障的哨兵集合：这些状况会随时间推移
// 或服务器重启而消失，适合退避后重连。
//
// 这里只放入"连接层"的瞬时故障；不放入 os.ErrNotExist、os.ErrPermission
// 等本地永久错误，也不放入 context.Canceled（那是调用方主动放弃）。
var retryableNetworkErrs = []error{
	syscall.ECONNRESET,   // 对端重置连接
	syscall.ECONNABORTED, // 连接被中止
	syscall.ECONNREFUSED, // 服务器尚未开始监听（冷启动时常见）
	syscall.ETIMEDOUT,    // 连接或读写超时
	syscall.EHOSTUNREACH, // 主机不可达
	syscall.EHOSTDOWN,    // 主机关闭
	syscall.ENETUNREACH,  // 网络不可达
	syscall.ENETRESET,    // 网络层重置
	syscall.ENETDOWN,     // 网络已断开
	syscall.EPIPE,        // 向已关闭的连接写入
	io.EOF,               // 对端关闭连接（stdio 服务器退出）
	io.ErrUnexpectedEOF,  // 对端在读取中途关闭
	context.DeadlineExceeded,
	os.ErrDeadlineExceeded,
}

// isRetryableConnectionError 报告 err 是否是一次值得退避重连的暂时性
// 连接故障。
//
// 分类只依据错误链上的类型与哨兵（errors.Is / errors.As），从不读取
// 错误文本。因此结果与措辞无关：无论底层用 CJK 还是英文描述、
// 无论大小写，也无论错误被 %w 包裹多少层，同一条错误链始终得到
// 同一个答案——这正是"稳定、CJK 无关"的核心。
//
// 判定顺序是"永久优先"：认证与配置类错误即使被超时信息包裹，
// 也仍判为不可重试，避免对注定失败的凭据反复重连。
func isRetryableConnectionError(err error) bool {
	if err == nil {
		return false
	}

	// 永久错误优先，压过错误链中可能出现的任何瞬时信号。
	if isPermanentConnectionError(err) {
		return false
	}

	// 瞬时故障的哨兵匹配，覆盖被 %w 多层包裹的情形。
	for _, sentinel := range retryableNetworkErrs {
		if errors.Is(err, sentinel) {
			return true
		}
	}

	// 网关类 HTTP 状态码是短暂的上游故障，值得退避重连。
	// 这里只看有类型错误，不解析任何错误文本。
	var statusErr *HTTPStatusError
	if errors.As(err, &statusErr) &&
		(statusErr.StatusCode == 502 || statusErr.StatusCode == 503 || statusErr.StatusCode == 504) {
		return true
	}

	// net.Error 的超时分支：覆盖 url.Error、net.OpError 等标准库
	// 传输错误，它们通过 Timeout() 表达瞬时性。只认 Timeout()
	// （Temporary() 已废弃），与 IsRetryable 的口径保持一致。
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// isPermanentConnectionError 汇总"重连也不会成功"的错误类别：
// 认证失败、配置错误、调用方取消以及已知的协议级永久失败。
func isPermanentConnectionError(err error) bool {
	// 认证与配置哨兵，以及标准的权限不足错误。
	if errors.Is(err, ErrAuthentication) || errors.Is(err, ErrConfiguration) ||
		errors.Is(err, os.ErrPermission) {
		return true
	}

	// 本包既有的本地策略哨兵都属于配置层拒绝，重连毫无意义。
	if errors.Is(err, ErrUnsupportedTransport) || errors.Is(err, ErrDisabled) ||
		errors.Is(err, ErrReadOnly) || errors.Is(err, ErrToolNotAllowed) {
		return true
	}

	// HTTP 认证状态码走有类型错误，而非解析错误文本。
	var statusErr *HTTPStatusError
	if errors.As(err, &statusErr) &&
		(statusErr.StatusCode == 401 || statusErr.StatusCode == 403) {
		return true
	}

	// JSON-RPC 方法不存在是协议级永久失败。
	var rpcErr *rpcErrorBody
	if errors.As(err, &rpcErr) && rpcErr.Code == -32601 {
		return true
	}

	// 调用方主动取消不应触发退避重连。
	return errors.Is(err, context.Canceled)
}
