package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// errorEventOf 返回接收器收集到的第一个 EventError（如果有）。
func errorEventOf(s *eventSink) (Event, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ev := range s.events {
		if ev.Kind == EventError {
			return ev, true
		}
	}
	return Event{}, false
}

// TestErrorEventCarriesFailureClassification 让真实的提供方错误经过聊天失败发出点，检查封闭词汇表、重试与设置路由提示以及固定 ErrorID
// 都到达事件，同时原始 Err 文本保持不变。
// （接上行）
// （接上行）
func TestErrorEventCarriesFailureClassification(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		err        error
		wantKind   llm.FailureKind
		wantAction llm.UserAction
		wantRetry  bool
		wantID     string
	}{
		{"auth", errors.New("provider returned 401 Unauthorized: invalid api key"), llm.FailureAuth, llm.UserActionSettings, false, "auth.invalid_key"},
		{"quota", errors.New("配额已用尽"), llm.FailureQuota, llm.UserActionSettings, false, "quota.exhausted"},
		{"rate limit", errors.New("provider returned 429 Too Many Requests: rate limit exceeded"), llm.FailureRateLimit, llm.UserActionRetry, true, "rate_limit.exceeded"},
		{"content filter", errors.New("provider returned 400 Bad Request: content_policy_violation"), llm.FailureAgentAction, llm.UserActionRetry, false, "agent_action.content_filter"},
		{"model not found", errors.New("provider returned 404 Not Found: model gpt-foo not found"), llm.FailureConfig, llm.UserActionSettings, false, "config.model_not_found"},
		{"network", errors.New("dial tcp 10.0.0.1:443: connection refused"), llm.FailureTransient, llm.UserActionRetry, true, "transient.network"},
		{"server", errors.New("provider returned 503 Service Unavailable"), llm.FailureTransient, llm.UserActionRetry, true, "transient.server"},
		{"unknown", errors.New("some unrelated failure"), llm.FailureUnknown, llm.UserActionNone, false, "unknown"},
	}
	// FailureInternal 保留给进程内故障；ClassifyFailure 从不发出它，因此刻意不在表中。
	// （接上行）
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &eventSink{}
			chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
				return llm.Message{}, tc.err
			}}
			runner := &Runner{Opts: Options{
				Model: "m", Chatter: chat, Tools: &fakeTools{}, Emit: sink.emit,
			}}
			res, err := runner.Run(context.Background(), "hi")
			if err == nil {
				t.Fatal("run unexpectedly succeeded")
			}
			if res.Stopped != StopProviderError {
				t.Fatalf("stopped = %q, want %q", res.Stopped, StopProviderError)
			}
			ev, ok := errorEventOf(sink)
			if !ok {
				t.Fatal("no error event emitted")
			}
			if !strings.Contains(ev.Err, tc.err.Error()) {
				t.Fatalf("Err = %q, want it to preserve %q", ev.Err, tc.err.Error())
			}
			f := ev.Failure
			if f == nil {
				t.Fatal("error event has no Failure classification")
			}
			if f.Kind != tc.wantKind || f.UserAction != tc.wantAction || f.Retryable != tc.wantRetry || f.ErrorID != tc.wantID {
				t.Fatalf("Failure = %+v, want kind=%q action=%q retry=%v id=%q", *f, tc.wantKind, tc.wantAction, tc.wantRetry, tc.wantID)
			}
			if !sink.has(EventStop) {
				t.Fatal("error event was not followed by a stop event")
			}
		})
	}
}

// TestErrorEventFromBeforeRequestHookCarriesFailure 覆盖另一个发出点：持久性钩子失败被同样分类，其 Err 是未经修改的钩子错误（无回退包装）。
// （接上行）
// （接上行）
func TestErrorEventFromBeforeRequestHookCarriesFailure(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("provider returned 402 Payment Required: insufficient quota")
	sink := &eventSink{}
	chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
		t.Fatal("chatter must not run when the before-request hook fails")
		return llm.Message{}, nil
	}}
	runner := &Runner{Opts: Options{
		Model: "m", Chatter: chat, Tools: &fakeTools{}, Emit: sink.emit,
		BeforeRequest: func() error { return wantErr },
	}}
	res, err := runner.Run(context.Background(), "hi")
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if res.Stopped != StopProviderError {
		t.Fatalf("stopped = %q, want %q", res.Stopped, StopProviderError)
	}
	ev, ok := errorEventOf(sink)
	if !ok {
		t.Fatal("no error event emitted")
	}
	if ev.Err != wantErr.Error() {
		t.Fatalf("Err = %q, want exact %q", ev.Err, wantErr.Error())
	}
	if ev.Failure == nil {
		t.Fatal("no Failure classification on hook error event")
	}
	if want := llm.ClassifyFailure(wantErr); *ev.Failure != want {
		t.Fatalf("Failure = %+v, want %+v", *ev.Failure, want)
	}
	if ev.Failure.Kind != llm.FailureQuota || ev.Failure.UserAction != llm.UserActionSettings || ev.Failure.Retryable {
		t.Fatalf("quota failure should be a non-retryable settings failure: %+v", *ev.Failure)
	}
}

// TestErrorEventNilFailureKeepsLegacyJSON 证明新字段纯粹是附加的：nil 的 Failure 在 JSON 中被省略，
// 因此较旧的生产者以及对整个 Event 做 json.Marshal 的 SSE/RPC 传输看到的线上输出逐字节不变，
// 而旧版 JSON（没有 failure 键）解码出的 Failure 为 nil。
// （接上行）
func TestErrorEventNilFailureKeepsLegacyJSON(t *testing.T) {
	t.Parallel()
	ev := Event{Kind: EventError, Turn: 3, Err: "boom", Reason: StopProviderError}
	if ev.Failure != nil {
		t.Fatal("Failure must default to nil")
	}
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "failure") || strings.Contains(string(data), "Failure") {
		t.Fatalf("nil Failure leaked into JSON: %s", data)
	}

	// Failure 出现之前的结构，所有字段顺序相同。字节相等是「nil 时不变」保证的最强形式。
	// （接上行）
	type legacyEvent struct {
		Kind            EventKind
		Turn            int
		Depth           int
		Text            string
		ToolCallID      string
		ToolName        string
		Args            string
		Intent          string
		Output          string
		IsError         bool
		DurationMS      int64
		Usage           llm.Usage
		TotalUsage      llm.Usage
		Err             string
		Reason          string
		PendingMessages int
		Checkpoint      *Checkpoint
	}
	legacy := legacyEvent{Kind: ev.Kind, Turn: ev.Turn, Err: ev.Err, Reason: ev.Reason}
	legacyJSON, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(legacyJSON) {
		t.Fatalf("nil-Failure JSON changed:\n got %s\nwant %s", data, legacyJSON)
	}

	var back Event
	if err := json.Unmarshal([]byte(`{"Kind":"error","Err":"legacy"}`), &back); err != nil {
		t.Fatal(err)
	}
	if back.Err != "legacy" || back.Failure != nil {
		t.Fatalf("legacy decode = %+v", back)
	}
}

// TestErrorEventFailureIDContainsNoSecret 在事件边界检查脱敏承诺：即使原始错误带有凭据，
// 分类的 ErrorID 也是固定的表标识符，其中不含任何机密。
// （接上行）
func TestErrorEventFailureIDContainsNoSecret(t *testing.T) {
	t.Parallel()
	const secret = "sk-proj-SUPERSECRET123"
	wantErr := errors.New("provider returned 401 Unauthorized: invalid api key " + secret)
	sink := &eventSink{}
	chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
		return llm.Message{}, wantErr
	}}
	runner := &Runner{Opts: Options{Model: "m", Chatter: chat, Tools: &fakeTools{}, Emit: sink.emit}}
	if _, err := runner.Run(context.Background(), "hi"); err == nil {
		t.Fatal("expected run error")
	}
	ev, ok := errorEventOf(sink)
	if !ok || ev.Failure == nil {
		t.Fatal("no classified error event")
	}
	if ev.Failure.ErrorID != "auth.invalid_key" {
		t.Fatalf("ErrorID = %q, want auth.invalid_key", ev.Failure.ErrorID)
	}
	if strings.Contains(ev.Failure.ErrorID, secret) || strings.Contains(ev.Failure.ErrorID, "sk-") {
		t.Fatalf("ErrorID leaked a secret: %q", ev.Failure.ErrorID)
	}
}
