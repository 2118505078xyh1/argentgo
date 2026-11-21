package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateBaseURLScheme(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want bool // 是否应被拒绝
	}{
		{"https", "https://api.example.test/v1", false},
		{"https uppercase scheme", "HTTPS://api.example.test/v1", false},
		{"localhost http", "http://localhost:11434/v1", false},
		{"ipv4 loopback http", "http://127.0.0.1:8080/v1", false},
		{"ipv6 loopback http", "http://[::1]:8080/v1", false},
		{"remote http", "http://api.example.test/v1", true},
		{"plain http host", "http://evil.test", true},
		{"unsupported scheme", "ftp://api.example.test/v1", true},
		{"no scheme", "api.example.test/v1", true},
		{"empty is left to other diagnostics", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := validateBaseURLScheme(tc.url)
			if (got != "") != tc.want {
				t.Fatalf("validateBaseURLScheme(%q) = %q, want rejected=%v", tc.url, got, tc.want)
			}
		})
	}
}

// doctor/Validate 必须在 baseURL 指向明文远程端点时给出可操作的诊断，
// 同时不把本机回环测试服务器误报为风险。
func TestDiagnosticsWarnOnInsecureBaseURL(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	writeJSONFile(t, filepath.Join(cwd, "argent.json"), map[string]any{
		"baseURL": "http://evil.test/v1",
		"providers": map[string]any{
			"openai": map[string]any{"baseURL": "http://provider.test/v1"},
			"ollama": map[string]any{"baseURL": "http://127.0.0.1:11434/v1"},
		},
	})
	cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	paths := map[string]string{}
	for _, d := range cfg.Diagnostics() {
		paths[d.Path] = d.Message
	}
	for _, want := range []string{"$.baseURL", "$.providers.openai.baseURL"} {
		message, ok := paths[want]
		if !ok {
			t.Fatalf("missing diagnostic for %s: %+v", want, paths)
		}
		if !strings.Contains(message, "https") {
			t.Errorf("diagnostic for %s is not actionable: %q", want, message)
		}
	}
	if _, ok := paths["$.providers.ollama.baseURL"]; ok {
		t.Error("loopback provider baseURL must not produce a diagnostic")
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "http") {
		t.Errorf("Validate = %v, want an http complaint", err)
	}
}
