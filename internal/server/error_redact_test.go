package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// credentialBearingError 模拟一种提供方失败，其错误体回显了
// 服务器自己的密钥。提供方错误体被 agent/llm 层原样返回；
// 这些片段都不得到达传输客户端，客户端只持有 bearer token，
// 绝不应该知道配置的 API key。
func credentialBearingError() error {
	return errors.New(
		"provider returned 401 Unauthorized: api_key=sk-testabcdef0123456789 " +
			"password=hunter2secret99 user@example.com " +
			"Authorization: Bearer sk-anotherkey0123456789",
	)
}

var credentialLeaks = []string{
	"sk-testabcdef0123456789",
	"hunter2secret99",
	"user@example.com",
	"sk-anotherkey0123456789",
}

func assertNoCredentialLeak(t *testing.T, wire string) {
	t.Helper()
	for _, secret := range credentialLeaks {
		if strings.Contains(wire, secret) {
			t.Fatalf("wire reply leaked %q: %s", secret, wire)
		}
	}
	if !strings.Contains(wire, "401") {
		t.Fatalf("wire reply lost the canonical reason: %s", wire)
	}
}

func TestWireErrorTextRedactsCredentials(t *testing.T) {
	got := wireErrorText(credentialBearingError())
	assertNoCredentialLeak(t, got)
	if !strings.Contains(got, "provider returned 401 Unauthorized") {
		t.Fatalf("wireErrorText = %q, want the error wording preserved", got)
	}
}

// TestHTTPPromptFailureRedactsCredentials 覆盖两条 HTTP 失败路径：
// 产生任何文本之前的失败（非 2xx 状态的 JSON 错误体），以及流
// 已提交之后的失败（终态 SSE 错误事件）。两者过去都携带
// 未经处理的提供方错误。
func TestHTTPPromptFailureRedactsCredentials(t *testing.T) {
	t.Run("pre-stream JSON error", func(t *testing.T) {
		h := NewHandler(Config{Token: "t", Runner: &failingRunner{err: credentialBearingError()}})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/prompt", strings.NewReader(`{"text":"hi"}`))
		req.Header.Set("Authorization", "Bearer t")
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401 (auth family)", rec.Code)
		}
		assertNoCredentialLeak(t, rec.Body.String())
	})

	t.Run("post-stream SSE error", func(t *testing.T) {
		h := NewHandler(Config{Token: "t", Runner: &cancellingRunner{err: credentialBearingError()}})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/prompt", strings.NewReader(`{"text":"hi"}`))
		req.Header.Set("Authorization", "Bearer t")
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (header already committed)", rec.Code)
		}
		events := decodeEvents(t, rec.Body.String())
		if len(events) != 2 || events[1]["type"] != "error" {
			t.Fatalf("events = %v, want text then error", events)
		}
		errText, _ := events[1]["error"].(string)
		assertNoCredentialLeak(t, errText)
	})
}

// TestRPCPromptFailureRedactsCredentials 驱动真实的 --rpc prompt 方法，
// 检查跨线传输的 -32603 错误消息。
func TestRPCPromptFailureRedactsCredentials(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- RunRPC(context.Background(), inR, outW, Config{
			Runner: &failingRunner{err: credentialBearingError()},
		})
	}()

	if err := json.NewEncoder(inW).Encode(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "prompt",
		"params": map[string]any{"text": "hi"},
	}); err != nil {
		t.Fatalf("encode prompt: %v", err)
	}
	resp := readJSON(t, json.NewDecoder(outR))
	body, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected a JSON-RPC error, got %v", resp)
	}
	msg, _ := body["message"].(string)
	assertNoCredentialLeak(t, msg)

	_ = inW.Close()
	waitFor(t, done)
}

// TestACPPromptFailureRedactsCredentials 驱动真实的 ACP prompt 方法，
// 检查跨线传输的 -32603 错误消息。
func TestACPPromptFailureRedactsCredentials(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- RunACP(context.Background(), inR, outW, Config{
			Runner: &failingRunner{err: credentialBearingError()},
		})
	}()

	enc := json.NewEncoder(inW)
	dec := json.NewDecoder(outR)

	// 先创建会话：ACP 在 runner 被调用之前就会拒绝针对未知会话 id 的提示，
	// 而那个校验错误并不是被测的
	// 提供方失败。
	_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "session/new"})
	created := readJSON(t, dec)
	createdBody, _ := created["result"].(map[string]any)
	sid, _ := createdBody["sessionId"].(string)

	_ = enc.Encode(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "session/prompt",
		"params": map[string]any{"sessionId": sid, "prompt": "hi"},
	})
	var promptResult map[string]any
	for {
		msg := readJSON(t, dec)
		if msg["id"] == float64(2) {
			promptResult = msg
			break
		}
		if msg["method"] != "session/update" {
			t.Fatalf("unexpected message = %v", msg)
		}
	}
	body, ok := promptResult["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected a JSON-RPC error, got %v", promptResult)
	}
	msg, _ := body["message"].(string)
	assertNoCredentialLeak(t, msg)

	_ = inW.Close()
	waitFor(t, done)
}
