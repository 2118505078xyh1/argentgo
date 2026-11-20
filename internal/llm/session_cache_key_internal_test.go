package llm

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestSessionCacheKeyThreeStates 覆盖三态策略：覆盖值优先、会话派生、
// 两者都为空时返回空串（供线协议通过 omitempty 省略 prompt_cache_key）。
func TestSessionCacheKeyThreeStates(t *testing.T) {
	cases := []struct {
		name      string
		sessionID string
		override  string
		want      string
	}{
		{"both empty", "", "", ""},
		{"session only", "sess-123", "", "session:sess-123"},
		{"override only", "", "override-key", "override-key"},
		{"override wins over session", "sess-123", "override-key", "override-key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sessionCacheKey(tc.sessionID, tc.override); got != tc.want {
				t.Fatalf("sessionCacheKey(%q, %q) = %q, want %q", tc.sessionID, tc.override, got, tc.want)
			}
		})
	}
}

// TestSessionCacheKeyTrimsWhitespace 验证首尾空白被去除，且纯空白的
// 覆盖值视为未设置，从而回退到会话派生分支。
func TestSessionCacheKeyTrimsWhitespace(t *testing.T) {
	if got := sessionCacheKey("sess-123", "   "); got != "session:sess-123" {
		t.Fatalf("whitespace override = %q, want session-derived key", got)
	}
	if got := sessionCacheKey("  sess-123  ", ""); got != "session:sess-123" {
		t.Fatalf("whitespace session = %q, want trimmed session-derived key", got)
	}
	if got := sessionCacheKey("sess-123", "  key  "); got != "key" {
		t.Fatalf("whitespace override = %q, want trimmed override", got)
	}
}

// TestSessionCacheKeyCJK 验证 CJK 字符在拼接与截断中都不会被破坏。
func TestSessionCacheKeyCJK(t *testing.T) {
	if got := sessionCacheKey("会话-42", ""); got != "session:会话-42" {
		t.Fatalf("CJK session = %q, want %q", got, "session:会话-42")
	}
	if got := sessionCacheKey("sess", "覆盖键"); got != "覆盖键" {
		t.Fatalf("CJK override = %q, want %q", got, "覆盖键")
	}

	// 截断发生在 rune 边界，不会把某个多字节字符切成一半。
	long := strings.Repeat("会", sessionCacheKeyMaxLen+10)
	got := sessionCacheKey("", long)
	if !utf8.ValidString(got) {
		t.Fatalf("truncated key is not valid UTF-8: %q", got)
	}
	if n := utf8.RuneCountInString(got); n != sessionCacheKeyMaxLen {
		t.Fatalf("truncated CJK key has %d runes, want %d", n, sessionCacheKeyMaxLen)
	}
}

// TestSessionCacheKeyLengthBound 验证两个分支的输出都有长度上限，
// 恰好等于上限的输入不被截断。
func TestSessionCacheKeyLengthBound(t *testing.T) {
	ascii := strings.Repeat("a", sessionCacheKeyMaxLen+50)
	if got := sessionCacheKey("", ascii); got != ascii[:sessionCacheKeyMaxLen] {
		t.Fatalf("long override was not truncated at rune %d", sessionCacheKeyMaxLen)
	}
	// 会话分支同样有界："session:" 前缀占 8 个 rune。
	longID := strings.Repeat("s", sessionCacheKeyMaxLen+50)
	if got := sessionCacheKey(longID, ""); utf8.RuneCountInString(got) != sessionCacheKeyMaxLen {
		t.Fatalf("long session key has %d runes, want %d", utf8.RuneCountInString(got), sessionCacheKeyMaxLen)
	}

	exact := strings.Repeat("b", sessionCacheKeyMaxLen)
	if got := sessionCacheKey("", exact); got != exact {
		t.Fatal("input at the length limit must not be truncated")
	}
}

// TestSessionCacheKeyDeterministic 验证相同输入总是得到相同输出。
func TestSessionCacheKeyDeterministic(t *testing.T) {
	want := sessionCacheKey("sess-123", "")
	for i := 0; i < 100; i++ {
		if got := sessionCacheKey("sess-123", ""); got != want {
			t.Fatalf("call %d = %q, want %q", i, got, want)
		}
	}
}
