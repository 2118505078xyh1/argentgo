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

// failingRunner 在产生任何文本之前就失败，演练 200 头部
// 尚未提交的流前路径。
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

// TestPromptPreStreamFailureReturnsNonOKStatus 守护审计 #7：任何文本之前的
// 彻底失败不得以 200 加错误 SSE 事件回复，因为
// 只看状态码的客户端会把它读成成功。
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

// TestPromptPostStreamFailureKeepsOKAndSSEError 验证文本之后的失败
// 仍以原有方式报告：状态已提交为 200，因此
// 终态信号是 SSE 错误事件。
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

// TestPromptPreStreamCancellationStillReturnsCancelledEvent 固定
// 共享的取消语义：即使之前没有文本，中止也不是失败，
// 因此仍是带 type:cancelled 终态事件的 200。
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
