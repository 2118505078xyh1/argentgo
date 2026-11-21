package llm

import (
	"regexp"
	"testing"
)

// TestErrorFamilyIDCoversEveryFamily 覆盖每个 Family 常量，确认映射为
// 稳定的小写标识；这是遥测聚合维度取值集合封闭的核心保证。
func TestErrorFamilyIDCoversEveryFamily(t *testing.T) {
	cases := []struct {
		name   string
		family string
		want   string
	}{
		{"not configured", FamilyNotConfigured, "not_configured"},
		{"auth", FamilyAuth, "auth"},
		{"quota exhausted", FamilyQuotaExhausted, "quota_exhausted"},
		{"rate limit", FamilyRateLimit, "rate_limit"},
		{"content filter", FamilyContentFilter, "content_filter"},
		{"context overflow", FamilyContextOverflow, "context_overflow"},
		{"model not found", FamilyModelNotFound, "model_not_found"},
		{"network", FamilyNetwork, "network"},
		{"server", FamilyServer, "server"},
		{"unknown", FamilyUnknown, "unknown"},
	}

	seen := map[string]string{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := errorFamilyID(tc.family)
			if got != tc.want {
				t.Errorf("errorFamilyID(%q) = %q, want %q", tc.family, got, tc.want)
			}
			if other, dup := seen[got]; dup {
				t.Errorf("标识 %q 被 %q 与 %q 共用，聚合维度应可区分各 family", got, other, tc.family)
			}
			seen[got] = tc.family
		})
	}
}

// TestErrorFamilyIDUnknownFoldsToUnknown 未知取值（含 CJK、大小写变体、
// 空串和未来可能的家族名）一律折叠为 "unknown"，输出不含任何 CJK 字符。
func TestErrorFamilyIDUnknownFoldsToUnknown(t *testing.T) {
	cases := []struct {
		name   string
		family string
	}{
		{"empty", ""},
		{"foreign CJK", "认证失败"},
		{"mixed CJK", "网络错误 auth"},
		{"uppercase", "AUTH"},
		{"leading space", " auth"},
		{"future family", "new_fancy_family"},
		{"message text", "provider returned 401 Unauthorized"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := errorFamilyID(tc.family); got != "unknown" {
				t.Errorf("errorFamilyID(%q) = %q, want %q", tc.family, got, "unknown")
			}
		})
	}
}

// TestErrorFamilyIDStableAndAscii 校验输出取值集合稳定：可重复、小写 ASCII、
// 且符合遥测标签白名单，因此可直接用于聚合而不需要二次清洗。
func TestErrorFamilyIDStableAndAscii(t *testing.T) {
	safe := regexp.MustCompile(`^[a-z][a-z_]*$`)
	families := []string{
		FamilyNotConfigured, FamilyAuth, FamilyQuotaExhausted, FamilyRateLimit,
		FamilyContentFilter, FamilyContextOverflow, FamilyModelNotFound,
		FamilyNetwork, FamilyServer, FamilyUnknown, "unknown-family", "未知",
	}

	for _, family := range families {
		first := errorFamilyID(family)
		if !safe.MatchString(first) {
			t.Errorf("errorFamilyID(%q) = %q, 不是安全的小写 ASCII 标识", family, first)
		}
		for i := 0; i < 5; i++ {
			if got := errorFamilyID(family); got != first {
				t.Fatalf("errorFamilyID(%q) 第 %d 次调用 = %q, want 稳定的 %q", family, i, got, first)
			}
		}
	}
}
