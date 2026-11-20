package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// cancellingRunner 流式输出一个分块，然后以 context.Canceled 失败，
// 这是 finalAnswer 取消传播修复后 Runner.Prompt 的形态。
type cancellingRunner struct {
	err error
}

func (r *cancellingRunner) Prompt(_ context.Context, _ string, onText func(string)) (string, error) {
	if onText != nil {
		onText("partial")
	}
	return "", r.err
}

func (r *cancellingRunner) Abort() {}

// TestPromptCancellationEmitsCancelledEvent 守护 HTTP 侧：用户中止
// 是正常的停止，因此终态 SSE 事件是 type:cancelled，绝不是 error。
func TestPromptCancellationEmitsCancelledEvent(t *testing.T) {
	h := NewHandler(Config{Token: "t", Runner: &cancellingRunner{
		err: fmt.Errorf("wrapped: %w", context.Canceled),
	}})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/prompt", strings.NewReader(`{"text":"hi"}`))
	req.Header.Set("Authorization", "Bearer t")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	events := decodeEvents(t, rec.Body.String())
	if len(events) != 2 {
		t.Fatalf("events = %d (%v), want 2", len(events), events)
	}
	if events[0]["type"] != "text" || events[0]["text"] != "partial" {
		t.Fatalf("event 0 = %v", events[0])
	}
	if events[1]["type"] != "cancelled" {
		t.Fatalf("terminal event = %v, want type:cancelled", events[1])
	}
	if _, ok := events[1]["error"]; ok {
		t.Fatalf("cancelled event carries an error: %v", events[1])
	}
}

// TestPromptRealFailureStillEmitsErrorEvent 让真正的失败保持
// 原有路径：只有取消才获得新的终态事件。
func TestPromptRealFailureStillEmitsErrorEvent(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"plain", errors.New("boom"), "boom"},
		{"deadline", context.DeadlineExceeded, context.DeadlineExceeded.Error()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := NewHandler(Config{Token: "t", Runner: &cancellingRunner{err: tc.err}})
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/prompt", strings.NewReader(`{"text":"hi"}`))
			req.Header.Set("Authorization", "Bearer t")
			h.ServeHTTP(rec, req)

			events := decodeEvents(t, rec.Body.String())
			if len(events) != 2 || events[1]["type"] != "error" || events[1]["error"] != tc.want {
				t.Fatalf("events = %v, want text then error:%q", events, tc.want)
			}
		})
	}
}

// TestRPCPromptCancellationReturnsNormalResult 验证 RPC 侧不再
// 把用户中止映射为 -32603 内部错误。
func TestRPCPromptCancellationReturnsNormalResult(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- RunRPC(context.Background(), inR, outW, Config{Runner: &cancellingRunner{
			err: fmt.Errorf("wrapped: %w", context.Canceled),
		}})
	}()
	enc := json.NewEncoder(inW)
	dec := json.NewDecoder(outR)

	if err := enc.Encode(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "prompt",
		"params": map[string]any{"text": "hi"},
	}); err != nil {
		t.Fatalf("encode prompt: %v", err)
	}
	if delta := readJSON(t, dec); delta["method"] != "prompt/delta" {
		t.Fatalf("first message = %v, want prompt/delta", delta)
	}

	result := readJSON(t, dec)
	if _, ok := result["error"]; ok {
		t.Fatalf("cancellation returned an error: %v", result)
	}
	body, _ := result["result"].(map[string]any)
	if body["stopReason"] != "cancelled" {
		t.Fatalf("prompt result = %v, want stopReason:cancelled", result)
	}

	if err := inW.Close(); err != nil {
		t.Fatalf("close stdin: %v", err)
	}
	waitFor(t, done)
}

// TestRPCPromptRealFailureStillReturnsError 为真正的失败保留 -32603。
func TestRPCPromptRealFailureStillReturnsError(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- RunRPC(context.Background(), inR, outW, Config{
			Runner: &cancellingRunner{err: errors.New("boom")},
		})
	}()
	enc := json.NewEncoder(inW)
	dec := json.NewDecoder(outR)

	if err := enc.Encode(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "prompt",
		"params": map[string]any{"text": "hi"},
	}); err != nil {
		t.Fatalf("encode prompt: %v", err)
	}
	readJSON(t, dec) // prompt/delta
	result := readJSON(t, dec)
	errBody, _ := result["error"].(map[string]any)
	if errBody["code"] != float64(-32603) || errBody["message"] != "boom" {
		t.Fatalf("failure response = %v, want -32603 boom", result)
	}

	if err := inW.Close(); err != nil {
		t.Fatalf("close stdin: %v", err)
	}
	waitFor(t, done)
}

// TestACPPromptCancellationKeepsStopReason 固定 ACP 已正确的线协议
// 语义，同时它共享各传输的取消检测。
func TestACPPromptCancellationKeepsStopReason(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- RunACP(context.Background(), inR, outW, Config{Runner: &cancellingRunner{
			err: fmt.Errorf("wrapped: %w", context.Canceled),
		}})
	}()
	enc := json.NewEncoder(inW)
	dec := json.NewDecoder(outR)

	_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "session/new"})
	created := readJSON(t, dec)
	createdBody, _ := created["result"].(map[string]any)
	sessionID, _ := createdBody["sessionId"].(string)

	_ = enc.Encode(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "session/prompt",
		"params": map[string]any{
			"sessionId": sessionID,
			"prompt":    []map[string]any{{"type": "text", "text": "hi"}},
		},
	})
	var result map[string]any
	for {
		msg := readJSON(t, dec)
		if msg["id"] == float64(2) {
			result = msg
			break
		}
		if msg["method"] != "session/update" {
			t.Fatalf("unexpected message = %v", msg)
		}
	}
	if _, ok := result["error"]; ok {
		t.Fatalf("cancellation returned an error: %v", result)
	}
	body, _ := result["result"].(map[string]any)
	if body["stopReason"] != "cancelled" {
		t.Fatalf("prompt result = %v, want stopReason:cancelled", result)
	}

	if err := inW.Close(); err != nil {
		t.Fatalf("close stdin: %v", err)
	}
	waitFor(t, done)
}
