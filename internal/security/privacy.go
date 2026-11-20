package security

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// PrivacyMode 是由 --privacy-mode 选择的网络姿态。它与 SandboxMode 相互独立：
// 沙箱模式管理文件工具，隐私模式管理远程端点和网络工具。
type PrivacyMode string

const (
	// PrivacyLocalOnly 默认阻止远程端点和 web 工具。对于敏感工作，这是一种确定性、
	// 可审计的姿态。
	PrivacyLocalOnly PrivacyMode = "local-only"
	// PrivacyAny 保留历史行为：所有端点都可访问。
	PrivacyAny PrivacyMode = "any"
)

// maxPrivacyURLBytes 限制隐私检查解析的内容。值来自分层配置文件，因此无界的
// 字符串绝不能到达 net/url。
const maxPrivacyURLBytes = 4096

var (
	ErrPrivacyEmptyURL     = errors.New("security: privacy: empty URL")
	ErrPrivacyURLTooLong   = errors.New("security: privacy: URL too long")
	ErrPrivacyMalformedURL = errors.New("security: privacy: malformed URL")
	ErrPrivacyScheme       = errors.New("security: privacy: scheme is not http or https")
	ErrPrivacyNoHost       = errors.New("security: privacy: URL has no host")
)

// 决定原因是稳定的常量：其中不掺入任何动态输入，因此审计日志保持确定性且
// 便于 diff。
const (
	reasonPrivacyAny           = "privacy: mode=any: remote endpoints allowed"
	reasonPrivacyLoopback      = "privacy: mode=local-only: loopback endpoint allowed"
	reasonPrivacyOverride      = "privacy: mode=local-only: remote endpoint allowed by explicit override"
	reasonPrivacyRemoteBlocked = "privacy: mode=local-only: remote endpoint blocked"
	reasonPrivacyInvalidURL    = "privacy: mode=local-only: endpoint denied (invalid URL)"
	reasonPrivacyUnknownMode   = "privacy: unknown mode: endpoint denied"
)

// IsLoopbackURL 报告 rawURL 是否是主机解析为本机的 http(s) URL：localhost、
// 任何 127.0.0.0/8 地址或 ::1。有效但远程的 URL 返回 (false, nil)；无法解析为
// http(s) URL 的一切内容返回 (false, error)，以便调用方失败即拒绝。
func IsLoopbackURL(rawURL string) (bool, error) {
	if len(rawURL) > maxPrivacyURLBytes {
		return false, fmt.Errorf("%w: %d bytes", ErrPrivacyURLTooLong, len(rawURL))
	}
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return false, ErrPrivacyEmptyURL
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrPrivacyMalformedURL, err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return false, fmt.Errorf("%w: %q", ErrPrivacyScheme, parsed.Scheme)
	}
	host := parsed.Hostname()
	if host == "" {
		return false, ErrPrivacyNoHost
	}
	if strings.EqualFold(host, "localhost") {
		return true, nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false, nil
	}
	return ip.IsLoopback(), nil
}

// AllowRemote 对一个端点应用隐私模式。"any" 模式允许一切；"local-only" 允许
// 回环端点或显式覆盖。未知模式会被拒绝，这样拼写错误不会悄悄放宽姿态。
// 返回的原因是调用方应放入审计条目的稳定字符串。
func AllowRemote(mode PrivacyMode, rawURL string, allowRemoteOverride bool) (bool, string) {
	switch mode {
	case PrivacyAny:
		return true, reasonPrivacyAny
	case PrivacyLocalOnly:
		// 在下面处理。
	default:
		return false, reasonPrivacyUnknownMode
	}
	if allowRemoteOverride {
		return true, reasonPrivacyOverride
	}
	loopback, err := IsLoopbackURL(rawURL)
	if err != nil {
		return false, reasonPrivacyInvalidURL
	}
	if loopback {
		return true, reasonPrivacyLoopback
	}
	return false, reasonPrivacyRemoteBlocked
}

// ShouldBlockWebTool 报告 web_search/web_fetch 的姿态默认值。显式的 allow-remote
// 覆盖是调用方另行处理的事情：该辅助函数只报告模式本身的要求，把决策集中在一处。
func ShouldBlockWebTool(mode PrivacyMode) bool {
	return mode == PrivacyLocalOnly
}

// PrivacyAuditReason 返回用于审计条目的稳定 "privacy_mode" 事件原因。空模式会被
// 记录为默认的 "any" 姿态，使事件始终自描述。changed 标记从默认姿态发生的转变。
func PrivacyAuditReason(mode PrivacyMode, changed bool) string {
	value := string(mode)
	if value == "" {
		value = string(PrivacyAny)
	}
	return fmt.Sprintf("privacy_mode=%s changed=%t", value, changed)
}
