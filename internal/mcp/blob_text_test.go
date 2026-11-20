package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestBlobAsTextUTF8(t *testing.T) {
	// 合法 UTF-8 且无 NUL 的字节按原样返回，不做任何改写。
	tests := []struct {
		name string
		data string
		mime string
	}{
		{"ascii", "hello world", "text/plain"},
		{"newlines", "line one\nline two\n", "text/plain; charset=utf-8"},
		{"json", `{"ok":true,"n":42}`, "application/json"},
		{"markdown", "# 标题\n正文", "text/markdown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := blobAsText([]byte(tt.data), tt.mime); got != tt.data {
				t.Fatalf("blobAsText() = %q, want %q", got, tt.data)
			}
		})
	}
}

func TestBlobAsTextCJK(t *testing.T) {
	// CJK 多字节字符是合法 UTF-8，必须原样保留，绝不能被误判为二进制。
	texts := []string{
		"你好，世界",
		"こんにちは世界",
		"안녕하세요",
		"混合 English 与 中文 与 🌏 emoji",
	}
	for _, text := range texts {
		if got := blobAsText([]byte(text), "text/plain"); got != text {
			t.Errorf("blobAsText(%q) = %q, want unchanged", text, got)
		}
	}
}

func TestBlobAsTextBinary(t *testing.T) {
	// 非法 UTF-8 字节序列视为二进制，输出占位符而非原文。
	tests := []struct {
		name string
		data []byte
		mime string
		want string
	}{
		{"invalid utf8", []byte{0xff, 0xfe}, "image/png", "[Binary content (image/png) - 2 bytes]"},
		{"png header", []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, "image/png", "[Binary content (image/png) - 8 bytes]"},
		{"truncated rune", []byte("ok\xe4\xb8"), "text/plain", "[Binary content (text/plain) - 4 bytes]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := blobAsText(tt.data, tt.mime); got != tt.want {
				t.Fatalf("blobAsText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBlobAsTextNUL(t *testing.T) {
	// 含 NUL 的字节即便恰好是合法 UTF-8，也视为二进制。
	data := []byte{'a', 0, 'b'}
	if got, want := blobAsText(data, "text/plain"), "[Binary content (text/plain) - 3 bytes]"; got != want {
		t.Fatalf("blobAsText() = %q, want %q", got, want)
	}
	if got, want := blobAsText([]byte{0}, "application/octet-stream"), "[Binary content (application/octet-stream) - 1 bytes]"; got != want {
		t.Fatalf("single NUL: got %q, want %q", got, want)
	}
}

func TestBlobAsTextMimeSanitization(t *testing.T) {
	// 恶意 mime 中的换行、方括号、控制字符和 CJK 都必须被换成 "_"，
	// 确保占位符的格式不被破坏，也无法向模型上下文注入内容。
	malicious := "image/png\r\n[Injected]\x00evil"
	got := blobAsText([]byte{0xff, 0xfe}, malicious)
	want := "[Binary content (image/png___Injected__evil) - 2 bytes]"
	if got != want {
		t.Fatalf("blobAsText() = %q, want %q", got, want)
	}
	// 占位符内不得残留任何危险字符或非 ASCII 字节。
	inner := strings.TrimSuffix(strings.TrimPrefix(got, "[Binary content ("), ") - 2 bytes]")
	for _, r := range inner {
		if r > 0x7f {
			t.Errorf("sanitized mime contains non-ASCII rune %q", r)
		}
	}
	for _, bad := range []string{"\r", "\n", "\x00", "[", "]"} {
		if strings.Contains(inner, bad) {
			t.Errorf("sanitized mime contains %q: %q", bad, inner)
		}
	}
}

func TestBlobAsTextCJKMime(t *testing.T) {
	// mime 按 RFC 应为 ASCII；含 CJK 的畸形 mime 在占位符中退化为 "_"。
	got := blobAsText([]byte{0xff, 0xfe}, "图像/png")
	want := "[Binary content (__/png) - 2 bytes]"
	if got != want {
		t.Fatalf("blobAsText() = %q, want %q", got, want)
	}
}

func TestBlobAsTextEmpty(t *testing.T) {
	// 空字节串是合法 UTF-8 且无 NUL，按原样返回空文本。
	if got := blobAsText(nil, "text/plain"); got != "" {
		t.Fatalf("nil data: got %q, want empty", got)
	}
	if got := blobAsText([]byte{}, "text/plain"); got != "" {
		t.Fatalf("empty data: got %q, want empty", got)
	}
	// 二进制数据配空 mime 时回退为 "unknown"。
	if got, want := blobAsText([]byte{0xff}, ""), "[Binary content (unknown) - 1 bytes]"; got != want {
		t.Fatalf("empty mime: got %q, want %q", got, want)
	}
	if got, want := blobAsText([]byte{0xff}, "   ; charset=utf-8"), "[Binary content (unknown) - 1 bytes]"; got != want {
		t.Fatalf("parameter-only mime: got %q, want %q", got, want)
	}
}

func TestBinaryContentPlaceholderFormat(t *testing.T) {
	// 占位符必须严格符合 "[Binary content (<mime>) - N bytes]"。
	if got, want := binaryContentPlaceholder("application/pdf", 1234), "[Binary content (application/pdf) - 1234 bytes]"; got != want {
		t.Fatalf("placeholder = %q, want %q", got, want)
	}
	if got, want := binaryContentPlaceholder("image/svg+xml", 0), "[Binary content (image/svg+xml) - 0 bytes]"; got != want {
		t.Fatalf("placeholder = %q, want %q", got, want)
	}
	// 超长 mime 必须被截断到上限内。
	long := strings.Repeat("a", maxBlobMimeDisplayLen+100)
	inner := sanitizeBlobMime(long)
	if len(inner) != maxBlobMimeDisplayLen {
		t.Fatalf("sanitized mime length = %d, want %d", len(inner), maxBlobMimeDisplayLen)
	}
	// 参数应被去掉。
	if got, want := sanitizeBlobMime(" text/plain ; charset=utf-8 "), "text/plain"; got != want {
		t.Fatalf("sanitizeBlobMime() = %q, want %q", got, want)
	}
}

func TestBlobTextEnabled(t *testing.T) {
	// 默认关闭：未设置、空值、假值或无法解析的值都不启用。
	for _, value := range []string{"", "   ", "false", "0", "off", "garbage"} {
		t.Setenv(blobTextEnv, value)
		if blobTextEnabled() {
			t.Errorf("blobTextEnabled(%q) = true, want false", value)
		}
	}
	// 真值启用。
	for _, value := range []string{"1", "true", "TRUE", "t", "True"} {
		t.Setenv(blobTextEnv, value)
		if !blobTextEnabled() {
			t.Errorf("blobTextEnabled(%q) = false, want true", value)
		}
	}
}

func TestBlobTextFromResource(t *testing.T) {
	// 文本型 blob 当文本用。
	textRes := ResourceContent{MIMEType: "text/plain", Blob: base64.StdEncoding.EncodeToString([]byte("plain text"))}
	if got, want := blobTextFromResource(textRes), "plain text"; got != want {
		t.Fatalf("text blob: got %q, want %q", got, want)
	}

	// 二进制 blob 输出占位符。
	binRes := ResourceContent{MIMEType: "image/png", Blob: base64.StdEncoding.EncodeToString([]byte{0xff, 0xfe})}
	if got, want := blobTextFromResource(binRes), "[Binary content (image/png) - 2 bytes]"; got != want {
		t.Fatalf("binary blob: got %q, want %q", got, want)
	}

	// 非法 base64 降级为占位符，不返回错误也不 panic。
	badRes := ResourceContent{MIMEType: "image/png", Blob: "!!!not base64!!!"}
	if got, want := blobTextFromResource(badRes), "[Binary content (image/png) - 0 bytes]"; got != want {
		t.Fatalf("invalid base64: got %q, want %q", got, want)
	}

	// 空 blob 同样降级。
	if got, want := blobTextFromResource(ResourceContent{MIMEType: "text/plain"}), "[Binary content (text/plain) - 0 bytes]"; got != want {
		t.Fatalf("empty blob: got %q, want %q", got, want)
	}
}

func TestCallToolBlobTextOptIn(t *testing.T) {
	// 选择加入时，文本型 blob 当文本用，二进制 blob 输出占位符。
	t.Setenv(blobTextEnv, "1")
	t.Setenv(blobArtifactDirEnv, "")

	textBlob := base64.StdEncoding.EncodeToString([]byte("from blob"))
	binBlob := base64.StdEncoding.EncodeToString([]byte{0xff, 0xfe})
	response := map[string]any{"content": []any{
		map[string]any{"type": "text", "text": "prefix"},
		map[string]any{"type": "resource", "resource": map[string]any{
			"mimeType": "text/plain", "blob": textBlob,
		}},
		map[string]any{"type": "resource", "resource": map[string]any{
			"mimeType": "image/png", "blob": binBlob,
		}},
	}}
	ft := &fakeTransport{handlers: map[string]func(json.RawMessage) (any, error){
		"tools/call": func(json.RawMessage) (any, error) { return response, nil },
	}}
	c := newFakeClient("fake", ft, time.Second)
	got, err := c.CallTool(context.Background(), "render", nil)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	want := "prefix\nfrom blob\n[Binary content (image/png) - 2 bytes]"
	if got != want {
		t.Fatalf("CallTool = %q, want %q", got, want)
	}
}
