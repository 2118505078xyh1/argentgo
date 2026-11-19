package tools_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argent-go/internal/tools"
)

type webSearchRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

func TestWebSearchPostsJSONQueryAndParsesResults(t *testing.T) {
	requests := make(chan webSearchRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		var request webSearchRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		requests <- request
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"results":[{"title":"Go","url":"https://go.dev","snippet":"The Go language"},{"title":"Rust","link":"https://rust-lang.org","description":"Systems language"}]}`)
	}))
	defer server.Close()
	t.Setenv("ARGENT_WEB_SEARCH_URL", server.URL)

	registry := tools.New(t.TempDir(), true)
	out, err := registry.Run(context.Background(), "web_search", `{"query":"golang","limit":2}`)
	if err != nil {
		t.Fatalf("web_search: %v", err)
	}
	for _, want := range []string{"Go", "https://go.dev", "The Go language", "Rust", "https://rust-lang.org"} {
		if !strings.Contains(out, want) {
			t.Fatalf("web_search = %q, want %q", out, want)
		}
	}
	got := <-requests
	if got.Query != "golang" || got.Limit != 2 {
		t.Fatalf("request = %+v, want query=golang limit=2", got)
	}
}

func TestWebSearchAcceptsBareArrayWithAlternateKeys(t *testing.T) {
	requests := make(chan webSearchRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request webSearchRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		requests <- request
		_, _ = io.WriteString(w, `[{"name":"Alpha","link":"https://a.example","text":"first hit"}]`)
	}))
	defer server.Close()
	t.Setenv("ARGENT_WEB_SEARCH_URL", server.URL)

	registry := tools.New(t.TempDir(), true)
	out, err := registry.Run(context.Background(), "web_search", `{"query":"alpha"}`)
	if err != nil {
		t.Fatalf("web_search: %v", err)
	}
	for _, want := range []string{"Alpha", "https://a.example", "first hit"} {
		if !strings.Contains(out, want) {
			t.Fatalf("web_search = %q, want %q", out, want)
		}
	}
	if got := <-requests; got.Limit != 20 {
		t.Fatalf("request limit = %d, want the capped default 20", got.Limit)
	}
}

func TestWebSearchWithoutConfigPointsToWebFetch(t *testing.T) {
	t.Setenv("ARGENT_WEB_SEARCH_URL", "")
	registry := tools.New(t.TempDir(), true)
	_, err := registry.Run(context.Background(), "web_search", `{"query":"anything"}`)
	if err == nil || !strings.Contains(err.Error(), "web_fetch") || !strings.Contains(err.Error(), "ARGENT_WEB_SEARCH_URL") {
		t.Fatalf("err = %v, want a web_fetch hint", err)
	}
}

func TestWebSearchRejectsInvalidArgsAndEndpointErrors(t *testing.T) {
	t.Setenv("ARGENT_WEB_SEARCH_URL", "http://127.0.0.1:1")
	registry := tools.New(t.TempDir(), true)
	for _, args := range []string{`{"query":""}`, `{`, `{"query":123}`} {
		if _, err := registry.Run(context.Background(), "web_search", args); err == nil {
			t.Fatalf("web_search accepted invalid args %s", args)
		}
	}
	if _, err := registry.Run(context.Background(), "web_search", `{"query":"x"}`); err == nil || !strings.Contains(err.Error(), "web_search:") {
		t.Fatalf("err = %v, want an endpoint error", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer server.Close()
	t.Setenv("ARGENT_WEB_SEARCH_URL", server.URL)
	out, err := registry.Run(context.Background(), "web_search", `{"query":"x"}`)
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("err = %v out = %q, want a 429 error", err, out)
	}
}
