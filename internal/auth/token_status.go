package auth

import (
	"net/http"
	"strings"
)

// TokenState 描述令牌截止时间的可用程度，与任何
// TokenState 描述令牌截止时间的可用程度，与任何 store 或时钟无关。它是 Status 的纯对应物：TokenStatus 处理
// 原始 Unix 秒，使调用方无需加载凭据文件即可分类，并可自行选择刷新余量。
// 并可自行选择刷新余量。
type TokenState string

// TokenStatus 报告的 TokenState 值。
const (
	TokenValid    TokenState = "valid"
	TokenExpiring TokenState = "expiring"
	TokenExpired  TokenState = "expired"
	// TokenUnknown 表示不知道截止时间（磁盘上零哨兵）。
	TokenUnknown TokenState = "unknown"
)

// String 实现 fmt.Stringer。
func (s TokenState) String() string { return string(s) }

// TokenStatus 分类访问令牌截止时间。expiresAt 和 now 是 Unix 秒；refreshMargin 是在过期前多少秒把仍有效的令牌视为足够接近从而值得主动刷新。
// 秒；refreshMargin 是在过期前多少秒使仍有效令牌
// 被认为足够接近从而值得主动刷新。
//
// 非正的 expiresAt 是"无过期时间"哨兵并产生 TokenUnknown。负的 refreshMargin 被视为零（仅在过期后刷新）。比较是溢出安全的：远超 int64 减法
// 负的 refreshMargin 被视为零（仅在过期后刷新）。
// 比较是溢出安全的：远超 int64 减法的
// 范围的截止时间被报告为 TokenValid，而非回绕到过去。
func TokenStatus(expiresAt, now, refreshMargin int64) TokenState {
	if expiresAt <= 0 {
		return TokenUnknown
	}
	if expiresAt <= now {
		return TokenExpired
	}
	// expiresAt > here，因此非正的差值意味着 expiresAt-now 回绕了：真实截止时间实际上无限且仍然健康。
	// 回绕：真实截止时间实际上无限且仍然健康。
	left := expiresAt - now
	if left <= 0 {
		return TokenValid
	}
	if refreshMargin < 0 {
		refreshMargin = 0
	}
	if left <= refreshMargin {
		return TokenExpiring
	}
	return TokenValid
}

// tokenRefreshMarkers 是使刷新值得进行的
// 令牌失效短语。刻意比通用认证错误分类更窄
// tokenRefreshMarkers 是使刷新值得进行的令牌失效短语。刻意比 internal/llm 中的通用认证错误分类更窄：403 或无效 API key 无法通过刷新修复。
// 匹配不区分大小写；CJK 标记不受小写化影响。
var tokenRefreshMarkers = []string{
	"invalid_token", "invalid token", "invalid-token",
	"expired_token", "expired token", "expired-token",
	"token_expired", "token expired", "token has expired",
	"invalid access token", "access token expired",
	"令牌已过期", "令牌过期", "token已过期", "token 已过期",
	"无效令牌", "无效的令牌", "令牌无效",
}

// ShouldRefreshOnError 报告 provider 失败是否是值得刷新的令牌过期信号。只有显式 401 或无效/过期令牌消息
// 值得刷新的信号。只有显式 401 或无效/过期令牌消息
// 才算；其他任何状态或消息都返回 false，使临时或权限错误永不触发不必要的刷新。该决定是两个参数的纯
// 才算；其他任何状态或消息返回 false，使临时或权限错误永不触发不必要刷新。该决定是
// 函数且对调用方可见（透明）。
func ShouldRefreshOnError(statusCode int, errBody string) bool {
	if statusCode == http.StatusUnauthorized {
		return true
	}
	lower := strings.ToLower(errBody)
	for _, marker := range tokenRefreshMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
