package app

import (
	"context"
	"testing"

	"example.com/argentgo/internal/security"
)

func TestNetworkGuardFromRules(t *testing.T) {
	raw := map[string]any{
		"net": map[string]any{
			"rules": []any{
				map[string]any{"decision": "deny", "hostGlob": "*.evil.example"},
			},
		},
	}
	guard := networkGuardFrom(raw, nil, nil)

	// 配置的 deny 规则必须拦截对应域名。
	if err := security.CheckNetworkEgress(
		security.WithNetworkGuard(context.Background(), guard),
		"https://api.evil.example/x", "GET", false,
	); err == nil {
		t.Fatal("configured deny rule must block matching host")
	}
	// 未命中规则的公网地址仍放行（内置默认策略）。
	if err := security.CheckNetworkEgress(
		security.WithNetworkGuard(context.Background(), guard),
		"https://example.com/x", "GET", false,
	); err != nil {
		t.Fatalf("unmatched public host should be allowed: %v", err)
	}
}

func TestNetworkGuardPolicyDisabled(t *testing.T) {
	raw := map[string]any{
		"net": map[string]any{
			"rules": []any{map[string]any{"decision": "deny", "hostGlob": "*.evil.example"}},
		},
	}
	env := func(k string) string {
		if k == netPolicyEnv {
			return "0"
		}
		return ""
	}
	guard := networkGuardFrom(raw, nil, env)
	// =0 回退后配置的 deny 不再生效。
	if err := security.CheckNetworkEgress(
		security.WithNetworkGuard(context.Background(), guard),
		"https://api.evil.example/x", "GET", false,
	); err != nil {
		t.Fatalf("ARGENT_NET_POLICY=0 must ignore configured rules: %v", err)
	}
}

func TestNetPolicyEnabledDefaultOn(t *testing.T) {
	if !netPolicyEnabled(nil) {
		t.Fatal("nil env defaults on")
	}
	if !netPolicyEnabled(func(string) string { return "" }) {
		t.Fatal("empty value defaults on")
	}
	if netPolicyEnabled(func(string) string { return "0" }) {
		t.Fatal("=0 off")
	}
}
