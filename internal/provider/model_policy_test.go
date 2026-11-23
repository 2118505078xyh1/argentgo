package provider

import "testing"

func TestModelPolicyExactAndFamily(t *testing.T) {
	// exact 覆盖：gpt-4o-mini 命中精确条目（即使 gpt 内置表是 128k，值相同也要验证命中路径）。
	if got := policyWindow("gpt-4o-mini"); got != 128000 {
		t.Fatalf("exact policyWindow = %d", got)
	}
	// family 最长匹配。
	if got := policyWindow("llama-3.3-70b"); got != 131072 {
		t.Fatalf("family policyWindow = %d", got)
	}
	if got := policyWindow("totally-unknown-model"); got != 0 {
		t.Fatalf("unknown policyWindow = %d, want 0", got)
	}
}

func TestModelPolicyFallbackToBuiltin(t *testing.T) {
	// 未在 JSON 声明的模型仍走内置表，保持旧行为。
	if got := KnownContextWindow("claude-sonnet-4-5"); got != 200_000 {
		t.Fatalf("builtin fallback = %d", got)
	}
}

func TestLoadModelPolicyInvalid(t *testing.T) {
	p := loadModelPolicy([]byte("not json"))
	if len(p.Exact) != 0 || len(p.Family) != 0 {
		t.Fatal("invalid JSON should yield empty policy")
	}
}
