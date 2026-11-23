package config

import (
	"reflect"
	"testing"
)

func TestParseNetConfig(t *testing.T) {
	raw := map[string]any{
		"net": map[string]any{
			"rules": []any{
				map[string]any{
					"decision": "deny", "hostGlob": "*.evil.example", "port": float64(8443),
					"protocol": "HTTPS", "method": "post", "requireHuman": true,
				},
				map[string]any{"decision": "ask", "host_glob": "*. Corp .EXAMPLE", "require_human": "1"},
				map[string]any{"decision": "bogus", "hostGlob": "ignored.example"}, // 非法判定丢弃
				"not-an-object", // 非对象条目丢弃
			},
		},
	}
	got := ParseNetConfig(raw).Rules
	want := []NetRuleConfig{
		{Decision: "deny", HostGlob: "*.evil.example", Port: 8443, Protocol: "https", Method: "POST", RequireHuman: true},
		{Decision: "ask", HostGlob: "*. Corp .EXAMPLE", RequireHuman: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rules = %+v, want %+v", got, want)
	}
}

func TestParseNetConfigShorthandAndAbsent(t *testing.T) {
	// 数组简写
	got := ParseNetConfig(map[string]any{
		"net": []any{map[string]any{"decision": "allow", "host": "good.example"}},
	}).Rules
	if len(got) != 1 || got[0].HostGlob != "good.example" || got[0].Decision != "allow" {
		t.Fatalf("shorthand rules = %+v", got)
	}
	// 缺失 / false / 类型错误一律零值
	for _, raw := range []map[string]any{
		nil,
		{},
		{"net": nil},
		{"net": false},
		{"net": "garbage"},
		{"net": map[string]any{"rules": "not-array"}},
	} {
		if r := ParseNetConfig(raw).Rules; len(r) != 0 {
			t.Fatalf("raw=%v want no rules, got %+v", raw, r)
		}
	}
}
