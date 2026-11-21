package config

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestDeprecatedHintKnownKeys 覆盖所有已知废弃键：每条建议都必须是
// 可操作的中文提示，标明“已废弃”，且为完整 UTF-8、长度有界。
func TestDeprecatedHintKnownKeys(t *testing.T) {
	cases := []struct {
		key     string
		wantAny []string // 建议中至少出现其中一个词
	}{
		{key: "logLevel", wantAny: []string{"删除"}},
		{key: "subagent_depth", wantAny: []string{"删除", "MaxDepth"}},
		{key: "layout", wantAny: []string{"删除"}},
		{key: "server", wantAny: []string{"删除", "--serve", "--rpc"}},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			hint := deprecatedHint(tc.key)
			if hint == "" {
				t.Fatalf("deprecatedHint(%q) 为空，期望中文迁移建议", tc.key)
			}
			if !utf8.ValidString(hint) {
				t.Fatalf("deprecatedHint(%q) 不是完整 UTF-8: %q", tc.key, hint)
			}
			if !strings.Contains(hint, "已废弃") {
				t.Errorf("deprecatedHint(%q) = %q，未标明已废弃", tc.key, hint)
			}
			found := false
			for _, word := range tc.wantAny {
				if strings.Contains(hint, word) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("deprecatedHint(%q) = %q，未给出可操作建议（期望包含 %v）", tc.key, hint, tc.wantAny)
			}
			// 返回值来自固定表，长度必须有界。
			if len(hint) > 512 || utf8.RuneCountInString(hint) > 256 {
				t.Errorf("deprecatedHint(%q) 长度无界: %d 字节 / %d 字符", tc.key, len(hint), utf8.RuneCountInString(hint))
			}
		})
	}
}

// TestDeprecatedHintUnknownKeys 覆盖未知键：空键、含 CJK 的键、大小写
// 变体、近似拼写以及超长键都必须返回空串。
func TestDeprecatedHintUnknownKeys(t *testing.T) {
	longKey := strings.Repeat("logLevel", 64) // 远超 maxDeprecatedHintKeyLen
	cases := []struct {
		name string
		key  string
	}{
		{name: "empty", key: ""},
		{name: "ordinary known key", key: "model"},
		{name: "baseURL", key: "baseURL"},
		{name: "lowercase", key: "loglevel"},
		{name: "uppercase", key: "LOGLEVEL"},
		{name: "underscore spelling", key: "log_level"},
		{name: "camel subagent", key: "subagentDepth"},
		{name: "dashed subagent", key: "subagent-depth"},
		{name: "plural server", key: "servers"},
		{name: "CJK key", key: "模型"},
		{name: "CJK phrase", key: "日志级别"},
		{name: "oversized key", key: longKey},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deprecatedHint(tc.key); got != "" {
				t.Errorf("deprecatedHint(%q) = %q, want empty string", tc.key, got)
			}
		})
	}
}

// TestDeprecatedHintIsPure 验证纯函数约定：重复调用结果一致，
// 且不修改表或其他输入状态。
func TestDeprecatedHintIsPure(t *testing.T) {
	first := deprecatedHint("server")
	for i := 0; i < 3; i++ {
		if got := deprecatedHint("server"); got != first {
			t.Fatalf("第 %d 次调用结果不一致: %q vs %q", i, got, first)
		}
	}
	// 对未知键的重复调用同样稳定。
	first = deprecatedHint("不存在")
	for i := 0; i < 3; i++ {
		if got := deprecatedHint("不存在"); got != first {
			t.Fatalf("未知键第 %d 次调用结果不一致: %q vs %q", i, got, first)
		}
	}
}

// TestTreeDiagnosticsUsesDeprecatedHint 确保废弃键的诊断消息确实
// 采用可操作的迁移建议，而不再只是笼统的 "ignored"。
func TestTreeDiagnosticsUsesDeprecatedHint(t *testing.T) {
	for _, key := range []string{"logLevel", "subagent_depth", "layout", "server"} {
		diags := treeDiagnostics(map[string]any{key: "anything"})
		if len(diags) != 1 {
			t.Fatalf("treeDiagnostics(%q) = %+v, want 1 diagnostic", key, diags)
		}
		if diags[0].Path != "$."+key {
			t.Errorf("diagnostic path = %q, want %q", diags[0].Path, "$."+key)
		}
		if diags[0].Kind != DiagnosticUnsupported {
			t.Errorf("diagnostic kind = %q, want %q", diags[0].Kind, DiagnosticUnsupported)
		}
		if want := deprecatedHint(key); diags[0].Message != want {
			t.Errorf("diagnostic message = %q, want hint %q", diags[0].Message, want)
		}
	}
}
