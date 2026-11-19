package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type fakeRunner struct {
	mu      sync.Mutex
	reply   string
	chunks  []string
	prompts []string
	aborted bool
}

func (f *fakeRunner) Prompt(_ context.Context, text string, onText func(string)) (string, error) {
	f.mu.Lock()
	f.prompts = append(f.prompts, text)
	chunks := append([]string(nil), f.chunks...)
	reply := f.reply
	f.mu.Unlock()
	for _, chunk := range chunks {
		if onText != nil {
			onText(chunk)
		}
	}
	return reply, nil
}

func (f *fakeRunner) Abort() {
	f.mu.Lock()
	f.aborted = true
	f.mu.Unlock()
}

func (f *fakeRunner) abortedNow() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.aborted
}

func decodeEvents(t *testing.T, body string) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, block := range strings.Split(body, "\n\n") {
		block = strings.TrimSpace(block)
		if !strings.HasPrefix(block, "data: ") {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(block, "data: ")), &event); err != nil {
			t.Fatalf("decode event %q: %v", block, err)
		}
		events = append(events, event)
	}
	return events
}

func TestHealthNeedsNoToken(t *testing.T) {
	h := NewHandler(Config{Token: "s3cret", Runner: &fakeRunner{}, Version: "1.2.3"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("health status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"version":"1.2.3"`) {
		t.Fatalf("health body = %q", rec.Body.String())
	}
}

func TestPromptRejectsMissingOrWrongToken(t *testing.T) {
	h := NewHandler(Config{Token: "s3cret", Runner: &fakeRunner{reply: "hi"}})
	for name, header := range map[string]string{
		"missing": "",
		"wrong":   "Bearer nope",
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/prompt", strings.NewReader(`{"text":"hi"}`))
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s token: status = %d, want 401", name, rec.Code)
		}
	}
}

func TestPromptStreamsTextThenDone(t *testing.T) {
	runner := &fakeRunner{reply: "hello", chunks: []string{"hel", "lo"}}
	h := NewHandler(Config{Token: "s3cret", Runner: runner})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/prompt", strings.NewReader(`{"text":"say hi"}`))
	req.Header.Set("Authorization", "Bearer s3cret")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content type = %q", ct)
	}
	events := decodeEvents(t, rec.Body.String())
	if len(events) != 3 {
		t.Fatalf("events = %d (%v), want 3", len(events), events)
	}
	if events[0]["type"] != "text" || events[0]["text"] != "hel" {
		t.Fatalf("event 0 = %v", events[0])
	}
	if events[1]["type"] != "text" || events[1]["text"] != "lo" {
		t.Fatalf("event 1 = %v", events[1])
	}
	if events[2]["type"] != "done" {
		t.Fatalf("event 2 = %v", events[2])
	}
	if len(runner.prompts) != 1 || runner.prompts[0] != "say hi" {
		t.Fatalf("prompts = %v", runner.prompts)
	}
}

func TestPromptAcceptsRawTextBody(t *testing.T) {
	runner := &fakeRunner{reply: "ok"}
	h := NewHandler(Config{Token: "t", Runner: runner})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/prompt", strings.NewReader("plain text"))
	req.Header.Set("Authorization", "Bearer t")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if runner.prompts[0] != "plain text" {
		t.Fatalf("prompt = %q", runner.prompts[0])
	}
}

func TestPromptRejectsEmptyText(t *testing.T) {
	h := NewHandler(Config{Token: "t", Runner: &fakeRunner{}})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/prompt", strings.NewReader(`{"text":"   "}`))
	req.Header.Set("Authorization", "Bearer t")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestAbortCallsRunner(t *testing.T) {
	runner := &fakeRunner{}
	h := NewHandler(Config{Token: "t", Runner: runner})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/abort", nil)
	req.Header.Set("Authorization", "Bearer t")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !runner.abortedNow() {
		t.Fatal("runner was not aborted")
	}
}

func TestServeRequiresToken(t *testing.T) {
	if _, _, err := Listen(Config{Runner: &fakeRunner{}}); err == nil {
		t.Fatal("Listen accepted a config without a token")
	}
}

func TestServeRequiresRunner(t *testing.T) {
	if _, _, err := Listen(Config{Token: "t"}); err == nil {
		t.Fatal("Listen accepted a config without a runner")
	}
}

func TestPromptMethodNotAllowed(t *testing.T) {
	h := NewHandler(Config{Token: "t", Runner: &fakeRunner{}})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/prompt", nil)
	req.Header.Set("Authorization", "Bearer t")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

type fakeSessions struct {
	infos  []SessionInfo
	events []SessionEvent
	err    error
}

func (f fakeSessions) List() ([]SessionInfo, error) { return f.infos, f.err }
func (f fakeSessions) Load(string) ([]SessionEvent, error) {
	return f.events, f.err
}
