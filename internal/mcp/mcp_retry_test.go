package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// writeRPCResult 给一条 JSON-RPC 请求回送成功结果，原样回显 id。
func writeRPCResult(t *testing.T, w http.ResponseWriter, r *http.Request, result any) {
	t.Helper()
	var message map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&message); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      message["id"],
		"result":  result,
	}); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

// retryServer 起一个按 POST 次数分派的流式 MCP 端点；GET 一律拒绝，
// 使 newHTTPTransport 走单端点形态。
func retryServer(t *testing.T, onPost func(attempt int, w http.ResponseWriter, r *http.Request)) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	posts := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			http.Error(w, "no stream", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// 请求通知（如 notifications/initialized）没有 id，不属于
		// 被重试的远程调用；计数只覆盖真正的 JSON-RPC 请求。
		var message map[string]json.RawMessage
		if err := json.Unmarshal(body, &message); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if _, hasID := message["id"]; !hasID {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		attempt := int(posts.Add(1))
		onPost(attempt, w, r)
	}))
	t.Cleanup(server.Close)
	return server, posts
}

func connectRetryClient(t *testing.T, server *httptest.Server) Client {
	t.Helper()
	c, err := Connect(context.Background(), Spec{
		Name:      "retry-test",
		Transport: "http",
		URL:       server.URL,
		Timeout:   3 * time.Second,
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestPostTypedHTTPStatusError(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		body        string
		wantRetry   bool
		wantAuthErr bool
	}{
		{name: "gateway", status: 503, body: "服务暂时不可用", wantRetry: true},
		{name: "bad gateway", status: 502, body: "bad gateway", wantRetry: true},
		{name: "timeout", status: 504, body: "gateway timeout", wantRetry: true},
		{name: "server error", status: 500, body: "boom"},
		{name: "unauthorized", status: 401, body: "need token", wantAuthErr: true},
		{name: "forbidden", status: 403, body: "无权限访问", wantAuthErr: true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			server, _ := retryServer(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
				http.Error(w, tt.body, tt.status)
			})
			tr, err := newHTTPTransport(context.Background(), Spec{
				Name: "typed", Transport: "http", URL: server.URL, Timeout: 2 * time.Second,
			})
			if err != nil {
				t.Fatalf("new transport: %v", err)
			}
			defer tr.Close()

			_, err = tr.post(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
			if err == nil {
				t.Fatal("post 返回 nil 错误")
			}
			var statusErr *HTTPStatusError
			if !errors.As(err, &statusErr) {
				t.Fatalf("错误链缺少 *HTTPStatusError: %v", err)
			}
			if statusErr.StatusCode != tt.status {
				t.Fatalf("StatusCode = %d, 期望 %d", statusErr.StatusCode, tt.status)
			}
			// CJK 响应体按 rune 原样保留，不截断成半个汉字。
			if want := strings.TrimSpace(tt.body); statusErr.Message != want {
				t.Fatalf("Message = %q, 期望 %q", statusErr.Message, want)
			}
			if prefix := fmt.Sprintf("HTTP %d: ", tt.status); !strings.HasPrefix(err.Error(), prefix) {
				t.Fatalf("错误文本 %q 缺少前缀 %q", err.Error(), prefix)
			}
			if !strings.Contains(err.Error(), tt.body) {
				t.Fatalf("错误文本 %q 丢失响应体 %q", err.Error(), tt.body)
			}
			if got := IsRetryable(err); got != tt.wantRetry {
				t.Fatalf("IsRetryable(%v) = %v, 期望 %v", err, got, tt.wantRetry)
			}
			if got := errors.Is(err, ErrAuthentication); got != tt.wantAuthErr {
				t.Fatalf("errors.Is(ErrAuthentication) = %v, 期望 %v", got, tt.wantAuthErr)
			}
		})
	}
}

func TestCallWithRetry(t *testing.T) {
	t.Run("503 then 200 within two attempts", func(t *testing.T) {
		t.Setenv(mcpRetryAttemptsEnv, "2")
		server, posts := retryServer(t, func(attempt int, w http.ResponseWriter, r *http.Request) {
			if attempt == 1 {
				http.Error(w, "service unavailable", http.StatusServiceUnavailable)
				return
			}
			writeRPCResult(t, w, r, map[string]any{
				"protocolVersion": "2024-11-05",
				"serverInfo":      map[string]any{"name": "retry-test", "version": "1"},
			})
		})
		info, err := connectRetryClient(t, server).Initialize(context.Background())
		if err != nil {
			t.Fatalf("initialize: %v", err)
		}
		if info.Name != "retry-test" {
			t.Fatalf("server name = %q", info.Name)
		}
		if got := posts.Load(); got != 2 {
			t.Fatalf("POST 次数 = %d, 期望 2", got)
		}
	})

	t.Run("401 sends exactly one request", func(t *testing.T) {
		t.Setenv(mcpRetryAttemptsEnv, "3")
		server, posts := retryServer(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "token expired", http.StatusUnauthorized)
		})
		_, err := connectRetryClient(t, server).Initialize(context.Background())
		if err == nil {
			t.Fatal("401 应返回错误")
		}
		if got := posts.Load(); got != 1 {
			t.Fatalf("401 POST 次数 = %d, 期望 1", got)
		}
		if !errors.Is(err, ErrAuthentication) {
			t.Fatalf("错误链缺少 ErrAuthentication: %v", err)
		}
		var statusErr *HTTPStatusError
		if !errors.As(err, &statusErr) || statusErr.StatusCode != 401 {
			t.Fatalf("错误链缺少 401 *HTTPStatusError: %v", err)
		}
	})

	t.Run("default one attempt baseline", func(t *testing.T) {
		t.Setenv(mcpRetryAttemptsEnv, "")
		server, posts := retryServer(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "暂时不可用", http.StatusServiceUnavailable)
		})
		_, err := connectRetryClient(t, server).Initialize(context.Background())
		if err == nil {
			t.Fatal("503 应返回错误")
		}
		if got := posts.Load(); got != 1 {
			t.Fatalf("默认 POST 次数 = %d, 期望 1", got)
		}
		var statusErr *HTTPStatusError
		if !errors.As(err, &statusErr) || statusErr.StatusCode != 503 {
			t.Fatalf("错误链缺少 503 *HTTPStatusError: %v", err)
		}
	})

	t.Run("cancelled context stops retry", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		calls := 0
		_, err := CallWithRetry(ctx, 3, func(ctx context.Context) (json.RawMessage, error) {
			calls++
			cancel() // 模拟调用方在收到 503 后立刻放弃
			return nil, &HTTPStatusError{StatusCode: 503, Message: "服务不可用"}
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, 期望 context.Canceled", err)
		}
		if calls != 1 {
			t.Fatalf("调用次数 = %d, 期望 1（取消后不得重试）", calls)
		}
	})

	t.Run("permanent error never retries", func(t *testing.T) {
		for _, status := range []int{401, 403, 500} {
			calls := 0
			wantErr := fmt.Errorf("HTTP %d: %w", status, &HTTPStatusError{
				StatusCode: status, Message: "失败",
			})
			_, err := CallWithRetry(context.Background(), 3, func(context.Context) (json.RawMessage, error) {
				calls++
				return nil, wantErr
			})
			if err != wantErr {
				t.Fatalf("status %d: err = %v, 期望原始错误", status, err)
			}
			if calls != 1 {
				t.Fatalf("status %d: 调用次数 = %d, 期望 1", status, calls)
			}
		}
	})

	t.Run("attempts clamp to three", func(t *testing.T) {
		calls := 0
		_, err := CallWithRetry(context.Background(), 99, func(context.Context) (json.RawMessage, error) {
			calls++
			return nil, &HTTPStatusError{StatusCode: 503, Message: "网关抖动"}
		})
		if err == nil {
			t.Fatal("耗尽尝试后应返回错误")
		}
		if calls != maxMCPRetryAttempts {
			t.Fatalf("调用次数 = %d, 期望 %d", calls, maxMCPRetryAttempts)
		}
	})
}
