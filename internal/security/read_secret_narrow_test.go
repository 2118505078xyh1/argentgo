package security

import (
	"encoding/json"
	"testing"
)

// TestBroadReadRuleNarrowsSecretFiles 验证 #22：宽泛 read 规则命中密钥文件时
// 自动收窄为 Ask；*.env.example 放行，显式 DenyPaths 优先于收窄。
func TestBroadReadRuleNarrowsSecretFiles(t *testing.T) {
	tests := []struct {
		name   string
		policy *Policy
		tool   string
		args   string
		want   Decision
	}{
		{"default policy bare .env", DefaultPolicy(t.TempDir()), "read", `{"path":".env"}`, Ask},
		{"default policy env.local", DefaultPolicy(t.TempDir()), "read", `{"path":"src/.env.local"}`, Ask},
		{"broad allow rule env.local", broadReadPolicy(t.TempDir()), "read", `{"path":".env.local"}`, Ask},
		{"broad allow rule nested production", broadReadPolicy(t.TempDir()), "read", `{"path":"src/.env.production"}`, Ask},
		{"case insensitive env", broadReadPolicy(t.TempDir()), "read", `{"path":".ENV.LOCAL"}`, Ask},
		{"suffix env file", broadReadPolicy(t.TempDir()), "read", `{"path":"config/.env"}`, Ask},
		{"foo.env", broadReadPolicy(t.TempDir()), "read", `{"path":"foo.env"}`, Ask},
		{"example is allowed", broadReadPolicy(t.TempDir()), "read", `{"path":".env.example"}`, Allow},
		{"nested example is allowed", broadReadPolicy(t.TempDir()), "read", `{"path":"src/foo.env.example"}`, Allow},
		{"normal file under broad rule", broadReadPolicy(t.TempDir()), "read", `{"path":"src/main.go"}`, Allow},
		{"config file is not a secret env", broadReadPolicy(t.TempDir()), "read", `{"path":"config.json"}`, Allow},
		{"deny paths take priority", strictEnvPolicy(t.TempDir()), "read", `{"path":".env.local"}`, Deny},
		{"write is unaffected", DefaultPolicy(t.TempDir()), "write", `{"path":"src/main.go"}`, Ask},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.policy.Check(tt.tool, json.RawMessage(tt.args)); got != tt.want {
				t.Fatalf("%s %s: decision = %v, want %v", tt.tool, tt.args, got, tt.want)
			}
		})
	}
}

func broadReadPolicy(root string) *Policy {
	return NewPolicy(root, PolicyConfig{
		Rules: []Rule{{Decision: Allow, Tool: "*"}},
	})
}

func strictEnvPolicy(root string) *Policy {
	return NewPolicy(root, PolicyConfig{DenyPaths: []string{"**/.env*"}})
}
