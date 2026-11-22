package security

import (
	"strings"
	"testing"
)

// TestRedactCLIFlagValue 覆盖 `--password value` 这类空格分隔的 CLI flag 凭据：
// 审计日志里的 bash 命令常以这种写法携带明文口令，必须脱敏。
func TestRedactCLIFlagValue(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		secret string
	}{
		{"long flag", "curl --password hunter2 example.com", "hunter2"},
		{"api key flag", "tool --api-key sk_live_abc123 run", "sk_live_abc123"},
		{"token flag", "deploy --token abc123def456", "abc123def456"},
		{"equals form still works", "export TOKEN=abc123def", "abc123def"},
		{"quoted value", `cmd --password "s3cr3t value"`, "s3cr3t"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Redact(tc.input)
			if strings.Contains(got, tc.secret) {
				t.Fatalf("secret %q leaked into redacted text: %q", tc.secret, got)
			}
		})
	}
}

// TestRedactDoesNotOverRedactProse 确认普通句子里出现的 password/token 不会
// 把下一个词当作机密脱敏（只在紧邻 -/-- 的 CLI flag 形式下才脱敏值）。
func TestRedactDoesNotOverRedactProse(t *testing.T) {
	cases := []string{
		"the password is wrong",
		"token expired yesterday",
		"please reset credential now",
	}
	for _, input := range cases {
		if got := Redact(input); got != input {
			t.Errorf("ordinary prose was redacted: %q -> %q", input, got)
		}
	}
}
