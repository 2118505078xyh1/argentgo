package tools

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestRepoMapCacheKeyGolden 用固定的摘要值锁定版本常量与帧格式：
// 只要版本或拼接方式改变，此值就会变化，从而证明版本参与摘要。
func TestRepoMapCacheKeyGolden(t *testing.T) {
	const want = "5782c6d6cfa8a3f0" // sha256("2:v14:root4:hash8:mentions") 的前 16 位
	if got := repoMapCacheKey("root", "hash", "mentions"); got != want {
		t.Fatalf("repoMapCacheKey = %q, want golden %q", got, want)
	}
	if repoMapCacheVersion != "v1" {
		t.Fatalf("repoMapCacheVersion = %q, want \"v1\"（改动需同步 golden 测试）", repoMapCacheVersion)
	}
}

// TestRepoMapCacheKeyShape 验证键是定长的小写十六进制摘要。
func TestRepoMapCacheKeyShape(t *testing.T) {
	got := repoMapCacheKey("root", "hash", "mentions")
	if len(got) != 16 {
		t.Fatalf("key length = %d, want 16: %q", len(got), got)
	}
	for _, r := range got {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			t.Fatalf("key contains non-lowercase-hex rune %q: %q", r, got)
		}
	}
}

// TestRepoMapCacheKeyDeterministic 验证同一组输入总是得到同一个键。
func TestRepoMapCacheKeyDeterministic(t *testing.T) {
	first := repoMapCacheKey("root", "hash", "mentions")
	for i := 0; i < 100; i++ {
		if got := repoMapCacheKey("root", "hash", "mentions"); got != first {
			t.Fatalf("call %d returned %q, want stable %q", i, got, first)
		}
	}
}

// TestRepoMapCacheKeyEachInputMatters 验证三个输入字段彼此独立：
// 只改动其中一个字段，键就必须改变。
func TestRepoMapCacheKeyEachInputMatters(t *testing.T) {
	base := repoMapCacheKey("root", "hash", "mentions")
	cases := []struct {
		name                      string
		root, filesHash, mentions string
	}{
		{"root", "other-root", "hash", "mentions"},
		{"filesHash", "root", "other-hash", "mentions"},
		{"mentions", "root", "hash", "other-mentions"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := repoMapCacheKey(tc.root, tc.filesHash, tc.mentions); got == base {
				t.Fatalf("changing %s did not change the key: %q", tc.name, got)
			}
		})
	}
}

// TestRepoMapCacheKeyEmptyStable 验证空输入被稳定编码，且与非空输入不同。
func TestRepoMapCacheKeyEmptyStable(t *testing.T) {
	const wantEmpty = "46a1a37d036de634" // sha256("0:v10:0:0:") 的前 16 位
	first := repoMapCacheKey("", "", "")
	if first != wantEmpty {
		t.Fatalf("empty key = %q, want golden %q", first, wantEmpty)
	}
	if second := repoMapCacheKey("", "", ""); second != first {
		t.Fatalf("empty key not stable: %q then %q", first, second)
	}
	// 空值字段与非空字段组合也保持稳定，且彼此可区分。
	cases := []struct {
		root, filesHash, mentions string
	}{
		{"", "hash", "mentions"},
		{"root", "", "mentions"},
		{"root", "hash", ""},
	}
	seen := map[string]bool{first: true, repoMapCacheKey("root", "hash", "mentions"): true}
	for _, tc := range cases {
		a := repoMapCacheKey(tc.root, tc.filesHash, tc.mentions)
		b := repoMapCacheKey(tc.root, tc.filesHash, tc.mentions)
		if a != b {
			t.Fatalf("input (%q,%q,%q) not stable: %q vs %q", tc.root, tc.filesHash, tc.mentions, a, b)
		}
		if seen[a] {
			t.Fatalf("input (%q,%q,%q) collided with another combination: %q", tc.root, tc.filesHash, tc.mentions, a)
		}
		seen[a] = true
	}
}

// TestRepoMapCacheKeyCJK 验证 CJK 多字节字符被当作原始字节安全处理：
// 键为纯 ASCII 十六进制、确定、且不同 CJK 文本得到不同键。
func TestRepoMapCacheKeyCJK(t *testing.T) {
	inputs := []struct {
		root, filesHash, mentions string
	}{
		{"项目根目录", "摘要", "用户关注项"},
		{"项目根目录", "摘要", "另一个关注项"},
		{"函数：处理用户请求", "版本😀", "中文标识符"},
	}
	seen := make(map[string]bool)
	for _, in := range inputs {
		if !utf8.ValidString(in.root) || !utf8.ValidString(in.mentions) {
			t.Fatalf("test input is not valid UTF-8: %+v", in)
		}
		got := repoMapCacheKey(in.root, in.filesHash, in.mentions)
		if len(got) != 16 || strings.Trim(got, "0123456789abcdef") != "" {
			t.Fatalf("CJK input produced non-hex key %q", got)
		}
		if again := repoMapCacheKey(in.root, in.filesHash, in.mentions); again != got {
			t.Fatalf("CJK input not deterministic: %q vs %q", got, again)
		}
		if seen[got] {
			t.Fatalf("distinct CJK inputs collided on key %q", got)
		}
		seen[got] = true
	}
}

// TestRepoMapCacheKeyNoDelimiterCollision 验证帧格式消除了字段边界歧义：
// 把分隔符或 NUL 在相邻字段间挪动不会得到相同的键。
func TestRepoMapCacheKeyNoDelimiterCollision(t *testing.T) {
	cases := []struct {
		name                    string
		aRoot, aHash, aMentions string
		bRoot, bHash, bMentions string
	}{
		{"colon", "a:b", "c", "", "a", "b:c", ""},
		{"nul", "a\x00b", "c", "", "a", "b\x00c", ""},
		{"length-digits", "12:x", "y", "", "1", "2:xy", ""},
		{"cjk", "甲乙", "丙", "", "甲", "乙丙", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := repoMapCacheKey(tc.aRoot, tc.aHash, tc.aMentions)
			b := repoMapCacheKey(tc.bRoot, tc.bHash, tc.bMentions)
			if a == b {
				t.Fatalf("distinct field layouts collided on key %q", a)
			}
		})
	}
}
