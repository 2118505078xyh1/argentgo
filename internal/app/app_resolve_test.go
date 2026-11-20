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

	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/security"
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

// An empty Options.Model/Protocol (as produced by parseArgs when no flag or
// ARGENT_* variable is set) must let the config file and profile win instead
// of the compiled defaults; this is the bug behind "gpt-4o-mini (openai)"
// showing up even when argent.json selects another provider.
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

// `argent login` writes providers.<name>.apiKey; Resolve must actually use it
// when neither a flag, an env var nor a top-level config key is present.
func TestResolveUsesLoginProviderAPIKey(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config string
	}{
		{"apiKey", `{"provider":"openai","providers":{"openai":{"apiKey":"sk-login"}}}`},
		// `argent login openai` only writes providers.openai.apiKey; it does
		// not add a top-level "provider" setting.
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

// A config overlay on a built-in provider must win over the hard-coded
// template. lookupProvider used to call provider.Resolve first, which swallowed
// providers.<name>.baseURL overrides for built-ins such as anthropic/openai.
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

// Reordering lookupProvider (config before built-ins) must not lose aliases:
// "claude" still resolves through provider.Resolve to the anthropic template.
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

// The short flag spellings and the wire names must resolve to one protocol.
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

// End-to-end proof that the login key is not merely parsed but actually sent
// as the Authorization header of the provider request.
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

	// An explicit flag still wins.
	flagOpts := cli.Options{Cwd: root, SandboxMode: "danger-full-access", Approval: "auto"}
	res, err = Resolve(env, flagOpts)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Sandbox != security.SandboxDangerFullAccess || res.Approval != "auto" {
		t.Fatalf("flags did not win: sandbox=%v approval=%q", res.Sandbox, res.Approval)
	}
}

// Resolved.TrustProject must interpret ARGENT_TRUST_PROJECT through the same
// parseTrustEnv helper the prompt path uses: true/yes/on are trusted,
// false/no/off are untrusted. Before the fix Resolve only recognized the
// literal "1", so "true"/"yes"/"on" disagreed with trustFromEnv.
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
		// Unset env leaves the explicit (false) flag in charge: the security
		// gate must not inherit parseTrustEnv's prompt-path "unset = trusted"
		// default, or --trust-project=false would silently be overridden.
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
			// The two paths share parseTrustEnv for explicitly set values.
			// An unset value intentionally differs: the prompt path keeps its
			// legacy "always inject" default while the security gate defers to
			// the explicit flag.
			if tc.value != "" {
				if want := trustFromEnv(env.Env); res.TrustProject != want {
					t.Errorf("ARGENT_TRUST_PROJECT=%q: Resolve TrustProject = %v, prompt path = %v", tc.value, res.TrustProject, want)
				}
			}
		})
	}

	// An explicit --trust flag still wins regardless of the env value.
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
