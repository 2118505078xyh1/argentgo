package app

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"example.com/argentgo/internal/auth"
	"example.com/argentgo/internal/cli"
	"example.com/argentgo/internal/llm"
)

// credentialEnv 与 isolatedEnv 的区别：它不注入任何伪 API key，
// 专用于凭据 fail-fast 本身的表驱动测试。
func credentialEnv(home string, extra map[string]string) func(string) string {
	pairs := map[string]string{"USERPROFILE": home, "HOME": home}
	for key, value := range extra {
		pairs[key] = value
	}
	return func(key string) string { return pairs[key] }
}

func assertCredentialError(t *testing.T, err error, provider string) {
	t.Helper()
	if err == nil {
		t.Fatal("Resolve 应当因无凭据而失败")
	}
	var actionable *llm.ActionableError
	if !errors.As(err, &actionable) {
		t.Fatalf("错误不是 *llm.ActionableError: %T %v", err, err)
	}
	if actionable.Family != llm.FamilyNotConfigured {
		t.Fatalf("Family = %q, want %q", actionable.Family, llm.FamilyNotConfigured)
	}
	for _, path := range []string{
		"argentgo login --device " + provider,
		"--api-key <key>",
		"ARGENT_API_KEY",
	} {
		if !strings.Contains(actionable.Error(), path) {
			t.Fatalf("错误文案缺少路径 %q: %s", path, actionable.Error())
		}
	}
}

// TestResolveFailFastWithoutCredential 锁定 P0-5 的核心决策：需要凭据的提供方
// 在无任何凭据时立即失败；本地提供方（无 key）不受影响。
func TestResolveFailFastWithoutCredential(t *testing.T) {
	cases := []struct {
		name         string
		provider     string
		wantFailFast bool
	}{
		{name: "默认 openai", wantFailFast: true},
		{name: "显式 anthropic", provider: "anthropic", wantFailFast: true},
		{name: "别名 claude", provider: "claude", wantFailFast: true},
		{name: "本地 ollama 豁免", provider: "ollama"},
		{name: "本地 lmstudio 豁免", provider: "lmstudio"},
		{name: "本地 local 豁免", provider: "local"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, home := t.TempDir(), t.TempDir()
			opts := cli.Options{Cwd: root, Provider: tc.provider}
			_, err := Resolve(cli.IO{Env: credentialEnv(home, nil)}, opts)
			if tc.wantFailFast {
				provider := tc.provider
				if provider == "" {
					provider = "openai"
				}
				if provider == "claude" {
					provider = "anthropic"
				}
				assertCredentialError(t, err, provider)
				return
			}
			if err != nil {
				t.Fatalf("本地提供方不应 fail-fast: %v", err)
			}
		})
	}
}

// TestResolveCredentialSourcesSuppressFailFast 验证四个凭据来源中任意一个存在
// 都能抑制 fail-fast：显式 flag、全局/提供方环境变量、配置与 auth store。
func TestResolveCredentialSourcesSuppressFailFast(t *testing.T) {
	cases := []struct {
		name   string
		env    map[string]string
		config string
		flag   string
		store  bool
	}{
		{name: "flag --api-key", flag: "sk-flag"},
		{name: "ARGENT_API_KEY", env: map[string]string{"ARGENT_API_KEY": "sk-global"}},
		{name: "provider env", env: map[string]string{"OPENAI_API_KEY": "sk-env"}},
		{name: "config top-level apiKey", config: `{"apiKey":"sk-config"}`},
		{name: "config provider apiKey", config: `{"providers":{"openai":{"apiKey":"sk-login"}}}`},
		{name: "auth store device token", store: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, home := t.TempDir(), t.TempDir()
			if tc.config != "" {
				writeArgentConfig(t, root, tc.config)
			}
			if tc.store {
				store, err := auth.Open(filepath.Join(DefaultStorage(credentialEnv(home, nil)), "auth"))
				if err != nil {
					t.Fatal(err)
				}
				if err := store.Put(auth.Token{Provider: "openai", Kind: auth.KindOAuth, Access: "sk-device"}); err != nil {
					t.Fatal(err)
				}
			}
			res, err := Resolve(cli.IO{Env: credentialEnv(home, tc.env)},
				cli.Options{Cwd: root, APIKey: tc.flag})
			if err != nil {
				t.Fatalf("有凭据来源却 fail-fast: %v", err)
			}
			if !tc.store && res.APIKey == "" {
				t.Fatal("凭据来源未被解析到 Resolved.APIKey")
			}
		})
	}
}

// TestCredentialNotConfiguredErrorMessage 直接锁定错误文案：即使错误只经过
// "error: <err>" 一行打印（newRunner 阶段），三条路径也必须都在文本里。
func TestCredentialNotConfiguredErrorMessage(t *testing.T) {
	err := newCredentialNotConfiguredError("openai")
	msg := err.Error()
	for _, want := range []string{"not configured", "login --device", "--api-key <key>", "ARGENT_API_KEY"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("文案缺少 %q: %s", want, msg)
		}
	}
	var actionable *llm.ActionableError
	if !errors.As(err, &actionable) || actionable.Family != llm.FamilyNotConfigured {
		t.Fatalf("分类错误: %v", err)
	}
	// ClassifyAPIError 对已分类错误幂等，保证错误流经 writeActionableError 时
	// 不会从自己的脱敏消息中重新派生出别的家族。
	if again := llm.ClassifyAPIError(fmt.Errorf("wrapped: %w", err)); again.Family != llm.FamilyNotConfigured {
		t.Fatalf("幂等分类家族 = %q", again.Family)
	}
}
