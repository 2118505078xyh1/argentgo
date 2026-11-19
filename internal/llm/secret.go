package llm

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

// Redacted is the placeholder used wherever a credential would be shown.
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

// Secret is a credential whose raw value can only be reached explicitly.
//
// Its unexported field and formatting methods keep the value out of fmt
// output, JSON and probe-based dumps. The only way to send it upstream is
// Value(), which should be called at the wire boundary and nowhere else.
type Secret struct {
	value string
}

// NewSecret wraps a credential so accidental logging cannot reveal it.
func NewSecret(value string) Secret {
	return Secret{value: value}
}

// Value returns the raw credential. Use it only when writing the auth header.
func (s Secret) Value() string {
	return s.value
}

// String makes fmt.Sprint, %v and %+v print a placeholder instead of the key.
func (s Secret) String() string {
	return Redacted
}

// GoString makes %#v print a placeholder as well.
func (s Secret) GoString() string {
	return Redacted
}

// MarshalJSON keeps the raw value out of encoded request logs and snapshots.
func (s Secret) MarshalJSON() ([]byte, error) {
	return json.Marshal(Redacted)
}

// RedactHeaders returns a copy of headers with credential-bearing header
// values replaced. The input is not modified, so the original request can
// still be sent with its real credentials.
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

// RedactURL returns a copy of u with the query and userinfo removed. Host and
// path are kept because they are useful in logs and rarely carry secrets.
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

// RedactJSONFields returns a copy of value with credential-shaped fields
// replaced, including fields nested in maps and slices. names extends the
// default field list; matching ignores case and non-alphanumeric characters.
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
