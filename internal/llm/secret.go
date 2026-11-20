package llm

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

// Redacted 是在任何会显示凭据之处使用的占位符。
const Redacted = "[REDACTED]"

var redactedHeaderNames = map[string]bool{
	"authorization":        true,
	"cookie":               true,
	"proxy-authorization":  true,
	"set-cookie":           true,
	"x-api-key":            true,
	"x-amz-security-token": true,
	"x-goog-api-key":       true,
}

var defaultRedactedJSONFields = []string{
	"access_token",
	"api_key",
	"apikey",
	"client_secret",
	"password",
	"refresh_token",
	"secret",
	"token",
}

// Secret 是一种原始值只能显式访问的凭据。
//
// 它的未导出字段和格式化方法使该值不会出现在 fmt
// 输出、JSON 和基于探测的转储中。把它发送到上游的唯一方式是
// Value()，它只应在协议边界调用，不应在其他地方调用。
type Secret struct {
	value string
}

// NewSecret 包装凭据，使意外的日志记录无法泄露它。
func NewSecret(value string) Secret {
	return Secret{value: value}
}

// Value 返回原始凭据。只在写认证头时使用它。
func (s Secret) Value() string {
	return s.value
}

// String 让 fmt.Sprint、%v 和 %+v 打印占位符而不是密钥。
func (s Secret) String() string {
	return Redacted
}

// GoString 让 %#v 也打印占位符。
func (s Secret) GoString() string {
	return Redacted
}

// MarshalJSON 使原始值不会出现在编码后的请求日志和快照中。
func (s Secret) MarshalJSON() ([]byte, error) {
	return json.Marshal(Redacted)
}

// RedactHeaders 返回头部的一份副本，其中含凭据的头部值
// 已被替换。输入不会被修改，因此原始请求仍可
// 带着真实凭据发送。
func RedactHeaders(headers http.Header) http.Header {
	if headers == nil {
		return nil
	}
	out := make(http.Header, len(headers))
	for name, values := range headers {
		canonical := http.CanonicalHeaderKey(name)
		if redactedHeaderNames[strings.ToLower(canonical)] {
			replaced := make([]string, len(values))
			for i := range values {
				replaced[i] = Redacted
			}
			out[canonical] = replaced
			continue
		}
		out[canonical] = append([]string(nil), values...)
	}
	return out
}

// RedactURL 返回 u 的一份副本，移除其 query 和 userinfo。Host 和
// path 保留，因为它们对日志有用且很少携带秘密。
func RedactURL(u *url.URL) *url.URL {
	if u == nil {
		return nil
	}
	out := *u
	out.User = nil
	out.RawQuery = ""
	out.ForceQuery = false
	return &out
}

// RedactJSONFields 返回 value 的一份副本，其中凭据形态的字段
// （包括嵌套在 map 和 slice 中的字段）已被替换。names 扩展
// 默认字段列表；匹配忽略大小写和非字母数字字符。
func RedactJSONFields(value map[string]any, names []string) map[string]any {
	fields := make(map[string]bool, len(defaultRedactedJSONFields)+len(names))
	for _, name := range defaultRedactedJSONFields {
		fields[normalizeFieldName(name)] = true
	}
	for _, name := range names {
		fields[normalizeFieldName(name)] = true
	}
	return redactJSONObject(value, fields)
}

func normalizeFieldName(name string) string {
	var out strings.Builder
	for _, r := range strings.ToLower(name) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			out.WriteRune(r)
		}
	}
	return out.String()
}

func redactJSONObject(value map[string]any, fields map[string]bool) map[string]any {
	if value == nil {
		return nil
	}
	out := make(map[string]any, len(value))
	for name, child := range value {
		if fields[normalizeFieldName(name)] {
			out[name] = Redacted
			continue
		}
		out[name] = redactJSONValue(child, fields)
	}
	return out
}

func redactJSONValue(value any, fields map[string]bool) any {
	switch typed := value.(type) {
	case map[string]any:
		return redactJSONObject(typed, fields)
	case []any:
		out := make([]any, len(typed))
		for i, child := range typed {
			out[i] = redactJSONValue(child, fields)
		}
		return out
	default:
		return value
	}
}
