package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"example.com/argentgo/internal/agent"
	"example.com/argentgo/internal/cli"
	"example.com/argentgo/internal/llm"
	"example.com/argentgo/internal/security"
)

func writeArgentConfig(t *testing.T, root, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "argent.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func isolatedEnv(home string, extra map[string]string) func(string) string {
	pairs := map[string]string{"USERPROFILE": home, "HOME": home}
	for key, value := range extra {
		pairs[key] = value
	}
	return envFrom(pairs)
}

// 空的 Options.Model/Protocol（当没有设置标志或 ARGENT_* 变量时，由 parseArgs 产生）
// 必须让配置文件和 profile 优先，而非
// 编译默认值；这就是即使 argent.json 选择了另一个提供方，仍显示
// "gpt-4o-mini (openai)" 的那个 bug。
func TestResolveConfigModelAndProviderBeatCompiledDefaults(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	writeArgentConfig(t, root, `{"provider":"anthropic","model":"claude-test"}`)
	env := isolatedEnv(home, nil)

	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: root})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Model != "claude-test" {
		t.Errorf("model = %q, want claude-test", res.Model)
	}
	if res.Provider != "anthropic" {
		t.Errorf("provider = %q, want anthropic", res.Provider)
	}
	if res.Protocol != "anthropic-messages" {
		t.Errorf("protocol = %q, want the anthropic wire name", res.Protocol)
	}
	report, err := TokenReport(cli.IO{Env: env}, cli.Options{Cwd: root})
	if err != nil {
		t.Fatalf("TokenReport: %v", err)
	}
	if !strings.Contains(report, "model: claude-test (anthropic-messages)") {
		t.Errorf("token report does not reflect config:\n%s", report)
	}
}

// 默认轮次预算必须与引擎对齐：旧默认 12 会过早截断长任务，把未完成的
// 工作包装成正常结束。这里同时覆盖配置层默认值和 app 层兜底（配置为 0）。
func TestResolveDefaultMaxTurnsAlignsWithEngine(t *testing.T) {
	if agent.DefaultMaxTurns != 100 {
		t.Fatalf("engine DefaultMaxTurns = %d, want 100", agent.DefaultMaxTurns)
	}

	t.Run("no config uses config default", func(t *testing.T) {
		root := t.TempDir()
		res, err := Resolve(cli.IO{Env: isolatedEnv(t.TempDir(), nil)}, cli.Options{Cwd: root})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if res.MaxTurns != agent.DefaultMaxTurns {
			t.Fatalf("MaxTurns = %d, want %d", res.MaxTurns, agent.DefaultMaxTurns)
		}
	})

	t.Run("zero config falls back to engine default", func(t *testing.T) {
		root := t.TempDir()
		writeArgentConfig(t, root, `{"maxTurns":0}`)
		res, err := Resolve(cli.IO{Env: isolatedEnv(t.TempDir(), nil)}, cli.Options{Cwd: root})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if res.MaxTurns != agent.DefaultMaxTurns {
			t.Fatalf("MaxTurns = %d, want fallback %d", res.MaxTurns, agent.DefaultMaxTurns)
		}
	})
}

func TestResolveProfileModelAndProviderBeatCompiledDefaults(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	writeArgentConfig(t, root, `{
		"profile": "work",
		"profiles": {"work": {"model": "work-model", "provider": "ollama"}}
	}`)

	res, err := Resolve(cli.IO{Env: isolatedEnv(home, nil)}, cli.Options{Cwd: root})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Model != "work-model" || res.Provider != "ollama" {
		t.Fatalf("profile not applied: model=%q provider=%q", res.Model, res.Provider)
	}
}

// `argentgo login` 写入 providers.<name>.apiKey；当没有标志、环境变量或顶层配置键时，
// Resolve 必须实际使用它。
func TestResolveUsesLoginProviderAPIKey(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config string
	}{
		{"apiKey", `{"provider":"openai","providers":{"openai":{"apiKey":"sk-login"}}}`},
		// `argentgo login openai` 只写入 providers.openai.apiKey；它
		// 不会添加顶层的 "provider" 设置。
		{"no top-level provider", `{"providers":{"openai":{"apiKey":"sk-login"}}}`},
		{"api_key alias", `{"provider":"openai","providers":{"openai":{"api_key":"sk-login"}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, home := t.TempDir(), t.TempDir()
			writeArgentConfig(t, root, tc.config)
			res, err := Resolve(cli.IO{Env: isolatedEnv(home, nil)}, cli.Options{Cwd: root})
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if res.APIKey != "sk-login" {
				t.Errorf("apiKey = %q, want the login key", res.APIKey)
			}
		})
	}
}

// 内置提供方上的配置覆盖层必须优先于硬编码的
// 模板。lookupProvider 以前先调用 provider.Resolve，这会吞掉
// 对 anthropic/openai 等内置项的 providers.<name>.baseURL 覆盖。
func TestResolveProviderConfigOverridesBuiltinBaseURL(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	writeArgentConfig(t, root, `{
		"provider":"anthropic",
		"providers":{"anthropic":{"baseURL":"https://custom.anthropic.test/v1"}}
	}`)
	res, err := Resolve(cli.IO{Env: isolatedEnv(home, nil)}, cli.Options{Cwd: root})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.BaseURL != "https://custom.anthropic.test/v1" {
		t.Errorf("baseURL = %q, want the config override of the built-in provider", res.BaseURL)
	}
}

// 重排 lookupProvider（配置优先于内置项）不得丢失别名：
// "claude" 仍通过 provider.Resolve 解析为 anthropic 模板。
func TestResolveProviderAliasStillResolves(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	res, err := Resolve(cli.IO{Env: isolatedEnv(home, nil)}, cli.Options{Cwd: root, Provider: "claude"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Provider != "anthropic" || res.Protocol != "anthropic-messages" {
		t.Fatalf("alias not resolved: provider=%q protocol=%q", res.Provider, res.Protocol)
	}
	if !strings.Contains(res.BaseURL, "anthropic.com") {
		t.Errorf("baseURL = %q, want the anthropic endpoint", res.BaseURL)
	}
}

func TestResolveAPIKeyPrecedence(t *testing.T) {
	cases := []struct {
		name   string
		config string
		opts   cli.Options
		env    map[string]string
		want   string
	}{
		{
			name:   "top-level config beats provider config",
			config: `{"apiKey":"sk-top","providers":{"openai":{"apiKey":"sk-provider"}}}`,
			want:   "sk-top",
		},
		{
			name:   "provider config used when top-level absent",
			config: `{"providers":{"openai":{"apiKey":"sk-provider"}}}`,
			want:   "sk-provider",
		},
		{
			name:   "flag beats config",
			config: `{"apiKey":"sk-top","providers":{"openai":{"apiKey":"sk-provider"}}}`,
			opts:   cli.Options{APIKey: "sk-flag"},
			want:   "sk-flag",
		},
		{
			name:   "env beats config",
			config: `{"apiKey":"sk-top","providers":{"openai":{"apiKey":"sk-provider"}}}`,
			env:    map[string]string{"ARGENT_API_KEY": "sk-env"},
			want:   "sk-env",
		},
		{
			name:   "provider env is the last resort",
			config: `{}`,
			env:    map[string]string{"OPENAI_API_KEY": "sk-openai-env"},
			want:   "sk-openai-env",
		},
		{
			name:   "key follows the selected provider",
			config: `{"provider":"anthropic","providers":{"openai":{"apiKey":"sk-openai"},"anthropic":{"apiKey":"sk-claude"}}}`,
			want:   "sk-claude",
		},
		{
			name:   "explicit provider flag selects its key",
			config: `{"providers":{"openai":{"apiKey":"sk-openai"},"anthropic":{"apiKey":"sk-claude"}}}`,
			opts:   cli.Options{Provider: "anthropic"},
			want:   "sk-claude",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, home := t.TempDir(), t.TempDir()
			writeArgentConfig(t, root, tc.config)
			tc.opts.Cwd = root
			res, err := Resolve(cli.IO{Env: isolatedEnv(home, tc.env)}, tc.opts)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if res.APIKey != tc.want {
				t.Errorf("apiKey = %q, want %q", res.APIKey, tc.want)
			}
		})
	}
}

// 标志的简短写法和线上名称必须解析为同一个协议。
func TestResolveCanonicalizesProtocol(t *testing.T) {
	cases := []struct {
		protocol string
		want     string
	}{
		{"", "openai-chat-completions"},
		{"openai", "openai-chat-completions"},
		{"openai-chat-completions", "openai-chat-completions"},
		{"anthropic", "anthropic-messages"},
		{"anthropic-messages", "anthropic-messages"},
		{"responses", "openai-responses"},
		{"openai-responses", "openai-responses"},
	}
	for _, tc := range cases {
		t.Run(tc.protocol, func(t *testing.T) {
			root, home := t.TempDir(), t.TempDir()
			res, err := Resolve(cli.IO{Env: isolatedEnv(home, nil)}, cli.Options{Cwd: root, Protocol: tc.protocol})
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if res.Protocol != tc.want {
				t.Errorf("protocol = %q, want %q", res.Protocol, tc.want)
			}
		})
	}
}

// 端到端证明登录密钥不只是被解析，而是实际作为提供方请求的
// Authorization 头发送出去。
func TestResolveLoginKeyReachesProviderRequest(t *testing.T) {
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"gpt-4o-mini",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer server.Close()

	root, home := t.TempDir(), t.TempDir()
	writeArgentConfig(t, root, fmt.Sprintf(
		`{"provider":"openai","baseURL":%q,"providers":{"openai":{"apiKey":"sk-login-wire"}}}`,
		server.URL+"/v1",
	))
	env := isolatedEnv(home, map[string]string{"OPENAI_API_KEY": "", "ARGENT_API_KEY": ""})
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: root})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, err := res.Chatter.Chat(
		context.Background(),
		res.Model,
		[]llm.Message{{Role: "user", Content: "hi"}},
		nil,
		nil,
	); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if authorization != "Bearer sk-login-wire" {
		t.Errorf("Authorization = %q, want the login key", authorization)
	}
}

func TestResolveSandboxAndApprovalFromConfig(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	writeArgentConfig(t, root, `{"sandboxMode":"read-only","approval":"never"}`)
	env := cli.IO{Env: isolatedEnv(home, nil)}

	res, err := Resolve(env, cli.Options{Cwd: root})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Sandbox != security.SandboxReadOnly {
		t.Errorf("sandbox = %v, want read-only", res.Sandbox)
	}
	if res.Approval != "never" {
		t.Errorf("approval = %q, want never", res.Approval)
	}

	// 显式标志仍然优先。
	flagOpts := cli.Options{Cwd: root, SandboxMode: "danger-full-access", Approval: "auto"}
	res, err = Resolve(env, flagOpts)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Sandbox != security.SandboxDangerFullAccess || res.Approval != "auto" {
		t.Fatalf("flags did not win: sandbox=%v approval=%q", res.Sandbox, res.Approval)
	}
}

// TestResolveYesFlagMapsToAuto 验证 --yes 与 --yolo 一样把审批模式切到 auto。
// 回归：--yes 曾被 flags 解析却未参与审批判定，导致非交互 --yes 仍走 ask，
// PromptApprover 向 stdout 打印 "Allow...?" 并因 stdin 关闭误判为拒绝。
func TestResolveYesFlagMapsToAuto(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	env := cli.IO{Env: isolatedEnv(home, nil)}
	for _, tc := range []struct {
		name string
		opts cli.Options
	}{
		{"yes", cli.Options{Cwd: root, Yes: true}},
		{"yolo", cli.Options{Cwd: root, Yolo: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Resolve(env, tc.opts)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if res.Approval != "auto" {
				t.Fatalf("approval = %q, want auto", res.Approval)
			}
		})
	}
}

// Resolved.TrustProject 必须通过提示路径使用的同一个 parseTrustEnv 辅助函数，
// 来解释 ARGENT_TRUST_PROJECT：true/yes/on 为受信任，
// false/no/off 为不受信任。修复之前 Resolve 只识别字面量 "1"，
// 因此 "true"/"yes"/"on" 与 trustFromEnv 不一致。
func TestResolveTrustProjectFromEnv(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"true", true},
		{"yes", true},
		{"on", true},
		{"1", true},
		{"false", false},
		{"no", false},
		{"off", false},
		{"0", false},
		// 未设置环境变量时，由显式的（false）标志主导：安全
		// 门控不得继承 parseTrustEnv 在提示路径上的 "未设置 = 受信任"
		// 默认值，否则 --trust-project=false 会被静默覆盖。
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			root, home := t.TempDir(), t.TempDir()
			writeArgentConfig(t, root, `{}`)
			env := cli.IO{Env: isolatedEnv(home, map[string]string{"ARGENT_TRUST_PROJECT": tc.value})}
			res, err := Resolve(env, cli.Options{Cwd: root})
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if res.TrustProject != tc.want {
				t.Errorf("ARGENT_TRUST_PROJECT=%q: TrustProject = %v, want %v", tc.value, res.TrustProject, tc.want)
			}
			// 两条路径对显式设置的值共享 parseTrustEnv。
			// 未设置的值有意不同：提示路径保留其
			// 旧版 "总是注入" 默认值，而安全门控则遵从
			// 显式标志。
			if tc.value != "" {
				if want := trustFromEnv(env.Env); res.TrustProject != want {
					t.Errorf("ARGENT_TRUST_PROJECT=%q: Resolve TrustProject = %v, prompt path = %v", tc.value, res.TrustProject, want)
				}
			}
		})
	}

	// 无论环境变量值如何，显式的 --trust 标志仍然优先。
	root, home := t.TempDir(), t.TempDir()
	writeArgentConfig(t, root, `{}`)
	env := cli.IO{Env: isolatedEnv(home, map[string]string{"ARGENT_TRUST_PROJECT": "off"})}
	res, err := Resolve(env, cli.Options{Cwd: root, TrustProject: true})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !res.TrustProject {
		t.Error("explicit TrustProject option must win over an untrusted env value")
	}
}
