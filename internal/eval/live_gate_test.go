package eval

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// 本文件只测纯门禁原语：不联网、不读真实进程环境（IgnoresProcessEnv 用 t.Setenv 种毒药值反证）、
// 不写 golden/testdata，也不修改任何入参。

func liveEnv(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestReadLiveConfigDefaultOffline(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
	}{
		{"missing switch", nil},
		{"live zero", map[string]string{envLiveEnabled: "0"}},
		{"live true literal", map[string]string{envLiveEnabled: "true"}},
		{"live padded left", map[string]string{envLiveEnabled: " 1"}},
		{"live padded right", map[string]string{envLiveEnabled: "1 "}},
		{"live newline", map[string]string{envLiveEnabled: "1\n"}},
		{"key present but switch absent", map[string]string{envAPIKey: "sk-poison", envProtocol: "bogus"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ReadLiveConfig(liveEnv(tc.env))
			if err != nil {
				t.Fatalf("ReadLiveConfig() error = %v, want nil", err)
			}
			if cfg != (LiveConfig{}) {
				t.Fatalf("ReadLiveConfig() = %+v, want zero (default offline)", cfg)
			}
		})
	}
}

func TestReadLiveConfigEnabled(t *testing.T) {
	env := map[string]string{
		envLiveEnabled: "1",
		envProtocol:    "anthropic",
		envModel:       "claude-test",
		envBaseURL:     "https://api.example.test/v1",
		envAPIKey:      "sk-secret",
	}
	cfg, err := ReadLiveConfig(liveEnv(env))
	if err != nil {
		t.Fatalf("ReadLiveConfig() error = %v", err)
	}
	want := LiveConfig{
		Enabled:  true,
		Protocol: "anthropic",
		Model:    "claude-test",
		BaseURL:  "https://api.example.test/v1",
		APIKey:   "sk-secret",
	}
	if cfg != want {
		t.Fatalf("ReadLiveConfig() = %+v, want %+v", cfg, want)
	}
}

func TestReadLiveConfigKeyFallback(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{
			name: "argent key wins",
			env:  map[string]string{envAPIKey: "sk-argent", envOpenAIKey: "sk-openai"},
			want: "sk-argent",
		},
		{
			name: "fallback to openai key",
			env:  map[string]string{envAPIKey: "", envOpenAIKey: "sk-openai"},
			want: "sk-openai",
		},
		{
			name: "whitespace argent key falls back and trims",
			env:  map[string]string{envAPIKey: "  \t", envOpenAIKey: "  sk-openai  "},
			want: "sk-openai",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.env[envLiveEnabled] = "1"
			cfg, err := ReadLiveConfig(liveEnv(tc.env))
			if err != nil {
				t.Fatalf("ReadLiveConfig() error = %v", err)
			}
			if cfg.APIKey != tc.want {
				t.Fatalf("APIKey = %q, want %q", cfg.APIKey, tc.want)
			}
		})
	}
}

func TestReadLiveConfigRequiresKey(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
	}{
		{"no key variables", map[string]string{envLiveEnabled: "1", envProtocol: "chat"}},
		{"both keys empty", map[string]string{envLiveEnabled: "1", envAPIKey: "", envOpenAIKey: ""}},
		{"both keys whitespace", map[string]string{envLiveEnabled: "1", envAPIKey: " ", envOpenAIKey: "\t"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ReadLiveConfig(liveEnv(tc.env))
			if !errors.Is(err, ErrLiveRequiresKey) {
				t.Fatalf("ReadLiveConfig() error = %v, want ErrLiveRequiresKey", err)
			}
			if !cfg.Enabled {
				t.Fatal("cfg.Enabled = false, want true so the error context is preserved")
			}
			if cfg.APIKey != "" {
				t.Fatalf("cfg.APIKey = %q, want empty", cfg.APIKey)
			}
		})
	}
}

func TestReadLiveConfigNilEnv(t *testing.T) {
	cfg, err := ReadLiveConfig(nil)
	if err != nil || cfg != (LiveConfig{}) {
		t.Fatalf("ReadLiveConfig(nil) = (%+v, %v), want (zero, nil)", cfg, err)
	}
}

func TestReadLiveConfigDoesNotMutateEnv(t *testing.T) {
	env := map[string]string{
		envLiveEnabled: "1",
		envProtocol:    "chat",
		envModel:       "模型",
		envAPIKey:      "sk-secret",
	}
	before := make(map[string]string, len(env))
	for k, v := range env {
		before[k] = v
	}
	if _, err := ReadLiveConfig(liveEnv(env)); err != nil {
		t.Fatalf("ReadLiveConfig() error = %v", err)
	}
	if !reflect.DeepEqual(env, before) {
		t.Fatalf("env was mutated: got %v, want %v", env, before)
	}
}

func TestReadLiveConfigIgnoresProcessEnv(t *testing.T) {
	// 给进程环境种毒药值；注入的 env 始终是空的，函数必须视而不见。
	t.Setenv(envLiveEnabled, "1")
	t.Setenv(envAPIKey, "sk-from-process")
	t.Setenv(envProtocol, "bogus")
	cfg, err := ReadLiveConfig(liveEnv(map[string]string{}))
	if err != nil || cfg != (LiveConfig{}) {
		t.Fatalf("ReadLiveConfig() = (%+v, %v), want (zero, nil) despite process env", cfg, err)
	}
}

func TestLiveConfigRedacted(t *testing.T) {
	cases := []struct {
		name     string
		cfg      LiveConfig
		mask     string
		contains []string
		leaks    []string
	}{
		{
			name:     "ascii key shows length only",
			cfg:      LiveConfig{Enabled: true, Protocol: "chat", Model: "m", BaseURL: "https://api.test/v1", APIKey: "sk-abcdef"},
			mask:     "***(len=9)",
			contains: []string{"enabled=true", "m", "https://api.test/v1"},
			leaks:    []string{"sk-abcdef", "abcdef"},
		},
		{
			name:     "cjk key counts runes and never leaks",
			cfg:      LiveConfig{Enabled: true, Model: "模型中文", APIKey: "密钥abc"},
			mask:     "***(len=5)",
			contains: []string{"模型中文"},
			leaks:    []string{"密钥abc", "密钥", "abc"},
		},
		{
			name: "empty key",
			cfg:  LiveConfig{APIKey: ""},
			mask: "<empty>",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.cfg.Redacted()
			if !strings.Contains(got, tc.mask) {
				t.Errorf("Redacted() = %q, want mask %q", got, tc.mask)
			}
			for _, needle := range tc.contains {
				if !strings.Contains(got, needle) {
					t.Errorf("Redacted() = %q, want it to contain %q", got, needle)
				}
			}
			for _, leak := range tc.leaks {
				if strings.Contains(got, leak) {
					t.Errorf("Redacted() leaked API key fragment %q: %q", leak, got)
				}
			}
		})
	}
}

func TestLiveConfigValidateProtocol(t *testing.T) {
	valid := []string{
		"", "chat", "openai", "openai-chat-completions",
		"anthropic", "anthropic-messages", "messages",
		"responses", "openai-responses",
		" CHAT ", "Anthropic", "  openai-responses  ",
	}
	for _, protocol := range valid {
		cfg := LiveConfig{Protocol: protocol}
		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate(%q) error = %v, want nil", protocol, err)
		}
	}
	invalid := []string{"bogus", "openai-completions", "claude", "中文协议"}
	for _, protocol := range invalid {
		cfg := LiveConfig{Protocol: protocol}
		if err := cfg.Validate(); !errors.Is(err, ErrLiveInvalidProtocol) {
			t.Errorf("Validate(%q) error = %v, want ErrLiveInvalidProtocol", protocol, err)
		}
	}
}

func TestLiveConfigValidateRequiresKeyWhenEnabled(t *testing.T) {
	cfg := LiveConfig{Enabled: true, Protocol: "chat"}
	if err := cfg.Validate(); !errors.Is(err, ErrLiveRequiresKey) {
		t.Fatalf("Validate() error = %v, want ErrLiveRequiresKey", err)
	}
	cfg.APIKey = "sk-present"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v with a key present", err)
	}
}

func TestLiveConfigCanonicalProtocol(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "openai-chat-completions"},
		{"chat", "openai-chat-completions"},
		{"openai", "openai-chat-completions"},
		{"openai-chat-completions", "openai-chat-completions"},
		{"anthropic", "anthropic-messages"},
		{"messages", "anthropic-messages"},
		{"anthropic-messages", "anthropic-messages"},
		{"responses", "openai-responses"},
		{"openai-responses", "openai-responses"},
		{"  Responses  ", "openai-responses"},
		{"bogus", "bogus"},
	}
	for _, tc := range cases {
		cfg := LiveConfig{Protocol: tc.in}
		if got := cfg.CanonicalProtocol(); got != tc.want {
			t.Errorf("CanonicalProtocol(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestLiveConfigMethodsDoNotMutateReceiver(t *testing.T) {
	cfg := LiveConfig{Enabled: true, Protocol: "chat", Model: "m", BaseURL: "u", APIKey: "sk-secret"}
	before := cfg
	_ = cfg.Redacted()
	_ = cfg.Validate()
	_ = cfg.CanonicalProtocol()
	if cfg != before {
		t.Fatalf("receiver was mutated: got %+v, want %+v", cfg, before)
	}
}
