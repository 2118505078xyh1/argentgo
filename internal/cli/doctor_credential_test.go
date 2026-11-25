package cli

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"example.com/argentgo/internal/auth"
)

func doctorTestApp(dir string, env map[string]string) App {
	ioState, _, _ := newTestIO("", env)
	return App{IO: ioState, StorageDir: dir, ConfigPath: filepath.Join(dir, "config.json")}
}

func TestCheckCredentialMissing(t *testing.T) {
	dir := t.TempDir()
	check := checkCredential(doctorTestApp(dir, nil))
	if check.status != statusWarn {
		t.Fatalf("status = %q, want warn: %+v", check.status, check)
	}
	for _, want := range []string{"argentgo login --device openai", "--api-key <key>", "ARGENT_API_KEY"} {
		if !strings.Contains(check.detail, want) {
			t.Fatalf("detail 缺少修复命令 %q: %s", want, check.detail)
		}
	}
}

// TestCheckCredentialConfiguredSources 覆盖环境变量、配置文件与 auth store 三类
// 凭据，外加本地 provider 豁免；每一项都应返回 ok。
func TestCheckCredentialConfiguredSources(t *testing.T) {
	cases := []struct {
		name   string
		env    map[string]string
		config string
		token  *auth.Token
	}{
		{name: "ARGENT_API_KEY", env: map[string]string{"ARGENT_API_KEY": "sk-global"}},
		{name: "provider env", env: map[string]string{"OPENAI_API_KEY": "sk-env"}},
		{name: "config top-level apiKey", config: `{"apiKey":"sk-config"}`},
		{name: "config provider apiKey", config: `{"providers":{"openai":{"apiKey":"sk-login"}}}`},
		{name: "auth store valid token", token: &auth.Token{Provider: "openai", Kind: auth.KindOAuth, Access: "sk-device"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.config != "" {
				if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(tc.config), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.token != nil {
				store, err := auth.Open(filepath.Join(dir, "auth"))
				if err != nil {
					t.Fatal(err)
				}
				token := *tc.token
				if err := store.Put(token); err != nil {
					t.Fatal(err)
				}
			}
			check := checkCredential(doctorTestApp(dir, tc.env))
			if check.status != statusOK {
				t.Fatalf("status = %q, want ok: %+v", check.status, check)
			}
		})
	}
}

func TestCheckCredentialExpiredTokenWarns(t *testing.T) {
	dir := t.TempDir()
	store, err := auth.Open(filepath.Join(dir, "auth"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(auth.Token{
		Provider:  "openai",
		Kind:      auth.KindOAuth,
		Access:    "sk-expired",
		ExpiresAt: time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	check := checkCredential(doctorTestApp(dir, nil))
	if check.status != statusWarn || !strings.Contains(check.detail, "expired") ||
		!strings.Contains(check.detail, "argentgo login --device openai") {
		t.Fatalf("过期令牌应给出续期提示: %+v", check)
	}
}

func TestCheckCredentialLocalProviderExempt(t *testing.T) {
	for _, name := range []string{"ollama", "lmstudio", "local"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.json")
			config := `{"provider":"` + name + `"}`
			if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}
			check := checkCredential(doctorTestApp(dir, nil))
			if check.status != statusOK {
				t.Fatalf("本地 provider %q 不应告警: %+v", name, check)
			}
		})
	}
}

// TestCheckProviderUnauthenticated 锁定 401/403 的"可达但未认证"语义：
// 仍为 warn（不计失败、不改退出码），但 detail 给出 login 命令。
func TestCheckProviderUnauthenticated(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
			}))
			defer server.Close()
			check := checkProvider(t.Context(), server.URL)
			if check.status != statusWarn {
				t.Fatalf("status = %q, want warn: %+v", check.status, check)
			}
			for _, want := range []string{"reachable but not authenticated", "argentgo login"} {
				if !strings.Contains(check.detail, want) {
					t.Fatalf("detail 缺少 %q: %s", want, check.detail)
				}
			}
		})
	}
}

func TestCheckProviderStillOKForHealthResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	if check := checkProvider(t.Context(), server.URL); check.status != statusOK {
		t.Fatalf("200 应为 ok: %+v", check)
	}
}
