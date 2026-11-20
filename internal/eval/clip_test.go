package eval

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestClipNeverSplitsAUTF8Rune 守护失败原因格式化器。clip
// 被用于模型回答、种子文件内容和 exec 输出，它们都
// 被用于模型回答、种子文件内容和 exec 输出，它们都可能包含 CJK 或 emoji。按字节索引切片 Go 字符串会切开
// 多字节 rune，在 TAP 报告中产生无效 UTF-8（报告渲染或 JSON 编码后变成替换字符）。
// 报告渲染或 JSON 编码后变成替换字符）。
func TestClipNeverSplitsAUTF8Rune(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		limit int
		want  string
	}{
		{name: "ascii fits", text: "hello world", limit: 20, want: "hello world"},
		{name: "ascii truncated", text: "hello world", limit: 5, want: "hell…"},
		{name: "empty text", text: "", limit: 5, want: ""},
		{name: "non-positive limit", text: "hello", limit: 0, want: ""},
		{name: "single rune limit", text: "你好", limit: 1, want: "你"},
		{name: "cjk truncated at a rune boundary", text: "你好世界", limit: 3, want: "你好…"},
		{name: "emoji truncated at a rune boundary", text: "🙂🚀done", limit: 2, want: "🙂…"},
		{name: "mixed cjk and ascii", text: "你好abc", limit: 4, want: "你好a…"},
		{name: "whitespace collapsed before clipping", text: "  hello \n  world  ", limit: 9, want: "hello wo…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := clip(tt.text, tt.limit)
			if got != tt.want {
				t.Fatalf("clip(%q, %d) = %q, want %q", tt.text, tt.limit, got, tt.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("clip(%q, %d) = invalid UTF-8 %x", tt.text, tt.limit, got)
			}
		})
	}
}

// TestClipIsValidUTF8ForEveryLimit 是属性扫描：任何限制都不得留下
// TestClipIsValidUTF8ForEveryLimit 是属性扫描：任何限制都不得留下无效字节序列，即使落在 3 或 4 字节 rune 上。
func TestClipIsValidUTF8ForEveryLimit(t *testing.T) {
	samples := []string{
		"你好世界，这是一段中文答案",
		"emoji 🙂🚀🎉 and text",
		"mixed 中文 and ASCII and 🙂",
	}
	for _, text := range samples {
		for limit := 0; limit <= len([]rune(text))+2; limit++ {
			got := clip(text, limit)
			if !utf8.ValidString(got) {
				t.Fatalf("clip(%q, %d) = invalid UTF-8 %q", text, limit, got)
			}
			if len([]rune(got)) > limit && limit > 0 {
				t.Fatalf("clip(%q, %d) = %q has %d runes, want <= %d",
					text, limit, got, len([]rune(got)), limit)
			}
		}
	}
}

// TestEditbenchClipNeverSplitsAUTF8Rune 覆盖 edit 基准自带的格式化器，它与 clip 有相同的字节切片形态。
// 格式化器，它与 clip 有相同的字节切片形态。
func TestEditbenchClipNeverSplitsAUTF8Rune(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{name: "short ascii unchanged", text: "abc", want: "abc"},
		{name: "long cjk clipped to a rune boundary", text: strings.Repeat("你好", 100), want: strings.Repeat("你好", 79) + "你…"},
		{name: "long emoji clipped to a rune boundary", text: strings.Repeat("🙂", 200), want: strings.Repeat("🙂", 159) + "…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := editbenchClip(tt.text)
			if got != tt.want {
				t.Fatalf("editbenchClip(%q) = %q, want %q", tt.text, got, tt.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("editbenchClip(%q) = invalid UTF-8 %x", tt.text, got)
			}
		})
	}
}
