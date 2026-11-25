package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"example.com/argentgo/internal/agent"
)

// disconnectRunner 模拟一个长时间运行的上游 agent：它阻塞直到
// 其 context 被取消，然后报告该取消。它使测试能
// 观察 SSE 传输在客户端断开时是否真的停止该运行，
// 而不是让它在后台继续消耗 token。
type disconnectRunner struct {
	started   chan struct{}
	cancelled chan struct{}
}

func (r *disconnectRunner) Prompt(ctx context.Context, _ string, _ func(string)) (string, error) {
	close(r.started)
	<-ctx.Done()
	close(r.cancelled)
	return "", ctx.Err()
}

func (r *disconnectRunner) Abort() {}

// TestPromptClientDisconnectCancelsUpstream 验证 /v1/prompt 用源自请求的
// context 驱动 Runner.Prompt。当客户端离开（请求 context 被取消）时，
// 上游运行必须被取消，且处理器必须及时返回，
// 后台不留任何运行。
func TestPromptClientDisconnectCancelsUpstream(t *testing.T) {
	runner := &disconnectRunner{
		started:   make(chan struct{}),
		cancelled: make(chan struct{}),
	}
	h := NewHandler(Config{Token: "t", Runner: runner})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequest(http.MethodPost, "/v1/prompt", strings.NewReader(`{"text":"hi"}`)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer t")
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(rec, req)
	}()

	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not start")
	}

	// 模拟客户端在运行进行中断开。
	cancel()

	select {
	case <-runner.cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream run was not cancelled on client disconnect")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("prompt handler did not return after client disconnect")
	}
}

// subscriberCount 报告 hub 中活跃订阅者的数量。它放在测试文件中，
// 使生产环境的 EventHub API 保持不变。
func (h *EventHub) subscriberCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs)
}

// TestEventStreamUnsubscribesOnClientDisconnect 验证 /v1/event 的
// SSE 处理器在客户端断开时释放其 hub 订阅和 servicing goroutine。
// 泄漏的订阅会继续接收发布的事件，泄漏的 goroutine 会让
// 心跳定时器一直活着。
func TestEventStreamUnsubscribesOnClientDisconnect(t *testing.T) {
	hub := NewEventHub()
	handler := newEventHandlerInterval(hub, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	rec, done := startEventStream(t, handler, ctx)

	rec.waitContains(t, "event: server.connected")
	if got := hub.subscriberCount(); got != 1 {
		t.Fatalf("subscribers while streaming = %d, want 1", got)
	}

	if !stopEventStream(cancel, done) {
		t.Fatal("event handler did not stop on client disconnect")
	}
	if got := hub.subscriberCount(); got != 0 {
		t.Fatalf("subscribers after disconnect = %d, want 0 (subscription leak)", got)
	}

	// 客户端离开后发布不得在陈旧的订阅者上 panic 或阻塞；
	// hub 应完全没有活跃订阅者。
	hub.Publish(agent.Event{Kind: agent.EventNotice, Text: "after-disconnect"})
	if got := hub.subscriberCount(); got != 0 {
		t.Fatalf("subscribers after publish = %d, want 0", got)
	}
}
