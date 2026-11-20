package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// failingRunner fails before producing any text, exercising the pre-stream
// path where the 200 header has not been committed yet.
type failingRunner struct {
	err error
}

func (r *failingRunner) Prompt(_ context.Context, _ string, _ func(string)) (string, error) {
	return "", r.err
}

func (r *failingRunner) Abort() {}

func promptRequest(t *testing.T, runner Runner) *httptest.ResponseRecorder {
	t.Helper()
	h := NewHandler(Config{Token: "t", Runner: runner})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/prompt", strings.NewReader(`{"text":"hi"}`))
	req.Header.Set("Authorization", "Bearer t")
	h.ServeHTTP(rec, req)
	return rec
}

// TestPromptPreStreamFailureReturnsNonOKStatus guards audit #7: a total
// failure before any text must not answer 200 with an error SSE event, because
// a status-code-only client would read that as success.
func TestPromptPreStreamFailureReturnsNonOKStatus(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantStatus int
		wantMsg    string
	}{
		{"plain", errors.New("boom"), http.StatusInternalServerError, "boom"},
		{"deadline", context.DeadlineExceeded, http.StatusInternalServerError, context.DeadlineExceeded.Error()},
		{"not configured", errors.New("provider not configured"), http.StatusInternalServerError, "not configured"},
		{"unauthorized", errors.New("provider returned 401 Unauthorized: invalid key"), http.StatusUnauthorized, "401"},
		{"rate limited", errors.New("provider returned 429 Too Many Requests: slow down"), http.StatusTooManyRequests, "429"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := promptRequest(t, &failingRunner{err: tc.err})

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				t.Fatalf("content type = %q, want application/json", ct)
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode body %q: %v", rec.Body.String(), err)
			}
			msg, ok := body["error"].(string)
			if !ok || !strings.Contains(msg, tc.wantMsg) {
				t.Fatalf("body = %v, want error containing %q", body, tc.wantMsg)
			}
			if strings.Contains(rec.Body.String(), "data: ") {
				t.Fatalf("pre-stream failure leaked SSE data: %q", rec.Body.String())
			}
		})
	}
}

// TestPromptPostStreamFailureKeepsOKAndSSEError verifies a failure after text
// is still reported the original way: status is committed to 200, so the
// terminal signal is an SSE error event.
func TestPromptPostStreamFailureKeepsOKAndSSEError(t *testing.T) {
	rec := promptRequest(t, &cancellingRunner{err: errors.New("boom")})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	events := decodeEvents(t, rec.Body.String())
	if len(events) != 2 || events[0]["type"] != "text" || events[1]["type"] != "error" || events[1]["error"] != "boom" {
		t.Fatalf("events = %v, want text then error:boom", events)
	}
}

// TestPromptPreStreamCancellationStillReturnsCancelledEvent locks in the
// shared cancellation semantics: an abort is not a failure even when no text
// preceded it, so it stays a 200 with a type:cancelled terminal event.
func TestPromptPreStreamCancellationStillReturnsCancelledEvent(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"plain", context.Canceled},
		{"wrapped", fmt.Errorf("wrapped: %w", context.Canceled)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := promptRequest(t, &failingRunner{err: tc.err})

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 for cancellation", rec.Code)
			}
			events := decodeEvents(t, rec.Body.String())
			if len(events) != 1 || events[0]["type"] != "cancelled" {
				t.Fatalf("events = %v, want a single cancelled event", events)
			}
		})
	}
}
