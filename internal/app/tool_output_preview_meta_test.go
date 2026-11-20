package app

// 截断元数据纯函数的表驱动测试：截断/未截断、CJK、相等、空值与前缀关系。
// 期望映射由各用例显式给出，避免与实现逻辑互相复述。

import (
	"reflect"
	"strconv"
	"testing"
)

func TestToolOutputPreviewMeta(t *testing.T) {
	// base 构造始终存在的四个计数字段；truncated 由用例显式声明。
	base := func(originalRunes, deliveredRunes, originalBytes, deliveredBytes int, truncated bool) map[string]string {
		return map[string]string{
			"original_runes":  strconv.Itoa(originalRunes),
			"delivered_runes": strconv.Itoa(deliveredRunes),
			"original_bytes":  strconv.Itoa(originalBytes),
			"delivered_bytes": strconv.Itoa(deliveredBytes),
			"truncated":       strconv.FormatBool(truncated),
		}
	}
	// withOmitted 给截断用例补上省略量。
	withOmitted := func(m map[string]string, runes, bytes int) map[string]string {
		m["omitted_runes"] = strconv.Itoa(runes)
		m["omitted_bytes"] = strconv.Itoa(bytes)
		return m
	}

	tests := []struct {
		name      string
		raw       string
		delivered string
		want      map[string]string
	}{
		{
			name:      "truncated_ascii_prefix",
			raw:       "abcdef",
			delivered: "abc",
			want:      withOmitted(base(6, 3, 6, 3, true), 3, 3),
		},
		{
			name:      "not_truncated_different_content",
			raw:       "abcdef",
			delivered: "xyz",
			want:      base(6, 3, 6, 3, false),
		},
		{
			name:      "equal",
			raw:       "hello world",
			delivered: "hello world",
			want:      base(11, 11, 11, 11, false),
		},
		{
			name:      "empty_both",
			raw:       "",
			delivered: "",
			want:      base(0, 0, 0, 0, false),
		},
		{
			name:      "empty_delivered_max_truncation",
			raw:       "abc",
			delivered: "",
			want:      withOmitted(base(3, 0, 3, 0, true), 3, 3),
		},
		{
			name:      "empty_raw_not_truncated",
			raw:       "",
			delivered: "abc",
			want:      base(0, 3, 0, 3, false),
		},
		{
			// 8 符文 / 16 字节（4 个汉字 + 4 个 ASCII），投递 3 符文 / 9 字节。
			name:      "cjk_truncated_rune_safe",
			raw:       "你好世界abcd",
			delivered: "你好世",
			want:      withOmitted(base(8, 3, 16, 9, true), 5, 7),
		},
		{
			name:      "cjk_equal",
			raw:       "中文测试",
			delivered: "中文测试",
			want:      base(4, 4, 12, 12, false),
		},
		{
			// 5 符文 / 11 字节（a🚀b🚀c），投递 3 符文 / 6 字节（a🚀b）。
			name:      "emoji_truncated_rune_safe",
			raw:       "a🚀b🚀c",
			delivered: "a🚀b",
			want:      withOmitted(base(5, 3, 11, 6, true), 2, 5),
		},
		{
			name:      "prefix_delivered_longer",
			raw:       "abc",
			delivered: "abcdef",
			want:      base(3, 6, 3, 6, false),
		},
		{
			name:      "prefix_non_prefix_same_length",
			raw:       "abc",
			delivered: "abd",
			want:      base(3, 3, 3, 3, false),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := toolOutputPreviewMeta(tc.raw, tc.delivered)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("toolOutputPreviewMeta(%q, %q) = %v, want %v", tc.raw, tc.delivered, got, tc.want)
			}
		})
	}
}

// TestToolOutputPreviewMetaDeliveredIsValidUTF8 确保截断判定不会在 CJK 多字节
// 字符中间切分：投递串始终是原始串的符文前缀，因此本身必然是合法 UTF-8。
func TestToolOutputPreviewMetaDeliveredIsValidUTF8(t *testing.T) {
	raw := "你好世界，欢迎使用"
	delivered := "你好世界"
	meta := toolOutputPreviewMeta(raw, delivered)
	if meta["truncated"] != "true" {
		t.Fatalf("truncated = %q, want true", meta["truncated"])
	}
	if meta["delivered_runes"] != "4" {
		t.Fatalf("delivered_runes = %q, want 4", meta["delivered_runes"])
	}
}
