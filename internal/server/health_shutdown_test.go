package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHealthReportsOKWithVersion(t *testing.T) {
	h := NewHandler(Config{Token: "t", Runner: &fakeRunner{}, Version: "1.2.3"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode health body %q: %v", rec.Body.String(), err)
	}
	if body["status"] != "ok" || body["version"] != "1.2.3" {
		t.Fatalf("health body = %v, want status=ok version=1.2.3", body)
	}
}

func TestHealthReturns503WithStructuredErrorWhenClosing(t *testing.T) {
	h := newHandler(Config{Token: "t", Runner: &fakeRunner{}, Version: "1.2.3"})
	h.beginShutdown()

	rec := httptest.NewRecorder()
	h.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/health", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode closing health body %q: %v", rec.Body.String(), err)
	}
	if body["status"] != "closing" {
		t.Fatalf("status = %v, want closing", body["status"])
	}
	message, ok := body["error"].(string)
	if !ok || message == "" {
		t.Fatalf("error = %v, want a non-empty message", body["error"])
	}
}

func TestHealthStillRequiresGetWhenClosing(t *testing.T) {
	h := newHandler(Config{Token: "t", Runner: &fakeRunner{}})
	h.beginShutdown()

	rec := httptest.NewRecorder()
	h.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/health", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestServerShutdownSetsClosingFlag(t *testing.T) {
	h := newHandler(Config{Token: "t", Runner: &fakeRunner{}})
	srv := newServer(h)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	// Shutdown 在 goroutine 中启动已注册的钩子，因此应短暂等待
	// 原子标志，而不是假设回调在返回时已经完成。
	deadline := time.Now().Add(time.Second)
	for !h.closing.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !h.closing.Load() {
		t.Fatal("Shutdown did not set the closing flag")
	}

	rec := httptest.NewRecorder()
	h.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/health", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("health after shutdown = %d, want 503", rec.Code)
	}
}
