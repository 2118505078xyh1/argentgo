package llm

import (
	"net/http"
	"strings"
)

// Auth 是一个已解析的认证头。零值 Auth 表示"无认证"：它
// 不写入任何头部，这正是 apiKey:false / NoAuth 的表示方式。
type Auth struct {
	Header string
	Value  Secret
}

// NoAuth 返回提供方不需要凭据时使用的哨兵值。
func NoAuth() Auth {
	return Auth{}
}

// OrElse 在接收者带有凭据时返回它，否则返回
// fallback。链式调用它可模拟有序的环境变量来源列表。
func (a Auth) OrElse(fallback Auth) Auth {
	if a.Value.Value() != "" {
		return a
	}
	return fallback
}

// Apply 写入已解析的头部。NoAuth 有意不写入任何东西。
func (a Auth) Apply(headers http.Header) {
	if headers == nil || a.Header == "" || a.Value.Value() == "" {
		return
	}
	headers.Set(a.Header, a.Value.Value())
}

// ResolveCredential 返回 names 中第一个非空值。空
// 名称会被跳过；仅含空白的值视为缺失。
func ResolveCredential(lookup func(string) string, names []string) string {
	for _, name := range names {
		if strings.TrimSpace(name) == "" || lookup == nil {
			continue
		}
		if value := strings.TrimSpace(lookup(name)); value != "" {
			return value
		}
	}
	return ""
}

// ResolveAuth 根据 names 中第一个非空凭据构建认证头。
// 设置 scheme 时会加在前面（Authorization 用 "Bearer"）；空
// scheme 则发送原始值（x-api-key）。没有 names 或没有值都产生 NoAuth。
func ResolveAuth(lookup func(string) string, names []string, header, scheme string) Auth {
	credential := ResolveCredential(lookup, names)
	if credential == "" {
		return NoAuth()
	}
	if scheme != "" {
		credential = scheme + " " + credential
	}
	return Auth{Header: header, Value: NewSecret(credential)}
}
