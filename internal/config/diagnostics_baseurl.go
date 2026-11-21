package config

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// validateBaseURLScheme 校验端点地址：只接受 https，或指向回环地址的
// http（本地测试/自托管）。返回空字符串表示可接受；空值由其他
// 诊断处理，这里不重复报告。
//
// 明文 http 会让 Authorization 头里的凭据以明文经过网络；
// 回环地址是例外，因为流量不会离开本机，本地测试服务器普遍使用 http。
func validateBaseURLScheme(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return fmt.Sprintf("baseURL %q is not a valid URL: %v", value, err)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		return ""
	case "http":
		if isLoopbackHost(parsed.Hostname()) {
			return ""
		}
		return fmt.Sprintf("baseURL %q uses insecure http; only https or a loopback http endpoint is allowed", value)
	default:
		return fmt.Sprintf("baseURL %q must use https, got scheme %q", value, parsed.Scheme)
	}
}

// isLoopbackHost 报告 host 是否指向本机（localhost、IPv4/IPv6 回环地址）。
func isLoopbackHost(host string) bool {
	if strings.EqualFold(strings.TrimSpace(host), "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
