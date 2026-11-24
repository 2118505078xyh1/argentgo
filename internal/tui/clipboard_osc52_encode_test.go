package tui

import (
	"encoding/base64"
	"strings"
	"testing"
)

// TestOSC52Sequence 是跨平台纯函数测试：锁定 OSC 52 写序列的精确形状与
// Unicode（含 CJK/emoji）的 base64 负载，确保终端能还原原文。
func TestOSC52Sequence(t *testing.T) {
	cases := []string{"", "hello", "hello 中文 ✅", "a\x07b", strings.Repeat("x", 1000)}
	for _, text := range cases {
		got := osc52Sequence(text)
		if !strings.HasPrefix(got, "\x1b]52;c;") || !strings.HasSuffix(got, "\x07") {
			t.Fatalf("OSC 52 shape wrong for %q: %q", text, got)
		}
		payload := strings.TrimSuffix(strings.TrimPrefix(got, "\x1b]52;c;"), "\x07")
		decoded, err := base64.StdEncoding.DecodeString(payload)
		if err != nil {
			t.Fatalf("base64 decode %q: %v", text, err)
		}
		if string(decoded) != text {
			t.Fatalf("round trip = %q, want %q", decoded, text)
		}
	}
}
