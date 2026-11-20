// Package auth 存储 provider 凭据并运行 OAuth 2.0 登录，不依赖第三方 SDK。
// 不依赖第三方 SDK。
//
// 令牌存放在某目录下的单个 JSON 文件中（模式 0600，原子
// 写入）。支持两种登录流程：浏览器授权码
// 令牌存放在某目录下的单个 JSON 文件中（模式 0600，原子写入）。支持两种登录流程：带 PKCE 的浏览器授权码流程（RFC 7636）和设备码流程（RFC 8628）。provider 预设提供端点；包本身只了解标准。
// provider 预设提供端点；包本身只了解标准。
package auth

import (
	"time"
)

// 令牌种类。
const (
	// KindOAuth 标记从 OAuth 流程获得的令牌。
	KindOAuth = "oauth"
	// KindAPIKey 标记手动粘贴的 API key。
	KindAPIKey = "api_key"
)

// expirySafety 在 provider 自己的截止时间之前稍早刷新令牌，
// expirySafety 在 provider 自己的截止时间之前稍早刷新令牌，镜像 Rust 实现的 60 秒余量。
const expirySafety = 60 * time.Second

// Token 是一条存储的凭据。
type Token struct {
	Provider string `json:"provider"`
	// Kind 是 KindOAuth 或 KindAPIKey。
	Kind string `json:"kind"`
	// Access 是要发给 provider 的秘密：OAuth 访问令牌或 API key。
	Access string `json:"access,omitempty"`
	// Refresh 是 OAuth 刷新令牌，API key 时为空。
	Refresh string `json:"refresh,omitempty"`
	// ExpiresAt 是访问令牌截止时间；零值表示"无过期时间"。
	ExpiresAt time.Time `json:"-"`
	// Account 可选地记录令牌属于哪个账号。
	Account string `json:"account,omitempty"`
}

// Bearer 返回要发给 provider 的秘密。
func (t Token) Bearer() string {
	if t.Access != "" {
		return t.Access
	}
	return t.Refresh
}

// Status 描述存储令牌此刻的可用程度。
type Status string

// Store.Status 报告的 Status 值。
const (
	StatusValid    Status = "valid"
	StatusExpiring Status = "expiring"
	StatusExpired  Status = "expired"
	StatusMissing  Status = "missing"
)

// String 实现 fmt.Stringer。
func (s Status) String() string { return string(s) }

// expirySkew 是仍有效的令牌被报告为即将过期的窗口，使调用方能提前刷新。
// 即将过期，使调用方能提前刷新。
const expirySkew = 5 * time.Minute
