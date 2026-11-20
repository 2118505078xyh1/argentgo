package app

import "testing"

func TestParseTrustEnv(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{"unset defaults to trusted", "", true},
		{"whitespace-only defaults to trusted", "   ", true},
		{"one is trusted", "1", true},
		{"true is trusted", "true", true},
		{"uppercase true is trusted", "TRUE", true},
		{"yes is trusted", "yes", true},
		{"on is trusted", "on", true},
		{"surrounding whitespace is ignored", "  On\t", true},
		{"zero is untrusted", "0", false},
		{"false is untrusted", "false", false},
		{"uppercase false is untrusted", "FALSE", false},
		{"no is untrusted", "no", false},
		{"off is untrusted", "off", false},
		{"mixed-case off with whitespace is untrusted", "  oFf ", false},
		{"unknown value fails closed", "maybe", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseTrustEnv(tt.value); got != tt.want {
				t.Fatalf("parseTrustEnv(%q) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

func TestTrustFromEnv(t *testing.T) {
	env := func(value string) func(string) string {
		return func(key string) string {
			if key == "ARGENT_TRUST_PROJECT" {
				return value
			}
			return ""
		}
	}
	tests := []struct {
		name string
		env  func(string) string
		want bool
	}{
		{"nil env defaults to trusted", nil, true},
		{"unset defaults to trusted", env(""), true},
		{"one is trusted", env("1"), true},
		{"true is trusted", env("true"), true},
		{"yes is trusted", env("yes"), true},
		{"on is trusted", env("on"), true},
		{"zero is untrusted", env("0"), false},
		{"false is untrusted (the reported inconsistency)", env("false"), false},
		{"uppercase FALSE is untrusted", env("FALSE"), false},
		{"no is untrusted", env("no"), false},
		{"off is untrusted", env("off"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := trustFromEnv(tt.env); got != tt.want {
				t.Fatalf("trustFromEnv() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestTrustFromEnvMatchesParseTrustEnv 锁定提示路径与 parseTrustEnv 的任何调用方（尤其是 Resolve 的 TrustProject 字段）
// 以相同方式读取 ARGENT_TRUST_PROJECT 的契约。修复之前这里把 "false" 视为受信任，
// 而 Resolve 视为不受信任——这正是所报告的不一致。
// （接上行）
// audit #5.
func TestTrustFromEnvMatchesParseTrustEnv(t *testing.T) {
	for _, value := range []string{"", "0", "false", "no", "off", "1", "true", "yes", "on", "FALSE", "On", "maybe"} {
		env := func(value string) func(string) string {
			return func(key string) string {
				if key == "ARGENT_TRUST_PROJECT" {
					return value
				}
				return ""
			}
		}(value)
		if got, want := trustFromEnv(env), parseTrustEnv(value); got != want {
			t.Errorf("ARGENT_TRUST_PROJECT=%q: trustFromEnv = %v, parseTrustEnv = %v", value, got, want)
		}
	}
	if trustFromEnv(func(string) string { return "false" }) {
		t.Error("ARGENT_TRUST_PROJECT=false must be untrusted on the prompt path")
	}
}
