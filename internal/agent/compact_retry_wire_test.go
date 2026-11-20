package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// noticesOf 返回接收器收集到的全部 EventNotice 正文，供压缩重试通知断言使用。
func noticesOf(s *eventSink) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, ev := range s.events {
		if ev.Kind == EventNotice {
			out = append(out, ev.Text)
		}
	}
	return out
}

// shrinkingCompactor 产生一个真正变短的摘要视图，并把最后一条原始消息留在末尾。
func shrinkingCompactor() *fakeCompactor {
	return &fakeCompactor{
		shorten: func(messages []llm.Message) []llm.Message {
			return []llm.Message{
				{Role: "system", Content: "摘要后的系统提示"},
				messages[len(messages)-1],
			}
		},
	}
}

// TestCompactRetryOnOverflowCompactsAndReplays 覆盖"溢出且未压缩过 -> 压缩并在同一轮重放"：
// 第一次请求溢出、第二次成功时，运行应按照成功的那一答结束，且轮次只增加一次。
func TestCompactRetryOnOverflowCompactsAndReplays(t *testing.T) {
	compactor := shrinkingCompactor()
	var seen [][]llm.Message
	chat := &fakeChatter{fn: func(n int, messages []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		seen = append(seen, append([]llm.Message(nil), messages...))
		if n == 1 {
			return llm.Message{}, errors.New("provider returned 400 Bad Request: prompt is too long")
		}
		return textReply("final"), nil
	}}
	sink := &eventSink{}
	runner := &Runner{Opts: Options{
		Model:              "m",
		Chatter:            chat,
		Tools:              &fakeTools{},
		MaxTurns:           3,
		EnableCompactRetry: true,
		Compactor:          compactor,
		Emit:               sink.emit,
	}}
	res, err := runner.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Stopped != StopComplete || res.Text != "final" {
		t.Fatalf("result = %q / %q, want complete / final", res.Stopped, res.Text)
	}
	if res.Turns != 1 {
		t.Fatalf("turns = %d, want 1 (replay must not consume an extra turn)", res.Turns)
	}
	if chat.count() != 2 {
		t.Fatalf("model requests = %d, want 2 (overflow + replay)", chat.count())
	}
	if compactor.calls != 1 {
		t.Fatalf("compactor calls = %d, want 1", compactor.calls)
	}
	if len(seen) != 2 || seen[1][0].Content != "摘要后的系统提示" {
		t.Fatalf("replay did not use the compacted history: %+v", seen)
	}
	var compactNotices int
	for _, text := range noticesOf(sink) {
		if strings.Contains(text, "上下文溢出") {
			compactNotices++
		}
	}
	if compactNotices != 1 {
		t.Fatalf("compact notices = %d, want 1", compactNotices)
	}
	if _, ok := errorEventOf(sink); ok {
		t.Fatal("successful replay must not leave an EventError behind")
	}
}

// TestCompactRetryStoppedAfterBudgetUsed 覆盖"已压缩过 -> 不再重试"：
// 预算用尽后即使再次溢出也必须立刻走原有失败路径，连压缩器都不再调用。
func TestCompactRetryStoppedAfterBudgetUsed(t *testing.T) {
	chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
		return llm.Message{}, errors.New("prompt is too long")
	}}
	compactor := shrinkingCompactor()
	s := newRunState(Options{
		Model:              "m",
		Chatter:            chat,
		Tools:              &fakeTools{},
		EnableCompactRetry: true,
		Compactor:          compactor,
	}, "hi", nil)
	s.compactRetries = MaxCompactRetries
	res, err := s.loop(context.Background())
	if err == nil {
		t.Fatal("expected provider error")
	}
	if res.Stopped != StopProviderError {
		t.Fatalf("stopped = %q, want %q", res.Stopped, StopProviderError)
	}
	if chat.count() != 1 {
		t.Fatalf("model requests = %d, want 1 (no replay once budget is used)", chat.count())
	}
	if compactor.calls != 0 {
		t.Fatalf("compactor calls = %d, want 0", compactor.calls)
	}
}

// TestCompactRetryIgnoresNonOverflowFailures 覆盖"非溢出错误 -> 不重试"：
// 封闭失败词汇表中除上下文溢出外的类别都保持原有的单次失败语义。
func TestCompactRetryIgnoresNonOverflowFailures(t *testing.T) {
	cases := []struct {
		name string
		err  error
		kind llm.FailureKind
	}{
		{"auth", errors.New("provider returned 401 Unauthorized: invalid api key"), llm.FailureAuth},
		{"quota", errors.New("配额已用尽"), llm.FailureQuota},
		{"rate limit", errors.New("provider returned 429 Too Many Requests: rate limit exceeded"), llm.FailureRateLimit},
		{"transient", errors.New("provider returned 503 Service Unavailable"), llm.FailureTransient},
		{"unknown", errors.New("some unrelated failure"), llm.FailureUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
				return llm.Message{}, tc.err
			}}
			compactor := shrinkingCompactor()
			sink := &eventSink{}
			s := newRunState(Options{
				Model:              "m",
				Chatter:            chat,
				Tools:              &fakeTools{},
				EnableCompactRetry: true,
				Compactor:          compactor,
			}, "hi", sink.emit)
			res, err := s.loop(context.Background())
			if err == nil {
				t.Fatal("expected provider error")
			}
			if res.Stopped != StopProviderError {
				t.Fatalf("stopped = %q, want %q", res.Stopped, StopProviderError)
			}
			if chat.count() != 1 || compactor.calls != 0 {
				t.Fatalf("requests = %d, compactor calls = %d; want 1 / 0", chat.count(), compactor.calls)
			}
			ev, ok := errorEventOf(sink)
			if !ok || ev.Failure == nil {
				t.Fatal("missing classified error event")
			}
			if ev.Failure.Kind != tc.kind {
				t.Fatalf("failure kind = %q, want %q", ev.Failure.Kind, tc.kind)
			}
		})
	}
}

// TestCompactRetryDisabledByDefault 覆盖 opt-in 默认值：开关关闭时即便配置了压缩器和溢出错误，
// 也不会压缩、不会重放，错误事件与停止原因保持引入该路径之前的形态。
func TestCompactRetryDisabledByDefault(t *testing.T) {
	overflow := errors.New("provider returned 400 Bad Request: prompt is too long")
	chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
		return llm.Message{}, overflow
	}}
	compactor := shrinkingCompactor()
	sink := &eventSink{}
	runner := &Runner{Opts: Options{
		Model: "m", Chatter: chat, Tools: &fakeTools{},
		Compactor: compactor, Emit: sink.emit,
	}}
	res, err := runner.Run(context.Background(), "hi")
	if err == nil {
		t.Fatal("expected provider error")
	}
	if !strings.Contains(err.Error(), overflow.Error()) {
		t.Fatalf("err = %q, want it to preserve %q", err, overflow.Error())
	}
	if res.Stopped != StopProviderError {
		t.Fatalf("stopped = %q, want %q", res.Stopped, StopProviderError)
	}
	if chat.count() != 1 || compactor.calls != 0 {
		t.Fatalf("requests = %d, compactor calls = %d; want 1 / 0", chat.count(), compactor.calls)
	}
	ev, ok := errorEventOf(sink)
	if !ok || ev.Failure == nil || ev.Failure.Kind != llm.FailureContextOverflow {
		t.Fatalf("error event = %+v, want classified context overflow", ev)
	}
	for _, text := range noticesOf(sink) {
		if strings.Contains(text, "上下文溢出") {
			t.Fatalf("disabled path emitted compact retry notice: %q", text)
		}
	}
}

// TestCompactRetryReplayFailureStopsRun 覆盖"重放再失败 -> 结束整轮"：
// 重放仍溢出时以重放后的错误走原有失败路径，绝不发起第三次请求。
func TestCompactRetryReplayFailureStopsRun(t *testing.T) {
	overflow := errors.New("provider returned 400 Bad Request: prompt is too long")
	chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
		return llm.Message{}, overflow
	}}
	compactor := shrinkingCompactor()
	sink := &eventSink{}
	runner := &Runner{Opts: Options{
		Model:              "m",
		Chatter:            chat,
		Tools:              &fakeTools{},
		MaxTurns:           3,
		EnableCompactRetry: true,
		Compactor:          compactor,
		Emit:               sink.emit,
	}}
	res, err := runner.Run(context.Background(), "hi")
	if err == nil {
		t.Fatal("expected provider error after failed replay")
	}
	if res.Stopped != StopProviderError {
		t.Fatalf("stopped = %q, want %q", res.Stopped, StopProviderError)
	}
	if chat.count() != 2 {
		t.Fatalf("model requests = %d, want 2 (no third request)", chat.count())
	}
	if compactor.calls != 1 {
		t.Fatalf("compactor calls = %d, want 1", compactor.calls)
	}
	ev, ok := errorEventOf(sink)
	if !ok || ev.Failure == nil {
		t.Fatal("missing classified error event for failed replay")
	}
	if ev.Failure.Kind != llm.FailureContextOverflow || !strings.Contains(ev.Err, overflow.Error()) {
		t.Fatalf("error event = %+v, want overflow with replayed error", ev)
	}
	if !sink.has(EventStop) {
		t.Fatal("failed replay was not followed by a stop event")
	}
}

// TestCompactRetryNoopCompactionDoesNotReplay 防止压缩器声称未缩短记录时空放一次请求。
func TestCompactRetryNoopCompactionDoesNotReplay(t *testing.T) {
	chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
		return llm.Message{}, errors.New("prompt is too long")
	}}
	compactor := &fakeCompactor{noChange: true}
	runner := &Runner{Opts: Options{
		Model: "m", Chatter: chat, Tools: &fakeTools{},
		EnableCompactRetry: true, Compactor: compactor,
	}}
	res, err := runner.Run(context.Background(), "hi")
	if err == nil {
		t.Fatal("expected provider error")
	}
	if res.Stopped != StopProviderError {
		t.Fatalf("stopped = %q, want %q", res.Stopped, StopProviderError)
	}
	if chat.count() != 1 {
		t.Fatalf("model requests = %d, want 1 (no replay when compaction is a no-op)", chat.count())
	}
	if compactor.calls != 1 {
		t.Fatalf("compactor calls = %d, want 1", compactor.calls)
	}
}
