package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// clearSearchPresetEnv 隔离每个用例的 provider/key 环境变量，
// 避免开发者机器上已有的 key 影响默认请求形态的断言。
func clearSearchPresetEnv(t *testing.T) {
	t.Helper()
	t.Setenv(webSearchProviderEnv, "")
	t.Setenv("BRAVE_API_KEY", "")
	t.Setenv("TAVILY_API_KEY", "")
}

func wantDefaultPayload(t *testing.T, query string, limit int) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"query": query, "limit": limit})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

// TestWebSearchDefaultRequestByteForByteBaseline 锁定无 key/预设时的请求基线：
// URL/方法/头/体与接入预设之前逐字节一致。
func TestWebSearchDefaultRequestByteForByteBaseline(t *testing.T) {
	clearSearchPresetEnv(t)

	type captured struct {
		method string
		header http.Header
		body   []byte
	}
	requests := make(chan captured, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- captured{method: r.Method, header: r.Header.Clone(), body: body}
		_, _ = io.WriteString(w, `{"results":[{"title":"Hit","url":"https://h.example"}]}`)
	}))
	defer server.Close()
	t.Setenv(webSearchURLEnv, server.URL)

	out, err := (webSearchTool{}).Run(context.Background(), t.TempDir(), []byte(`{"query":"golang","limit":2}`))
	if err != nil {
		t.Fatalf("web_search: %v", err)
	}
	if !strings.Contains(out, "Hit") {
		t.Fatalf("out = %q, want parsed result", out)
	}
	got := <-requests
	if got.method != http.MethodPost {
		t.Errorf("method = %q, want POST", got.method)
	}
	if want := wantDefaultPayload(t, "golang", 2); string(got.body) != string(want) {
		t.Fatalf("body = %q, want byte-for-byte %q", got.body, want)
	}
	if ct := got.header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if token := got.header.Get("X-Subscription-Token"); token != "" {
		t.Errorf("unexpected X-Subscription-Token = %q", token)
	}
	for name := range got.header {
		lower := strings.ToLower(name)
		if strings.Contains(lower, "token") || strings.Contains(lower, "key") || strings.Contains(lower, "api") {
			t.Errorf("unexpected credential-like header %q", name)
		}
	}
}

// TestWebSearchBraveInjectsSubscriptionHeader 验证 Brave key 通过请求头注入，
// 且请求体保持默认两字段形态。
func TestWebSearchBraveInjectsSubscriptionHeader(t *testing.T) {
	clearSearchPresetEnv(t)
	t.Setenv("BRAVE_API_KEY", "brave-test-token-42")

	type captured struct {
		header http.Header
		body   []byte
	}
	requests := make(chan captured, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- captured{header: r.Header.Clone(), body: body}
		_, _ = io.WriteString(w, `{"results":[{"title":"Brave Hit","url":"https://brave.example"}]}`)
	}))
	defer server.Close()
	t.Setenv(webSearchURLEnv, server.URL)

	out, err := (webSearchTool{}).Run(context.Background(), t.TempDir(), []byte(`{"query":"brave q","limit":1}`))
	if err != nil {
		t.Fatalf("web_search: %v", err)
	}
	if !strings.Contains(out, "Brave Hit") {
		t.Fatalf("out = %q, want Brave result", out)
	}
	got := <-requests
	if token := got.header.Get("X-Subscription-Token"); token != "brave-test-token-42" {
		t.Fatalf("X-Subscription-Token = %q, want the Brave key", token)
	}
	if want := wantDefaultPayload(t, "brave q", 1); string(got.body) != string(want) {
		t.Fatalf("Brave body = %q, want default %q", got.body, want)
	}
}

// captureTransport 拦截 http.DefaultTransport，用于在不触网的前提下
// 断言未显式设置端点时请求被发往预设 URL。
type captureTransport struct {
	mu     sync.Mutex
	url    string
	header http.Header
	body   []byte
}

func (c *captureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	c.mu.Lock()
	c.url = req.URL.String()
	c.header = req.Header.Clone()
	c.body = body
	c.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"results":[]}`)),
		Request:    req,
	}, nil
}

// TestWebSearchBraveUsesPresetURLWhenEndpointUnset 验证只有 key 时，
// 端点自动回落到 Brave 预设 URL（显式 ARGENT_WEB_SEARCH_URL 始终优先）。
func TestWebSearchBraveUsesPresetURLWhenEndpointUnset(t *testing.T) {
	clearSearchPresetEnv(t)
	t.Setenv("BRAVE_API_KEY", "brave-url-token")
	t.Setenv(webSearchURLEnv, "")

	old := http.DefaultTransport
	spy := &captureTransport{}
	http.DefaultTransport = spy
	defer func() { http.DefaultTransport = old }()

	if _, err := (webSearchTool{}).Run(context.Background(), t.TempDir(), []byte(`{"query":"q"}`)); err != nil {
		t.Fatalf("web_search: %v", err)
	}
	spy.mu.Lock()
	defer spy.mu.Unlock()
	brave := SearchPresets[0]
	if spy.url != brave.URL {
		t.Fatalf("request URL = %q, want preset URL %q", spy.url, brave.URL)
	}
	if token := spy.header.Get(brave.HeaderName); token != "brave-url-token" {
		t.Fatalf("%s = %q, want the Brave key", brave.HeaderName, token)
	}
	if want := wantDefaultPayload(t, "q", webSearchMaxResults); string(spy.body) != string(want) {
		t.Fatalf("body = %q, want default %q", spy.body, want)
	}
}

// TestWebSearchTavilySendsKeyInBodyAndParses 验证 Tavily 的 key 走请求体、
// 无自定义请求头，且其响应能被现有宽容解析器解析。
func TestWebSearchTavilySendsKeyInBodyAndParses(t *testing.T) {
	clearSearchPresetEnv(t)
	t.Setenv("TAVILY_API_KEY", "tavily-test-key-77")

	type captured struct {
		header http.Header
		body   []byte
	}
	requests := make(chan captured, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- captured{header: r.Header.Clone(), body: body}
		_, _ = io.WriteString(w, `{"results":[{"title":"Tavily Hit","url":"https://t.example","content":"from tavily"}]}`)
	}))
	defer server.Close()
	t.Setenv(webSearchURLEnv, server.URL)

	out, err := (webSearchTool{}).Run(context.Background(), t.TempDir(), []byte(`{"query":"tavily q","limit":3}`))
	if err != nil {
		t.Fatalf("web_search: %v", err)
	}
	for _, want := range []string{"Tavily Hit", "https://t.example", "from tavily"} {
		if !strings.Contains(out, want) {
			t.Fatalf("out = %q, want %q", out, want)
		}
	}
	got := <-requests
	if !strings.Contains(string(got.body), `"api_key":"tavily-test-key-77"`) {
		t.Fatalf("body = %q, want api_key field", got.body)
	}
	if token := got.header.Get("X-Subscription-Token"); token != "" {
		t.Errorf("Tavily must not set a custom header, got %q", token)
	}
	for name := range got.header {
		lower := strings.ToLower(name)
		if strings.Contains(lower, "token") || strings.Contains(lower, "key") || strings.Contains(lower, "api") {
			t.Errorf("unexpected credential-like header %q", name)
		}
	}
}

// TestWebSearchPresetKeyNeverLeaks 验证 key 不出现在输出或错误信息中。
func TestWebSearchPresetKeyNeverLeaks(t *testing.T) {
	const braveKey = "super-secret-brave-key"

	t.Run("brave error path", func(t *testing.T) {
		clearSearchPresetEnv(t)
		t.Setenv("BRAVE_API_KEY", braveKey)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "denied", http.StatusUnauthorized)
		}))
		defer server.Close()
		t.Setenv(webSearchURLEnv, server.URL)

		out, err := (webSearchTool{}).Run(context.Background(), t.TempDir(), []byte(`{"query":"q"}`))
		if err == nil {
			t.Fatal("want an error from the 401 response")
		}
		if out != "" || strings.Contains(err.Error(), braveKey) {
			t.Fatalf("out = %q, err = %q, key leaked", out, err.Error())
		}
	})

	t.Run("tavily success path", func(t *testing.T) {
		clearSearchPresetEnv(t)
		const tavilyKey = "super-secret-tavily-key"
		t.Setenv("TAVILY_API_KEY", tavilyKey)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"results":[{"title":"ok","url":"https://ok.example"}]}`)
		}))
		defer server.Close()
		t.Setenv(webSearchURLEnv, server.URL)

		out, err := (webSearchTool{}).Run(context.Background(), t.TempDir(), []byte(`{"query":"q"}`))
		if err != nil {
			t.Fatalf("web_search: %v", err)
		}
		if strings.Contains(out, tavilyKey) {
			t.Fatalf("out = %q, key leaked", out)
		}
	})
}

// TestPresetForEnvFailClosedAndCJK 覆盖 helper 的 fail-closed 与 Unicode 安全。
func TestPresetForEnvFailClosedAndCJK(t *testing.T) {
	cases := []struct {
		name   string
		env    map[string]string
		want   string
		wantOK bool
	}{
		{name: "no signals", env: nil},
		{name: "blank provider", env: map[string]string{webSearchProviderEnv: "   "}},
		{name: "unknown provider", env: map[string]string{webSearchProviderEnv: "bing"}},
		{name: "CJK provider is unknown", env: map[string]string{webSearchProviderEnv: "中文搜索"}},
		{name: "explicit brave with spaces", env: map[string]string{webSearchProviderEnv: "  BRAVE  "}, want: "brave", wantOK: true},
		{name: "explicit searxng without key", env: map[string]string{webSearchProviderEnv: "searx"}, want: "searxng", wantOK: true},
		{name: "brave key infers brave", env: map[string]string{"BRAVE_API_KEY": "k"}, want: "brave", wantOK: true},
		{name: "tavily key infers tavily", env: map[string]string{"TAVILY_API_KEY": "k"}, want: "tavily", wantOK: true},
		{name: "brave wins when both keys", env: map[string]string{"BRAVE_API_KEY": "b", "TAVILY_API_KEY": "t"}, want: "brave", wantOK: true},
		{name: "blank keys are ignored", env: map[string]string{"BRAVE_API_KEY": " \t ", "TAVILY_API_KEY": " "}},
		{name: "explicit provider overrides key", env: map[string]string{webSearchProviderEnv: "tavily", "BRAVE_API_KEY": "b"}, want: "tavily", wantOK: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := func(key string) string { return tc.env[key] }
			preset, ok := PresetForEnv(env)
			if ok != tc.wantOK || (ok && preset.Name != tc.want) {
				t.Fatalf("PresetForEnv = (%q, %v), want (%q, %v)", preset.Name, ok, tc.want, tc.wantOK)
			}
			if !ok && preset != (SearchPreset{}) {
				t.Fatalf("fail-closed must return zero value, got %+v", preset)
			}
		})
	}
}
