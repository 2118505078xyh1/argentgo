package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchCatalogModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %s, want /v1/models", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"abc123"`)
		w.Write([]byte(`{"data":[{"id":"gpt-x","name":"GPT X"},{"id":"gpt-y"}]}`))
	}))
	defer srv.Close()

	got, err := FetchCatalog(context.Background(), FetchOptions{BaseURL: srv.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Models) != 2 {
		t.Fatalf("models = %d, want 2", len(got.Models))
	}
	if got.Models[0].ID != "gpt-x" || got.Models[0].Name != "GPT X" {
		t.Fatalf("model[0] = %+v", got.Models[0])
	}
	if got.Models[1].Name != "gpt-y" { // 缺 name 时回退 id
		t.Fatalf("model[1].Name = %q, want id fallback", got.Models[1].Name)
	}
	if got.ETag != `"abc123"` {
		t.Fatalf("ETag = %q", got.ETag)
	}
}

func TestFetchCatalogConditional304(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("If-None-Match"); got != `"abc123"` {
			t.Errorf("If-None-Match = %q", got)
		}
		if got := r.Header.Get("If-Modified-Since"); got != "Mon, 01 Jan 2024 00:00:00 GMT" {
			t.Errorf("If-Modified-Since = %q", got)
		}
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()

	got, err := FetchCatalog(context.Background(), FetchOptions{
		BaseURL:      srv.URL,
		ETag:         `"abc123"`,
		LastModified: "Mon, 01 Jan 2024 00:00:00 GMT",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.NotModified || len(got.Models) != 0 {
		t.Fatalf("304 result = %+v", got)
	}
}

func TestFetchCatalogAuthHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret123" {
			t.Errorf("Authorization = %q", got)
		}
		w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	defer srv.Close()

	_, err := FetchCatalog(context.Background(), FetchOptions{
		BaseURL:    srv.URL,
		AuthHeader: "Authorization",
		AuthValue:  "Bearer secret123",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestFetchCatalogRejectsDuplicateID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"dup"},{"id":"dup"}]}`))
	}))
	defer srv.Close()

	_, err := FetchCatalog(context.Background(), FetchOptions{BaseURL: srv.URL})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("want duplicate id error, got %v", err)
	}
}

func TestFetchCatalogRejectsEmptyID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"  "}]}`))
	}))
	defer srv.Close()

	_, err := FetchCatalog(context.Background(), FetchOptions{BaseURL: srv.URL})
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("want empty id error, got %v", err)
	}
}

func TestFetchCatalogRejectsInvalidBase(t *testing.T) {
	for _, base := range []string{"", "not-a-url", "ftp://x.com/v1", "/relative"} {
		if _, err := FetchCatalog(context.Background(), FetchOptions{BaseURL: base}); err == nil {
			t.Errorf("base %q should be rejected", base)
		}
	}
}

func TestCatalogURLKeepsPathPrefix(t *testing.T) {
	got, err := catalogURL("https://api.example.com/openai/v1/")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://api.example.com/openai/v1/models" {
		t.Fatalf("catalogURL = %q", got)
	}
}

func TestFetchCatalogErrorDoesNotLeakURL(t *testing.T) {
	// 用一个会立即关闭的服务器模拟连接错误，URL 含 query key。
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	urlWithKey := srv.URL + "/v1?api_key=SUPERSECRET"
	srv.Close()

	_, err := FetchCatalog(context.Background(), FetchOptions{BaseURL: urlWithKey})
	if err == nil {
		t.Fatal("expected connection error")
	}
	if strings.Contains(err.Error(), "SUPERSECRET") {
		t.Fatalf("error leaked key: %v", err)
	}
}

func TestFetchCatalogRespectsCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := FetchCatalog(ctx, FetchOptions{BaseURL: srv.URL})
	if err == nil {
		t.Fatal("expected canceled error")
	}
}

func TestSameOrigin(t *testing.T) {
	if !sameOrigin("https://api.openai.com/v1", "https://api.openai.com/v1/models") {
		t.Fatal("same origin should be true")
	}
	if sameOrigin("https://api.openai.com/v1", "https://evil.com/v1/models") {
		t.Fatal("cross origin should be false")
	}
}
