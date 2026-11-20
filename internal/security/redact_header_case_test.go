package security

import (
	"bytes"
	"strings"
	"testing"
)

// TestRedactHeaderNamesAreCaseInsensitive 覆盖 RFC 7230/7235 中不区分大小写的头名
// 和认证方案名。headerPrefixes 过去保存精确大小写的搜索串并搜索原始文本，因此
// `AUTHORIZATION: Basic <b64>` —— 一个完全合法的头 —— 泄露了整个 base64 凭据：
// 裸 base64 token 本身不具有凭据形状，也没有其他规则匹配它。修复方案是把搜索串
// 与 ASCII 小写副本匹配（字节偏移仍然有效），这也让独立的 `BEARER <token>` 变得安全。
func TestRedactHeaderNamesAreCaseInsensitive(t *testing.T) {
	const secret = "dXNlcjpwYXNz"
	cases := []struct {
		name    string
		in      string
		absent  []string
		present []string
	}{
		{
			name:    "uppercase header with OWS",
			in:      "AUTHORIZATION: Basic " + secret,
			absent:  []string{"Basic", secret},
			present: []string{"AUTHORIZATION:", placeholder("auth header")},
		},
		{
			name:    "mixed case header with OWS",
			in:      "AuThOrIzAtIoN: Bearer abc.def.ghi",
			absent:  []string{"Bearer", "abc.def.ghi"},
			present: []string{"AuThOrIzAtIoN:", placeholder("auth header")},
		},
		{
			name:    "uppercase header without OWS",
			in:      "AUTHORIZATION:Basic " + secret,
			absent:  []string{"Basic", secret},
			present: []string{"AUTHORIZATION:", placeholder("auth header")},
		},
		{
			name:    "lowercase header without OWS",
			in:      "authorization:Mac token=abc123",
			absent:  []string{"Mac", "abc123"},
			present: []string{placeholder("auth header")},
		},
		{
			name:    "inside a shell header argument",
			in:      `curl -H 'AUTHORIZATION: Basic ` + secret + `' https://example.com`,
			absent:  []string{"Basic", secret},
			present: []string{"curl -H", "https://example.com", placeholder("auth header")},
		},
		{
			name:    "standalone uppercase bearer scheme",
			in:      "retry with BEARER abc.def.ghi now",
			absent:  []string{"abc.def.ghi"},
			present: []string{"BEARER", placeholder("bearer token")},
		},
		{
			name:    "standalone mixed case bearer scheme",
			in:      "retry with BeArEr abc.def.ghi now",
			absent:  []string{"abc.def.ghi"},
			present: []string{"BeArEr", placeholder("bearer token")},
		},
		{
			name:    "every header on one line is scrubbed",
			in:      "AUTHORIZATION: Basic " + secret + "; Authorization: Bearer abc.def.ghi",
			absent:  []string{secret, "abc.def.ghi"},
			present: []string{placeholder("auth header")},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := Redact(tc.in)
			for _, secret := range tc.absent {
				if strings.Contains(out, secret) {
					t.Errorf("Redact(%q) = %q, leaked %q", tc.in, out, secret)
				}
			}
			for _, marker := range tc.present {
				if !strings.Contains(out, marker) {
					t.Errorf("Redact(%q) = %q, lost %q", tc.in, out, marker)
				}
			}
		})
	}
}

// TestRedactHeaderCaseStructuredPaths 检查不区分大小写的头规则也保护终端行和审计
// JSON 路径，并且 ANSI 颜色序列相对于周围文本保持其确切位置。
func TestRedactHeaderCaseStructuredPaths(t *testing.T) {
	const secret = "dXNlcjpwYXNz"

	line := []byte("\x1b[32mOK\x1b[0m AUTHORIZATION: Basic " + secret + " \x1b[1mend\x1b[0m")
	before := ansiSequences(line)
	out := RedactRow(line)
	if bytes.Contains(out, []byte(secret)) {
		t.Fatalf("credential leaked into row: %q", out)
	}
	if got := ansiSequences(out); !reflectDeepEqualStrings(got, before) {
		t.Fatalf("ANSI sequences changed:\n got %q\nwant %q", got, before)
	}

	jsonIn := []byte(`{"command":"curl -H 'AUTHORIZATION: Basic ` + secret + `' https://x","ok":true}`)
	jsonOut := RedactJSON(jsonIn)
	if strings.Contains(jsonOut, secret) {
		t.Fatalf("credential leaked from JSON: %q", jsonOut)
	}
	if !strings.Contains(jsonOut, `"ok":true`) {
		t.Fatalf("JSON lost non-secret content: %q", jsonOut)
	}
}
